// Package vmshutdown holds the graceful-shutdown timing shared by the controller and the
// node daemon. It follows KubeVirt's model: every stop is expressed as deletion of the VM
// pod, the node daemon alone drives the guest shutdown, and the shutdown start time is
// derived from durable API state rather than kept in memory.
package vmshutdown

import (
	"time"

	corev1 "k8s.io/api/core/v1"

	v1beta1 "github.com/nalajala4naresh/chvmm-api/v1beta1"
)

const (
	// GracePeriodPadding is added to the VM grace period at each layer, as KubeVirt does:
	// the pod's preStop hook waits grace+padding, and the pod's
	// terminationGracePeriodSeconds is grace+2*padding, so each layer outlives the one
	// below it and the daemon's forced stop always fires before the kubelet's SIGKILL.
	GracePeriodPadding = 15 * time.Second

	// PowerButtonInterval is how often the ACPI power button is pressed again while
	// waiting. A booting guest can miss the first press.
	PowerButtonInterval = 5 * time.Second
)

// GracePeriod returns the VM's guest shutdown grace period.
func GracePeriod(vm *v1beta1.VirtualMachine) time.Duration {
	secs := v1beta1.DefaultGracePeriodSeconds
	if vm.Spec.TerminationGracePeriodSeconds != nil && *vm.Spec.TerminationGracePeriodSeconds >= 0 {
		secs = *vm.Spec.TerminationGracePeriodSeconds
	}
	return time.Duration(secs) * time.Second
}

// PreStopWait is how long the VM pod's preStop hook waits for the VM to stop.
func PreStopWait(vm *v1beta1.VirtualMachine) time.Duration {
	return GracePeriod(vm) + GracePeriodPadding
}

// PodTerminationGracePeriod is the VM pod's terminationGracePeriodSeconds.
func PodTerminationGracePeriod(vm *v1beta1.VirtualMachine) time.Duration {
	return GracePeriod(vm) + 2*GracePeriodPadding
}

// ShutdownStart returns when the VM was asked to stop, and false if it hasn't been.
//
// The VM pod's deletion is the stop signal. Its deletionTimestamp is the time the kubelet
// will kill it, so the request time is that minus deletionGracePeriodSeconds. If the pod is
// not being deleted yet but the VirtualMachine is, the VM's deletionTimestamp is used; for
// custom resources that is the time of the delete request. Both values are stored by the
// API server, so the deadline survives daemon restarts.
func ShutdownStart(vm *v1beta1.VirtualMachine, pod *corev1.Pod) (time.Time, bool) {
	if pod != nil && pod.DeletionTimestamp != nil {
		start := pod.DeletionTimestamp.Time
		if pod.DeletionGracePeriodSeconds != nil {
			start = start.Add(-time.Duration(*pod.DeletionGracePeriodSeconds) * time.Second)
		}
		return start, true
	}
	if vm.DeletionTimestamp != nil {
		return vm.DeletionTimestamp.Time, true
	}
	return time.Time{}, false
}

// Action is the next step of a graceful shutdown.
type Action int

const (
	// PressPowerButton asks the guest to shut down via ACPI.
	PressPowerButton Action = iota
	// ForceStop stops the VM without guest cooperation.
	ForceStop
)

// Decide returns the next shutdown action for a VM that is still running, and when to check
// again. It is a pure function of the start time, the grace period and the current time.
func Decide(start time.Time, grace time.Duration, now time.Time) (Action, time.Duration) {
	remaining := grace - now.Sub(start)
	if remaining <= 0 {
		return ForceStop, 0
	}
	if remaining < PowerButtonInterval {
		return PressPowerButton, remaining
	}
	return PressPowerButton, PowerButtonInterval
}

// IsActivePhase reports whether a VM in this phase has a running Cloud Hypervisor VM.
func IsActivePhase(phase v1beta1.VirtualMachinePhase) bool {
	switch phase {
	case v1beta1.VirtualMachineRunning,
		v1beta1.VirtualMachinePaused,
		v1beta1.VirtualMachineResumed,
		v1beta1.VirtualMachineResizeInProgress,
		v1beta1.VirtualMachineSnapShotInprogress:
		return true
	}
	return false
}
