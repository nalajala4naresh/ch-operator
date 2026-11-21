package controller

import (
	"context"
	"fmt"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	v1beta1 "github.com/nalajala4naresh/chvmm-api/v1beta1"

	"github.com/nalajala4naresh/ch-operator/pkg/evacuation"
	"github.com/nalajala4naresh/ch-operator/pkg/vmshutdown"
)

const (
	evacuationBackoffBase = 10 * time.Second
	evacuationBackoffMax  = 5 * time.Minute
	// evacuationWaitInterval is how often an evacuation held back by the concurrency
	// limits, or by a VM that is not yet migratable, is retried.
	evacuationWaitInterval = 10 * time.Second
)

// EvacuationReconciler live-migrates VMs that an eviction marked for evacuation (see
// pkg/evacuation). For each VM with Status.EvacuationNodeName set it creates a
// VirtualMachineMigration, retries failed ones with exponential backoff, and clears the
// mark once the VM no longer runs on that node.
type EvacuationReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder

	// MaxPerNode and MaxInCluster bound the evacuation migrations in flight off a single
	// node and across the cluster. Zero means unlimited.
	MaxPerNode   int
	MaxInCluster int
}

// +kubebuilder:rbac:groups=cloudhypervisor.quill.today,resources=virtualmachines,verbs=get;list;watch
// +kubebuilder:rbac:groups=cloudhypervisor.quill.today,resources=virtualmachines/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cloudhypervisor.quill.today,resources=virtualmachinemigrations,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;update;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;update;patch

func (r *EvacuationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	var vm v1beta1.VirtualMachine
	if err := r.Get(ctx, req.NamespacedName, &vm); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	node := vm.Status.EvacuationNodeName
	if node == "" {
		return ctrl.Result{}, nil
	}

	vmms, err := r.listVMMs(ctx, &vm)
	if err != nil {
		return ctrl.Result{}, err
	}

	// The VM has left the node, or stopped: the evacuation is over.
	if vm.DeletionTimestamp != nil || vm.Status.NodeName != node || !vmshutdown.IsActivePhase(vm.Status.Phase) {
		if vm.Status.NodeName != node && vmshutdown.IsActivePhase(vm.Status.Phase) {
			r.Recorder.Eventf(&vm, nil, corev1.EventTypeNormal, "Evacuated", "Evacuated", "VM evacuated from node %q to %q", node, vm.Status.NodeName)
		}
		if err := r.clearEvacuation(ctx, &vm); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, r.gcEvacuationVMMs(ctx, &vm, vmms)
	}

	// A migration of this VM is already under way, by evacuation or by a user.
	if vm.Status.Migration != nil {
		return ctrl.Result{}, nil
	}
	for i := range vmms {
		if !isTerminalMigrationPhase(vmms[i].Status.Phase) {
			return ctrl.Result{}, nil
		}
	}

	// Retry failed evacuation migrations with exponential backoff.
	if wait := evacuationBackoff(vmms, time.Now()); wait > 0 {
		log.Info("evacuation backing off after failed migration", "vm", vm.Name, "wait", wait)
		return ctrl.Result{RequeueAfter: wait}, nil
	}

	if ok, err := r.withinLimits(ctx, node); err != nil {
		return ctrl.Result{}, err
	} else if !ok {
		log.Info("evacuation waiting for migration slot", "vm", vm.Name, "node", node)
		return ctrl.Result{RequeueAfter: evacuationWaitInterval}, nil
	}

	vmm := &v1beta1.VirtualMachineMigration{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: vm.Name + "-evacuation-",
			Namespace:    vm.Namespace,
			Labels: map[string]string{
				evacuation.LabelEvacuation: "true",
				evacuation.LabelVMName:     vm.Name,
				evacuation.LabelSourceNode: node,
			},
		},
		Spec: v1beta1.VirtualMachineMigrationSpec{VMName: vm.Name},
	}
	if err := controllerutil.SetControllerReference(&vm, vmm, r.Scheme); err != nil {
		return ctrl.Result{}, fmt.Errorf("set evacuation migration owner: %w", err)
	}
	if err := r.Create(ctx, vmm); err != nil {
		// The migration webhook refuses VMs that are not currently migratable; wait for
		// the condition to change.
		if apierrors.IsInvalid(err) || apierrors.IsForbidden(err) {
			r.Recorder.Eventf(&vm, nil, corev1.EventTypeWarning, "EvacuationBlocked", "EvacuationBlocked", "Cannot live-migrate VM off node %q: %s", node, err)
			return ctrl.Result{RequeueAfter: evacuationWaitInterval}, nil
		}
		return ctrl.Result{}, fmt.Errorf("create evacuation migration: %w", err)
	}
	r.Recorder.Eventf(&vm, nil, corev1.EventTypeNormal, "EvacuationStarted", "EvacuationStarted", "Live-migrating VM off node %q (migration %q)", node, vmm.Name)
	return ctrl.Result{}, nil
}

