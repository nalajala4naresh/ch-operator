package evacuation

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1beta1 "github.com/nalajala4naresh/chvmm-api/v1beta1"
)

func strategy(s v1beta1.EvictionStrategy) *v1beta1.EvictionStrategy { return &s }

func newVM(s *v1beta1.EvictionStrategy, migratable metav1.ConditionStatus) *v1beta1.VirtualMachine {
	vm := &v1beta1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{Name: "vm", Namespace: "ns"},
		Spec:       v1beta1.VirtualMachineSpec{EvictionStrategy: s, RunPolicy: v1beta1.RunAlways},
		Status: v1beta1.VirtualMachineStatus{
			Phase:     v1beta1.VirtualMachineRunning,
			VMPodName: "vm-pod",
			NodeName:  "node-a",
		},
	}
	if migratable != "" {
		vm.Status.Conditions = []metav1.Condition{{
			Type: string(v1beta1.VirtualMachineMigratable), Status: migratable, Message: "dedicated CPU",
		}}
	}
	return vm
}

func newPod(name string, phase corev1.PodPhase) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns"},
		Spec:       corev1.PodSpec{NodeName: "node-a"},
		Status:     corev1.PodStatus{Phase: phase},
	}
}

func TestDecide(t *testing.T) {
	now := metav1.Now()
	tests := []struct {
		name           string
		vm             func() *v1beta1.VirtualMachine
		pod            *corev1.Pod
		clusterDefault v1beta1.EvictionStrategy
		want           Decision
	}{{
		name: "unset strategy defaults to None",
		vm:   func() *v1beta1.VirtualMachine { return newVM(nil, metav1.ConditionTrue) },
		pod:  newPod("vm-pod", corev1.PodRunning),
		want: Decision{Allow: true},
	}, {
		name: "explicit None",
		vm: func() *v1beta1.VirtualMachine {
			return newVM(strategy(v1beta1.EvictionStrategyNone), metav1.ConditionTrue)
		},
		clusterDefault: v1beta1.EvictionStrategyLiveMigrate,
		pod:            newPod("vm-pod", corev1.PodRunning),
		want:           Decision{Allow: true},
	}, {
		name:           "cluster default LiveMigrate applies to unset strategy",
		vm:             func() *v1beta1.VirtualMachine { return newVM(nil, metav1.ConditionTrue) },
		clusterDefault: v1beta1.EvictionStrategyLiveMigrate,
		pod:            newPod("vm-pod", corev1.PodRunning),
		want:           Decision{Evacuate: true},
	}, {
		name: "LiveMigrate and migratable evacuates",
		vm: func() *v1beta1.VirtualMachine {
			return newVM(strategy(v1beta1.EvictionStrategyLiveMigrate), metav1.ConditionTrue)
		},
		pod:  newPod("vm-pod", corev1.PodRunning),
		want: Decision{Evacuate: true},
	}, {
		name: "LiveMigrate and not migratable blocks",
		vm: func() *v1beta1.VirtualMachine {
			return newVM(strategy(v1beta1.EvictionStrategyLiveMigrate), metav1.ConditionFalse)
		},
		pod:  newPod("vm-pod", corev1.PodRunning),
		want: Decision{},
	}, {
		name: "LiveMigrate and migratable unknown blocks",
		vm: func() *v1beta1.VirtualMachine {
			return newVM(strategy(v1beta1.EvictionStrategyLiveMigrate), "")
		},
		pod:  newPod("vm-pod", corev1.PodRunning),
		want: Decision{},
	}, {
		name: "LiveMigrateIfPossible and migratable evacuates",
		vm: func() *v1beta1.VirtualMachine {
			return newVM(strategy(v1beta1.EvictionStrategyLiveMigrateIfPossible), metav1.ConditionTrue)
		},
		pod:  newPod("vm-pod", corev1.PodRunning),
		want: Decision{Evacuate: true},
	}, {
		name: "LiveMigrateIfPossible and not migratable shuts down",
		vm: func() *v1beta1.VirtualMachine {
			return newVM(strategy(v1beta1.EvictionStrategyLiveMigrateIfPossible), metav1.ConditionFalse)
		},
		pod:  newPod("vm-pod", corev1.PodRunning),
		want: Decision{Allow: true},
	}, {
		name: "migration target pod is evictable",
		vm: func() *v1beta1.VirtualMachine {
			vm := newVM(strategy(v1beta1.EvictionStrategyLiveMigrate), metav1.ConditionTrue)
			vm.Status.Migration = &v1beta1.VirtualMachineStatusMigration{TargetVMPodName: "target-pod"}
			return vm
		},
		pod:  newPod("target-pod", corev1.PodRunning),
		want: Decision{Allow: true},
	}, {
		name: "source pod during migration stays and keeps evacuation mark",
		vm: func() *v1beta1.VirtualMachine {
			vm := newVM(strategy(v1beta1.EvictionStrategyLiveMigrate), metav1.ConditionFalse)
			vm.Status.Migration = &v1beta1.VirtualMachineStatusMigration{TargetVMPodName: "target-pod"}
			return vm
		},
		pod:  newPod("vm-pod", corev1.PodRunning),
		want: Decision{Evacuate: true},
	}, {
		name: "completed source pod is evictable",
		vm: func() *v1beta1.VirtualMachine {
			return newVM(strategy(v1beta1.EvictionStrategyLiveMigrate), metav1.ConditionTrue)
		},
		pod:  newPod("vm-pod", corev1.PodSucceeded),
		want: Decision{Allow: true},
	}, {
		name: "VM not running is evictable",
		vm: func() *v1beta1.VirtualMachine {
			vm := newVM(strategy(v1beta1.EvictionStrategyLiveMigrate), metav1.ConditionTrue)
			vm.Status.Phase = v1beta1.VirtualMachineScheduled
			return vm
		},
		pod:  newPod("vm-pod", corev1.PodRunning),
		want: Decision{Allow: true},
	}, {
		name: "VM being deleted is evictable",
		vm: func() *v1beta1.VirtualMachine {
			vm := newVM(strategy(v1beta1.EvictionStrategyLiveMigrate), metav1.ConditionTrue)
			vm.DeletionTimestamp = &now
			return vm
		},
		pod:  newPod("vm-pod", corev1.PodRunning),
		want: Decision{Allow: true},
	}, {
		name: "halted VM is evictable",
		vm: func() *v1beta1.VirtualMachine {
			vm := newVM(strategy(v1beta1.EvictionStrategyLiveMigrate), metav1.ConditionTrue)
			vm.Spec.RunPolicy = v1beta1.RunPolicyHalted
			return vm
		},
		pod:  newPod("vm-pod", corev1.PodRunning),
		want: Decision{Allow: true},
	}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Decide(tc.vm(), tc.pod, tc.clusterDefault)
			if got.Allow != tc.want.Allow || got.Evacuate != tc.want.Evacuate {
				t.Fatalf("Decide() = %+v, want Allow=%v Evacuate=%v", got, tc.want.Allow, tc.want.Evacuate)
			}
			if !got.Allow && got.Reason == "" {
				t.Fatalf("refusal without a reason")
			}
		})
	}
}

