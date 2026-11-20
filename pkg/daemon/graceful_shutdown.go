package daemon

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/nalajala4naresh/ch-operator/pkg/cloudhypervisor"
	"github.com/nalajala4naresh/ch-operator/pkg/vmshutdown"
	v1beta1 "github.com/nalajala4naresh/chvmm-api/v1beta1"
)

// vmStopper is the part of the Cloud Hypervisor client used to stop a VM.
type vmStopper interface {
	VmInfo(ctx context.Context) (*cloudhypervisor.VmInfo, error)
	VmPowerButton(ctx context.Context) error
	VmShutdown(ctx context.Context) error
}

// requeueAfter is returned by reconcile steps that are waiting on something and need to be
// checked again sooner than the daemon's regular resync. It is not reported as an error.
type requeueAfter time.Duration

func (r requeueAfter) Error() string { return fmt.Sprintf("requeue after %s", time.Duration(r)) }

// shutdownSignals remembers which stop requests already got a "shutting down" event, so the
// event is emitted once rather than on every power-button press. Losing it on a daemon
// restart only means the event is emitted again.
type shutdownSignals struct {
	mu   sync.Mutex
	sent map[string]struct{}
}

func (s *shutdownSignals) markSent(key string) (first bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sent == nil {
		s.sent = map[string]struct{}{}
	}
	if _, ok := s.sent[key]; ok {
		return false
	}
	s.sent[key] = struct{}{}
	return true
}

func (s *shutdownSignals) forget(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sent, key)
}

func (r *VMReconciler) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

// stopGracefully drives the shutdown of a VM that has been asked to stop: its pod or the
// VirtualMachine itself is being deleted. Like KubeVirt's virt-handler, the daemon is the only
// component that acts on the guest. It presses the ACPI power button every few seconds until
// the guest powers off, and forcibly stops the VM once the grace period, counted from the stop
// request, has passed.
//
// It returns true once the VM is no longer running. While the guest is shutting down it
// returns a requeueAfter error so the deadline is checked on time.
func (r *VMReconciler) stopGracefully(ctx context.Context, vm *v1beta1.VirtualMachine, pod *corev1.Pod, ch vmStopper) (bool, error) {
	start, ok := vmshutdown.ShutdownStart(vm, pod)
	if !ok {
		return false, fmt.Errorf("stop requested for VM %q, but neither the VM nor its pod is being deleted", vm.Name)
	}
	signalKey := string(vm.UID) + "/" + string(pod.UID)

	info, err := ch.VmInfo(ctx)
	if err != nil {
		if isChVMNotCreatedError(err) || isChUnreachableError(err) {
			r.shutdownSignals.forget(signalKey)
			return true, nil
		}
		return false, fmt.Errorf("get VM info: %s", err)
	}
	if info.State != "Running" && info.State != "Paused" {
		r.shutdownSignals.forget(signalKey)
		return true, nil
	}

	grace := vmshutdown.GracePeriod(vm)
	action, requeue := vmshutdown.Decide(start, grace, r.clock())
	switch action {
	case vmshutdown.ForceStop:
		if err := ch.VmShutdown(ctx); err != nil {
			r.Recorder.Eventf(vm, corev1.EventTypeWarning, "FailedVMShutdown", "Failed to stop VM: %s", err)
			return false, fmt.Errorf("force VM shutdown: %s", err)
		}
		if grace == 0 {
			r.Recorder.Eventf(vm, corev1.EventTypeNormal, "VMStopped", "Graceful shutdown disabled, VM stopped")
		} else {
			r.Recorder.Eventf(vm, corev1.EventTypeNormal, "VMStopped", "Grace period of %s expired, VM forcibly stopped", grace)
		}
		r.shutdownSignals.forget(signalKey)
		return true, nil

	default: // vmshutdown.PressPowerButton
		if err := ch.VmPowerButton(ctx); err != nil {
			// Keep going: the next press may succeed, and the deadline still forces the stop.
			ctrl.LoggerFrom(ctx).Error(err, "failed to press VM power button", "vm", vm.Name)
		} else if r.shutdownSignals.markSent(signalKey) {
			r.Recorder.Eventf(vm, corev1.EventTypeNormal, "ShuttingDown",
				"Signaled graceful shutdown, VM will be forcibly stopped after %s", grace)
		}
		return false, requeueAfter(requeue)
	}
}

// isChUnreachableError reports whether err means the Cloud Hypervisor process is gone,
// which happens when it exits after the guest powers off.
func isChUnreachableError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "connection refused") || strings.Contains(msg, "no such file or directory")
}