// listVMMs returns every VirtualMachineMigration of the VM, evacuation or not.
func (r *EvacuationReconciler) listVMMs(ctx context.Context, vm *v1beta1.VirtualMachine) ([]v1beta1.VirtualMachineMigration, error) {
	var list v1beta1.VirtualMachineMigrationList
	if err := r.List(ctx, &list, client.InNamespace(vm.Namespace)); err != nil {
		return nil, fmt.Errorf("list migrations: %w", err)
	}
	var out []v1beta1.VirtualMachineMigration
	for _, vmm := range list.Items {
		if vmm.Spec.VMName == vm.Name {
			out = append(out, vmm)
		}
	}
	return out, nil
}

// withinLimits reports whether another evacuation migration off node may start.
func (r *EvacuationReconciler) withinLimits(ctx context.Context, node string) (bool, error) {
	if r.MaxPerNode <= 0 && r.MaxInCluster <= 0 {
		return true, nil
	}
	var list v1beta1.VirtualMachineMigrationList
	if err := r.List(ctx, &list, client.MatchingLabels{evacuation.LabelEvacuation: "true"}); err != nil {
		return false, fmt.Errorf("list evacuation migrations: %w", err)
	}
	inCluster, onNode := 0, 0
	for _, vmm := range list.Items {
		if isTerminalMigrationPhase(vmm.Status.Phase) {
			continue
		}
		inCluster++
		if vmm.Labels[evacuation.LabelSourceNode] == node {
			onNode++
		}
	}
	if r.MaxInCluster > 0 && inCluster >= r.MaxInCluster {
		return false, nil
	}
	if r.MaxPerNode > 0 && onNode >= r.MaxPerNode {
		return false, nil
	}
	return true, nil
}

// evacuationBackoff returns how long to wait before the next evacuation attempt, given
// the VM's migrations. Each failed evacuation migration doubles the wait, measured from
// the creation of the most recent one.
func evacuationBackoff(vmms []v1beta1.VirtualMachineMigration, now time.Time) time.Duration {
	failures := 0
	var last time.Time
	for _, vmm := range vmms {
		if vmm.Labels[evacuation.LabelEvacuation] != "true" || vmm.Status.Phase != v1beta1.VirtualMachineMigrationFailed {
			continue
		}
		failures++
		if t := vmm.CreationTimestamp.Time; t.After(last) {
			last = t
		}
	}
	if failures == 0 {
		return 0
	}
	backoff := evacuationBackoffMax
	if failures <= 6 {
		backoff = min(evacuationBackoffBase<<(failures-1), evacuationBackoffMax)
	}
	return max(last.Add(backoff).Sub(now), 0)
}

func (r *EvacuationReconciler) clearEvacuation(ctx context.Context, vm *v1beta1.VirtualMachine) error {
	patch := client.MergeFrom(vm.DeepCopy())
	vm.Status.EvacuationNodeName = ""
	if err := r.Status().Patch(ctx, vm, patch); err != nil {
		return client.IgnoreNotFound(err)
	}
	return nil
}

// gcEvacuationVMMs deletes the finished evacuation migrations of a VM once its evacuation
// is over, so that a later evacuation starts with a fresh backoff. The most recent
// successful one is kept as a record. A migration still referenced by the VM's status is
// left alone, so the migration controller can clear it first.
func (r *EvacuationReconciler) gcEvacuationVMMs(ctx context.Context, vm *v1beta1.VirtualMachine, vmms []v1beta1.VirtualMachineMigration) error {
	var done []v1beta1.VirtualMachineMigration
	for _, vmm := range vmms {
		if vmm.Labels[evacuation.LabelEvacuation] != "true" || !isTerminalMigrationPhase(vmm.Status.Phase) {
			continue
		}
		if vm.Status.Migration != nil && vm.Status.Migration.UID == vmm.UID {
			continue
		}
		done = append(done, vmm)
	}
	sort.Slice(done, func(i, j int) bool {
		return done[i].CreationTimestamp.After(done[j].CreationTimestamp.Time)
	})
	keptSuccess := false
	for i := range done {
		if !keptSuccess && done[i].Status.Phase == v1beta1.VirtualMachineMigrationSucceeded {
			keptSuccess = true
			continue
		}
		if err := r.Delete(ctx, &done[i]); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("delete evacuation migration %q: %w", done[i].Name, err)
		}
	}
	return nil
}

func (r *EvacuationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Only VMs marked for evacuation have work to do.
	marked := predicate.NewPredicateFuncs(func(obj client.Object) bool {
		vm, ok := obj.(*v1beta1.VirtualMachine)
		return ok && vm.Status.EvacuationNodeName != ""
	})
	return ctrl.NewControllerManagedBy(mgr).
		Named("evacuation").
		For(&v1beta1.VirtualMachine{}, builder.WithPredicates(marked)).
		// An evacuation migration finishing re-triggers its VM to retry or wrap up.
		Owns(&v1beta1.VirtualMachineMigration{}).
		Complete(r)
}
