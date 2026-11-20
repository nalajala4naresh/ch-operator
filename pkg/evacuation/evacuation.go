// Package evacuation decides how a VM responds to an eviction of its pod. It follows
// KubeVirt's model: an eviction of the pod of a VM whose EvictionStrategy asks for live
// migration is refused with 429 Too Many Requests (which kubectl drain and other Eviction
// API clients retry), and the VM is marked for evacuation by setting
// Status.EvacuationNodeName. The evacuation controller then creates a
// VirtualMachineMigration. Once the VM runs on another node its old pod is no longer the
// VM's pod, the retried eviction is allowed, and the drain completes.
package evacuation

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1beta1 "github.com/nalajala4naresh/chvmm-api/v1beta1"

	"github.com/nalajala4naresh/ch-operator/pkg/vmshutdown"
)

const (
	// LabelEvacuation marks VirtualMachineMigrations created to evacuate a VM.
	LabelEvacuation = "cloudhypervisor.quill.today/evacuation"
	// LabelVMName is the name of the VM an evacuation migration moves.
	LabelVMName = "cloudhypervisor.quill.today/evacuation-vm"
	// LabelSourceNode is the node an evacuation migration moves the VM off.
	LabelSourceNode = "cloudhypervisor.quill.today/evacuation-node"
)

// ParseStrategy validates a strategy name, such as the controller's default.
func ParseStrategy(s string) (v1beta1.EvictionStrategy, error) {
	switch st := v1beta1.EvictionStrategy(s); st {
	case v1beta1.EvictionStrategyNone, v1beta1.EvictionStrategyLiveMigrate, v1beta1.EvictionStrategyLiveMigrateIfPossible:
		return st, nil
	}
	return "", fmt.Errorf("unknown eviction strategy %q (want None, LiveMigrate or LiveMigrateIfPossible)", s)
}

// EffectiveStrategy returns the VM's eviction strategy, falling back to the cluster default.
func EffectiveStrategy(vm *v1beta1.VirtualMachine, clusterDefault v1beta1.EvictionStrategy) v1beta1.EvictionStrategy {
	if vm.Spec.EvictionStrategy != nil && *vm.Spec.EvictionStrategy != "" {
		return *vm.Spec.EvictionStrategy
	}
	if clusterDefault == "" {
		return v1beta1.EvictionStrategyNone
	}
	return clusterDefault
}

// NodeInUse reports whether a VM still needs the node daemon on node, and names it. The
// daemon drives live migration (its TLS relays carry the migration stream) and graceful
// guest shutdown, so it must outlive every VM that runs on, or migrates to or from, its
// node.
func NodeInUse(vms []v1beta1.VirtualMachine, node string) (bool, string) {
	for i := range vms {
		vm := &vms[i]
		if m := vm.Status.Migration; m != nil && (vm.Status.NodeName == node || m.TargetNodeName == node) {
			return true, fmt.Sprintf("VM %s/%s is migrating to or from node %s", vm.Namespace, vm.Name, node)
		}
		if vm.Status.NodeName == node && (vmshutdown.IsActivePhase(vm.Status.Phase) || vm.Status.EvacuationNodeName == node) {
			return true, fmt.Sprintf("VM %s/%s still runs on node %s", vm.Namespace, vm.Name, node)
		}
	}
	return false, ""
}

// Decision is the outcome of an eviction request.
type Decision struct {
	// Allow lets the eviction proceed.
	Allow bool
	// Evacuate asks for the VM to be marked for evacuation off the pod's node. It is only
	// set when Allow is false.
	Evacuate bool
	// Reason explains a refusal; it is returned to the eviction client.
	Reason string
}

// Decide returns how the eviction of vmPod, a pod controlled by vm, is handled. It is a
// pure function of API state. The same decision applies to a hotplug-volume pod owned by
// vmPod: its volumes back the running guest, so it must stay until the VM has moved.
func Decide(vm *v1beta1.VirtualMachine, vmPod *corev1.Pod, clusterDefault v1beta1.EvictionStrategy) Decision {
	strategy := EffectiveStrategy(vm, clusterDefault)
	if strategy == v1beta1.EvictionStrategyNone {
		return Decision{Allow: true}
	}

	// Only the VM's current pod hosts a guest worth migrating. The target pod of a
	// migration in flight can be evicted: the migration fails before cutover and the VM
	// stays on its source. A stale pod is garbage anyway.
	if vmPod.Name != vm.Status.VMPodName {
		return Decision{Allow: true}
	}
	if vmPod.Status.Phase == corev1.PodSucceeded || vmPod.Status.Phase == corev1.PodFailed {
		return Decision{Allow: true}
	}
	// A VM that is being deleted, halted or is not running has nothing to migrate; the
	// eviction stops it as before.
	if vm.DeletionTimestamp != nil || vm.Spec.RunPolicy == v1beta1.RunPolicyHalted || !vmshutdown.IsActivePhase(vm.Status.Phase) {
		return Decision{Allow: true}
	}

	// A migration is already moving the VM. Keep the evacuation mark so the evacuation
	// controller retries if that migration fails.
	if vm.Status.Migration != nil {
		return Decision{Evacuate: true, Reason: fmt.Sprintf("VM %s/%s is being live-migrated", vm.Namespace, vm.Name)}
	}

	cond := meta.FindStatusCondition(vm.Status.Conditions, string(v1beta1.VirtualMachineMigratable))
	if cond != nil && cond.Status == metav1.ConditionTrue {
		return Decision{Evacuate: true, Reason: fmt.Sprintf("eviction triggered live migration of VM %s/%s", vm.Namespace, vm.Name)}
	}

	if strategy == v1beta1.EvictionStrategyLiveMigrateIfPossible {
		return Decision{Allow: true}
	}
	msg := "migratable condition is unknown"
	if cond != nil && cond.Message != "" {
		msg = cond.Message
	}
	return Decision{Reason: fmt.Sprintf("VM %s/%s has evictionStrategy LiveMigrate but is not migratable: %s", vm.Namespace, vm.Name, msg)}
}
