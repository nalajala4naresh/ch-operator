package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"

	"github.com/nalajala4naresh/ch-operator/pkg/cloudhypervisor"
	v1beta1 "github.com/nalajala4naresh/chvmm-api/v1beta1"
)

type fakeCH struct {
	state       cloudhypervisor.VmState
	infoErr     error
	shutdownErr error
	buttons     int
	shutdowns   int
}

func (f *fakeCH) VmInfo(context.Context) (*cloudhypervisor.VmInfo, error) {
	if f.infoErr != nil {
		return nil, f.infoErr
	}
	return &cloudhypervisor.VmInfo{State: f.state}, nil
}
func (f *fakeCH) VmPowerButton(context.Context) error { f.buttons++; return nil }
func (f *fakeCH) VmShutdown(context.Context) error {
	f.shutdowns++
	if f.shutdownErr == nil {
		f.state = "Shutdown"
	}
	return f.shutdownErr
}

func int64p(v int64) *int64 { return &v }

// terminating returns a VM with the given grace period and a pod whose deletion was
// requested at requested.
func terminating(grace int64, requested time.Time) (*v1beta1.VirtualMachine, *corev1.Pod) {
	vm := &v1beta1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{Name: "vm", UID: "vm-uid"},
		Spec:       v1beta1.VirtualMachineSpec{TerminationGracePeriodSeconds: int64p(grace)},
	}
	podGrace := grace + 30
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		UID:                        "pod-uid",
		DeletionTimestamp:          &metav1.Time{Time: requested.Add(time.Duration(podGrace) * time.Second)},
		DeletionGracePeriodSeconds: &podGrace,
	}}
	return vm, pod
}

func newTestReconciler(now *time.Time) (*VMReconciler, *events.FakeRecorder) {
	rec := events.NewFakeRecorder(10)
	return &VMReconciler{Recorder: rec, now: func() time.Time { return *now }}, rec
}

func drain(rec *events.FakeRecorder) []string {
	var events []string
	for {
		select {
		case e := <-rec.Events:
			events = append(events, e)
		default:
			return events
		}
	}
}

func TestStopGracefullyPressesUntilDeadlineThenForces(t *testing.T) {
	ctx := context.Background()
	requested := time.Unix(1000, 0)
	now := requested
	r, rec := newTestReconciler(&now)
	vm, pod := terminating(30, requested)
	ch := &fakeCH{state: "Running"}

	for _, elapsed := range []time.Duration{0, 5 * time.Second, 10 * time.Second, 28 * time.Second} {
		now = requested.Add(elapsed)
		stopped, err := r.stopGracefully(ctx, vm, pod, ch)
		var rq requeueAfter
		if stopped || !errors.As(err, &rq) {
			t.Fatalf("at %s: stopped=%v err=%v; want waiting with requeue", elapsed, stopped, err)
		}
		if elapsed == 28*time.Second && time.Duration(rq) != 2*time.Second {
			t.Fatalf("requeue near deadline = %s, want 2s", time.Duration(rq))
		}
	}
	if ch.buttons != 4 || ch.shutdowns != 0 {
		t.Fatalf("buttons=%d shutdowns=%d; want 4, 0", ch.buttons, ch.shutdowns)
	}
	if events := drain(rec); len(events) != 1 || !strings.Contains(events[0], "ShuttingDown") {
		t.Fatalf("events = %q; want a single ShuttingDown", events)
	}

	now = requested.Add(30 * time.Second)
	if stopped, err := r.stopGracefully(ctx, vm, pod, ch); !stopped || err != nil {
		t.Fatalf("at deadline: stopped=%v err=%v; want forced stop", stopped, err)
	}
	if ch.shutdowns != 1 {
		t.Fatalf("shutdowns=%d; want 1", ch.shutdowns)
	}
	if events := drain(rec); len(events) != 1 || !strings.Contains(events[0], "VMStopped") {
		t.Fatalf("events = %q; want VMStopped", events)
	}
}