func TestParseStrategy(t *testing.T) {
	for _, s := range []string{"None", "LiveMigrate", "LiveMigrateIfPossible"} {
		if _, err := ParseStrategy(s); err != nil {
			t.Errorf("ParseStrategy(%q) error: %v", s, err)
		}
	}
	if _, err := ParseStrategy("livemigrate"); err == nil {
		t.Errorf("ParseStrategy accepted an unknown strategy")
	}
}

func TestNodeInUse(t *testing.T) {
	running := *newVM(nil, metav1.ConditionTrue) // Running on node-a
	stopped := *newVM(nil, "")
	stopped.Status.Phase = v1beta1.VirtualMachineSucceeded
	incoming := *newVM(nil, "")
	incoming.Status.NodeName = "node-b"
	incoming.Status.Migration = &v1beta1.VirtualMachineStatusMigration{TargetNodeName: "node-a"}

	tests := []struct {
		name string
		vms  []v1beta1.VirtualMachine
		want bool
	}{
		{"no VMs", nil, false},
		{"running VM on node", []v1beta1.VirtualMachine{running}, true},
		{"only stopped VM on node", []v1beta1.VirtualMachine{stopped}, false},
		{"VM migrating onto node", []v1beta1.VirtualMachine{incoming}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, why := NodeInUse(tc.vms, "node-a")
			if got != tc.want {
				t.Fatalf("NodeInUse() = %v (%s), want %v", got, why, tc.want)
			}
			if got && why == "" {
				t.Fatalf("in use without a reason")
			}
		})
	}
	if got, _ := NodeInUse([]v1beta1.VirtualMachine{running}, "node-b"); got {
		t.Fatalf("VM on node-a reported in use on node-b")
	}
}
