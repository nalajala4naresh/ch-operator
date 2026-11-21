package controller

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1beta1 "github.com/nalajala4naresh/chvmm-api/v1beta1"

	"github.com/nalajala4naresh/ch-operator/pkg/evacuation"
)

func evacuatingVM(name string) *v1beta1.VirtualMachine {
	return &v1beta1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns", UID: types.UID(name + "-uid")},
		Status: v1beta1.VirtualMachineStatus{
			Phase:              v1beta1.VirtualMachineRunning,
			NodeName:           "node-a",
			VMPodName:          name + "-pod",
			EvacuationNodeName: "node-a",
		},
	}
}

func evacuationVMM(name, vmName string, phase v1beta1.VirtualMachineMigrationPhase, created time.Time) *v1beta1.VirtualMachineMigration {
	return &v1beta1.VirtualMachineMigration{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "ns", CreationTimestamp: metav1.NewTime(created),
			Labels: map[string]string{
				evacuation.LabelEvacuation: "true",
				evacuation.LabelVMName:     vmName,
				evacuation.LabelSourceNode: "node-a",
			},
		},
		Spec:   v1beta1.VirtualMachineMigrationSpec{VMName: vmName},
		Status: v1beta1.VirtualMachineMigrationStatus{Phase: phase},
	}
}

func newEvacuationReconciler(t *testing.T, objs ...client.Object) *EvacuationReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1beta1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1beta1.VirtualMachine{}, &v1beta1.VirtualMachineMigration{}).
		WithObjects(objs...).Build()
	return &EvacuationReconciler{Client: c, Scheme: scheme, Recorder: events.NewFakeRecorder(100)}
}

func reconcileVM(t *testing.T, r *EvacuationReconciler, name string) ctrl.Result {
	t.Helper()
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: name}})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return res
}

func evacuationVMMs(t *testing.T, r *EvacuationReconciler) []v1beta1.VirtualMachineMigration {
	t.Helper()
	var list v1beta1.VirtualMachineMigrationList
	if err := r.List(context.Background(), &list, client.MatchingLabels{evacuation.LabelEvacuation: "true"}); err != nil {
		t.Fatal(err)
	}
	return list.Items
}

func TestEvacuationCreatesMigration(t *testing.T) {
	r := newEvacuationReconciler(t, evacuatingVM("vm"))
	reconcileVM(t, r, "vm")

	vmms := evacuationVMMs(t, r)
	if len(vmms) != 1 {
		t.Fatalf("got %d evacuation migrations, want 1", len(vmms))
	}
	vmm := vmms[0]
	if vmm.Spec.VMName != "vm" || vmm.Labels[evacuation.LabelSourceNode] != "node-a" {
		t.Fatalf("unexpected migration %+v", vmm.ObjectMeta)
	}
	if owner := metav1.GetControllerOf(&vmm); owner == nil || owner.Name != "vm" {
		t.Fatalf("migration not owned by the VM: %+v", vmm.OwnerReferences)
	}

	// A second pass while that migration is in flight must not create another.
	reconcileVM(t, r, "vm")
	if n := len(evacuationVMMs(t, r)); n != 1 {
		t.Fatalf("got %d evacuation migrations after re-reconcile, want 1", n)
	}
}

func TestEvacuationWaitsForInFlightMigration(t *testing.T) {
	vm := evacuatingVM("vm")
	vm.Status.Migration = &v1beta1.VirtualMachineStatusMigration{Phase: v1beta1.VirtualMachineMigrationRunning}
	r := newEvacuationReconciler(t, vm)
	reconcileVM(t, r, "vm")
	if n := len(evacuationVMMs(t, r)); n != 0 {
		t.Fatalf("created %d migrations while one is in flight", n)
	}
}

func TestEvacuationRespectsPerNodeLimit(t *testing.T) {
	r := newEvacuationReconciler(t,
		evacuatingVM("vm"),
		evacuationVMM("other-evac", "other", v1beta1.VirtualMachineMigrationRunning, time.Now()),
	)
	r.MaxPerNode = 1
	res := reconcileVM(t, r, "vm")
	if res.RequeueAfter == 0 {
		t.Fatalf("expected requeue while at the per-node limit")
	}
	if n := len(evacuationVMMs(t, r)); n != 1 {
		t.Fatalf("got %d evacuation migrations, want only the existing one", n)
	}
}

func TestEvacuationBacksOffAfterFailure(t *testing.T) {
	r := newEvacuationReconciler(t,
		evacuatingVM("vm"),
		evacuationVMM("vm-evac-1", "vm", v1beta1.VirtualMachineMigrationFailed, time.Now()),
	)
	res := reconcileVM(t, r, "vm")
	if res.RequeueAfter <= 0 || res.RequeueAfter > evacuationBackoffBase {
		t.Fatalf("RequeueAfter = %v, want (0, %v]", res.RequeueAfter, evacuationBackoffBase)
	}
	if n := len(evacuationVMMs(t, r)); n != 1 {
		t.Fatalf("retried during backoff")
	}
}

func TestEvacuationBackoff(t *testing.T) {
	now := time.Now()
	failed := func(n int, at time.Time) []v1beta1.VirtualMachineMigration {
		var out []v1beta1.VirtualMachineMigration
		for i := 0; i < n; i++ {
			out = append(out, *evacuationVMM("x", "vm", v1beta1.VirtualMachineMigrationFailed, at))
		}
		return out
	}
	if got := evacuationBackoff(nil, now); got != 0 {
		t.Errorf("no failures: got %v, want 0", got)
	}
	if got := evacuationBackoff(failed(1, now), now); got != 10*time.Second {
		t.Errorf("1 failure: got %v, want 10s", got)
	}
	if got := evacuationBackoff(failed(3, now), now); got != 40*time.Second {
		t.Errorf("3 failures: got %v, want 40s", got)
	}
	if got := evacuationBackoff(failed(20, now), now); got != evacuationBackoffMax {
		t.Errorf("20 failures: got %v, want %v", got, evacuationBackoffMax)
	}
	if got := evacuationBackoff(failed(1, now.Add(-time.Minute)), now); got != 0 {
		t.Errorf("expired backoff: got %v, want 0", got)
	}
}

func TestEvacuationClearsAfterMoveAndCollectsMigrations(t *testing.T) {
	vm := evacuatingVM("vm")
	vm.Status.NodeName = "node-b"
	now := time.Now()
	r := newEvacuationReconciler(t, vm,
		evacuationVMM("vm-evac-1", "vm", v1beta1.VirtualMachineMigrationFailed, now.Add(-2*time.Minute)),
		evacuationVMM("vm-evac-2", "vm", v1beta1.VirtualMachineMigrationSucceeded, now.Add(-time.Minute)),
	)
	reconcileVM(t, r, "vm")

	var got v1beta1.VirtualMachine
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "ns", Name: "vm"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.EvacuationNodeName != "" {
		t.Fatalf("evacuationNodeName = %q, want cleared", got.Status.EvacuationNodeName)
	}
	vmms := evacuationVMMs(t, r)
	if len(vmms) != 1 || vmms[0].Name != "vm-evac-2" {
		t.Fatalf("remaining migrations = %v, want only the successful one", vmms)
	}
}
