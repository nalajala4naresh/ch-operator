package vmshutdown

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1beta1 "github.com/nalajala4naresh/chvmm-api/v1beta1"
)

func ptr[T any](v T) *T { return &v }

func TestGracePeriod(t *testing.T) {
	tests := []struct {
		name  string
		grace *int64
		want  time.Duration
	}{
		{"unset uses default", nil, time.Duration(v1beta1.DefaultGracePeriodSeconds) * time.Second},
		{"set", ptr[int64](120), 120 * time.Second},
		{"zero", ptr[int64](0), 0},
		{"negative uses default", ptr[int64](-1), time.Duration(v1beta1.DefaultGracePeriodSeconds) * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vm := &v1beta1.VirtualMachine{Spec: v1beta1.VirtualMachineSpec{TerminationGracePeriodSeconds: tt.grace}}
			if got := GracePeriod(vm); got != tt.want {
				t.Errorf("GracePeriod() = %v, want %v", got, tt.want)
			}
		})
	}

	vm := &v1beta1.VirtualMachine{Spec: v1beta1.VirtualMachineSpec{TerminationGracePeriodSeconds: ptr[int64](60)}}
	if got := PreStopWait(vm); got != 75*time.Second {
		t.Errorf("PreStopWait() = %v, want 75s", got)
	}
	if got := PodTerminationGracePeriod(vm); got != 90*time.Second {
		t.Errorf("PodTerminationGracePeriod() = %v, want 90s", got)
	}
}

func TestShutdownStart(t *testing.T) {
	requested := time.Unix(1000, 0)
	vmDeleted := time.Unix(2000, 0)

	terminatingPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		DeletionTimestamp:          &metav1.Time{Time: requested.Add(90 * time.Second)},
		DeletionGracePeriodSeconds: ptr[int64](90),
	}}
	deletingVM := &v1beta1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{DeletionTimestamp: &metav1.Time{Time: vmDeleted}}}

	tests := []struct {
		name   string
		vm     *v1beta1.VirtualMachine
		pod    *corev1.Pod
		want   time.Time
		wantOK bool
	}{
		{"nothing deleted", &v1beta1.VirtualMachine{}, &corev1.Pod{}, time.Time{}, false},
		{"pod deleted", &v1beta1.VirtualMachine{}, terminatingPod, requested, true},
		{"pod wins over VM", deletingVM, terminatingPod, requested, true},
		{"only VM deleted", deletingVM, &corev1.Pod{}, vmDeleted, true},
		{"nil pod", deletingVM, nil, vmDeleted, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ShutdownStart(tt.vm, tt.pod)
			if ok != tt.wantOK || !got.Equal(tt.want) {
				t.Errorf("ShutdownStart() = %v, %v; want %v, %v", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestDecide(t *testing.T) {
	start := time.Unix(1000, 0)
	grace := 30 * time.Second
	tests := []struct {
		name        string
		elapsed     time.Duration
		grace       time.Duration
		wantAction  Action
		wantRequeue time.Duration
	}{
		{"just started", 0, grace, PressPowerButton, PowerButtonInterval},
		{"mid grace", 10 * time.Second, grace, PressPowerButton, PowerButtonInterval},
		{"close to deadline", 28 * time.Second, grace, PressPowerButton, 2 * time.Second},
		{"deadline reached", 30 * time.Second, grace, ForceStop, 0},
		{"past deadline", time.Minute, grace, ForceStop, 0},
		{"zero grace", 0, 0, ForceStop, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			action, requeue := Decide(start, tt.grace, start.Add(tt.elapsed))
			if action != tt.wantAction || requeue != tt.wantRequeue {
				t.Errorf("Decide() = %v, %v; want %v, %v", action, requeue, tt.wantAction, tt.wantRequeue)
			}
		})
	}
}