func TestStopGracefullyRecordsGracefulShutdownOnce(t *testing.T) {
	ctx := context.Background()
	requested := time.Unix(1000, 0)
	now := requested
	r, rec := newTestReconciler(&now)
	vm, pod := terminating(60, requested)
	ch := &fakeCH{state: "Running"}

	if _, err := r.stopGracefully(ctx, vm, pod, ch); err == nil {
		t.Fatal("want requeue after first press")
	}
	drain(rec)

	// The guest powers off 12s after the stop request.
	now = requested.Add(12 * time.Second)
	ch.state = "Shutdown"
	if stopped, err := r.stopGracefully(ctx, vm, pod, ch); !stopped || err != nil {
		t.Fatalf("stopped=%v err=%v; want stopped", stopped, err)
	}
	events := drain(rec)
	if len(events) != 1 || !strings.Contains(events[0], "VMShutdown") || !strings.Contains(events[0], "after 12s") {
		t.Fatalf("events = %q; want one VMShutdown after 12s", events)
	}

	// Later reconciles see the VM still stopped and stay quiet.
	now = requested.Add(30 * time.Second)
	if stopped, _ := r.stopGracefully(ctx, vm, pod, ch); !stopped {
		t.Fatal("want stopped")
	}
	if events := drain(rec); len(events) != 0 {
		t.Fatalf("events = %q; want none on repeat checks", events)
	}
}

func TestStopGracefullyDeadlineSurvivesRestart(t *testing.T) {
	// The start comes from the pod's deletion timestamp, so a fresh reconciler (a restarted
	// daemon) that first sees the pod after the deadline stops the VM immediately.
	requested := time.Unix(1000, 0)
	now := requested.Add(time.Minute)
	r, _ := newTestReconciler(&now)
	vm, pod := terminating(30, requested)
	ch := &fakeCH{state: "Running"}

	if stopped, err := r.stopGracefully(context.Background(), vm, pod, ch); !stopped || err != nil {
		t.Fatalf("stopped=%v err=%v; want forced stop", stopped, err)
	}
	if ch.buttons != 0 || ch.shutdowns != 1 {
		t.Fatalf("buttons=%d shutdowns=%d; want 0, 1", ch.buttons, ch.shutdowns)
	}
}

func TestStopGracefullyAlreadyStopped(t *testing.T) {
	requested := time.Unix(1000, 0)
	now := requested
	tests := []struct {
		name string
		ch   *fakeCH
	}{
		{"guest powered off", &fakeCH{state: "Shutdown"}},
		{"VM never created", &fakeCH{infoErr: errors.New("request failed: 500: VM is not created")}},
		{"VMM exited", &fakeCH{infoErr: errors.New("do request: dial unix /x/vm.sock: connect: no such file or directory")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, rec := newTestReconciler(&now)
			vm, pod := terminating(30, requested)
			if stopped, err := r.stopGracefully(context.Background(), vm, pod, tt.ch); !stopped || err != nil {
				t.Fatalf("stopped=%v err=%v; want stopped", stopped, err)
			}
			if tt.ch.buttons != 0 || tt.ch.shutdowns != 0 {
				t.Fatalf("buttons=%d shutdowns=%d; want no action", tt.ch.buttons, tt.ch.shutdowns)
			}
			// This daemon never signaled the shutdown, so there's nothing to report.
			if events := drain(rec); len(events) != 0 {
				t.Fatalf("events = %q; want none", events)
			}
		})
	}
}

func TestStopGracefullyZeroGraceAndErrors(t *testing.T) {
	ctx := context.Background()
	requested := time.Unix(1000, 0)
	now := requested

	r, _ := newTestReconciler(&now)
	vm, pod := terminating(0, requested)
	ch := &fakeCH{state: "Paused"}
	if stopped, err := r.stopGracefully(ctx, vm, pod, ch); !stopped || err != nil || ch.buttons != 0 {
		t.Fatalf("zero grace: stopped=%v err=%v buttons=%d; want immediate stop", stopped, err, ch.buttons)
	}

	vm, pod = terminating(0, requested)
	ch = &fakeCH{state: "Running", shutdownErr: errors.New("busy")}
	if stopped, err := r.stopGracefully(ctx, vm, pod, ch); stopped || err == nil {
		t.Fatalf("failed force stop: stopped=%v err=%v; want error", stopped, err)
	}

	ch = &fakeCH{infoErr: errors.New("request failed: 500: something else")}
	if stopped, err := r.stopGracefully(ctx, vm, pod, ch); stopped || err == nil {
		t.Fatalf("unknown info error: stopped=%v err=%v; want error", stopped, err)
	}
}
