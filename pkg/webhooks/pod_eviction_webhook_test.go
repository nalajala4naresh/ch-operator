package webhooks

import (
	"context"
	"net/http"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	v1beta1 "github.com/nalajala4naresh/chvmm-api/v1beta1"
)

func evictionTestObjects(strategy v1beta1.EvictionStrategy) (*v1beta1.VirtualMachine, *corev1.Pod, *corev1.Pod, *corev1.Pod) {
	vm := &v1beta1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{Name: "vm", Namespace: "ns", UID: "vm-uid"},
		Spec:       v1beta1.VirtualMachineSpec{EvictionStrategy: &strategy},
		Status: v1beta1.VirtualMachineStatus{
			Phase:     v1beta1.VirtualMachineRunning,
			VMPodName: "vm-pod",
			NodeName:  "node-a",
			Conditions: []metav1.Condition{{
				Type: string(v1beta1.VirtualMachineMigratable), Status: metav1.ConditionTrue,
				Reason: "Migratable", LastTransitionTime: metav1.Now(),
			}},
		},
	}
	vmPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "vm-pod", Namespace: "ns", UID: "pod-uid",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: v1beta1.GroupVersion.String(), Kind: "VirtualMachine",
				Name: "vm", UID: "vm-uid", Controller: ptr.To(true),
			}},
		},
		Spec:   corev1.PodSpec{NodeName: "node-a"},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	hotplugPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "vm-hotplug-volumes-x", Namespace: "ns",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "v1", Kind: "Pod", Name: "vm-pod", UID: "pod-uid", Controller: ptr.To(true),
			}},
		},
		Spec:   corev1.PodSpec{NodeName: "node-a"},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	otherPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx", Namespace: "ns"},
		Spec:       corev1.PodSpec{NodeName: "node-a"},
	}
	return vm, vmPod, hotplugPod, otherPod
}

func newEvictionHandler(t *testing.T, objs ...client.Object) (*PodEvictionHandler, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1beta1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1beta1.VirtualMachine{}).
		WithObjects(objs...).Build()
	return &PodEvictionHandler{Client: c}, c
}

func evictionRequest(pod string, dryRun bool) admission.Request {
	return admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		Operation: admissionv1.Create,
		Namespace: "ns",
		Name:      pod,
		DryRun:    &dryRun,
	}}
}

func evacuationNode(t *testing.T, c client.Client) string {
	t.Helper()
	var vm v1beta1.VirtualMachine
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: "vm"}, &vm); err != nil {
		t.Fatal(err)
	}
	return vm.Status.EvacuationNodeName
}

func TestPodEvictionHandler(t *testing.T) {
	t.Run("non-VM pod is allowed", func(t *testing.T) {
		vm, vmPod, hp, other := evictionTestObjects(v1beta1.EvictionStrategyLiveMigrate)
		h, _ := newEvictionHandler(t, vm, vmPod, hp, other)
		if resp := h.Handle(context.Background(), evictionRequest("nginx", false)); !resp.Allowed {
			t.Fatalf("expected allowed, got %+v", resp.Result)
		}
	})

	t.Run("missing pod is allowed", func(t *testing.T) {
		h, _ := newEvictionHandler(t)
		if resp := h.Handle(context.Background(), evictionRequest("gone", false)); !resp.Allowed {
			t.Fatalf("expected allowed, got %+v", resp.Result)
		}
	})

	t.Run("None strategy is allowed", func(t *testing.T) {
		vm, vmPod, hp, other := evictionTestObjects(v1beta1.EvictionStrategyNone)
		h, c := newEvictionHandler(t, vm, vmPod, hp, other)
		if resp := h.Handle(context.Background(), evictionRequest("vm-pod", false)); !resp.Allowed {
			t.Fatalf("expected allowed, got %+v", resp.Result)
		}
		if n := evacuationNode(t, c); n != "" {
			t.Fatalf("evacuation marked on node %q", n)
		}
	})

	t.Run("LiveMigrate VM pod is refused with 429 and marked", func(t *testing.T) {
		vm, vmPod, hp, other := evictionTestObjects(v1beta1.EvictionStrategyLiveMigrate)
		h, c := newEvictionHandler(t, vm, vmPod, hp, other)
		resp := h.Handle(context.Background(), evictionRequest("vm-pod", false))
		if resp.Allowed || resp.Result == nil || resp.Result.Code != http.StatusTooManyRequests {
			t.Fatalf("expected 429 refusal, got allowed=%v result=%+v", resp.Allowed, resp.Result)
		}
		if n := evacuationNode(t, c); n != "node-a" {
			t.Fatalf("evacuationNodeName = %q, want node-a", n)
		}
	})

	t.Run("dry run refuses without marking", func(t *testing.T) {
		vm, vmPod, hp, other := evictionTestObjects(v1beta1.EvictionStrategyLiveMigrate)
		h, c := newEvictionHandler(t, vm, vmPod, hp, other)
		if resp := h.Handle(context.Background(), evictionRequest("vm-pod", true)); resp.Allowed {
			t.Fatalf("expected refusal")
		}
		if n := evacuationNode(t, c); n != "" {
			t.Fatalf("dry run marked evacuation on node %q", n)
		}
	})

	t.Run("hotplug volume pod follows its VM pod", func(t *testing.T) {
		vm, vmPod, hp, other := evictionTestObjects(v1beta1.EvictionStrategyLiveMigrate)
		h, c := newEvictionHandler(t, vm, vmPod, hp, other)
		resp := h.Handle(context.Background(), evictionRequest("vm-hotplug-volumes-x", false))
		if resp.Allowed || resp.Result.Code != http.StatusTooManyRequests {
			t.Fatalf("expected 429 refusal, got allowed=%v result=%+v", resp.Allowed, resp.Result)
		}
		if n := evacuationNode(t, c); n != "node-a" {
			t.Fatalf("evacuationNodeName = %q, want node-a", n)
		}
	})

	t.Run("VM pod is allowed once the VM has moved", func(t *testing.T) {
		vm, vmPod, hp, other := evictionTestObjects(v1beta1.EvictionStrategyLiveMigrate)
		vm.Status.VMPodName = "vm-pod-on-node-b"
		vm.Status.NodeName = "node-b"
		h, _ := newEvictionHandler(t, vm, vmPod, hp, other)
		if resp := h.Handle(context.Background(), evictionRequest("vm-pod", false)); !resp.Allowed {
			t.Fatalf("expected allowed, got %+v", resp.Result)
		}
	})

	daemonPod := func() *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "ch-vmm-daemon-abc", Namespace: "ns", Labels: map[string]string{"name": "ch-vmm-daemon"},
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "apps/v1", Kind: "DaemonSet", Name: "ch-vmm-daemon", UID: "ds-uid", Controller: ptr.To(true),
				}},
			},
			Spec:   corev1.PodSpec{NodeName: "node-a"},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		}
	}
	selector := labels.SelectorFromSet(labels.Set{"name": "ch-vmm-daemon"})

	t.Run("daemon pod is refused while a VM runs on its node", func(t *testing.T) {
		// Strategy None: the daemon still drives that VM's graceful shutdown.
		vm, vmPod, hp, other := evictionTestObjects(v1beta1.EvictionStrategyNone)
		h, _ := newEvictionHandler(t, vm, vmPod, hp, other, daemonPod())
		h.DaemonSelector = selector
		resp := h.Handle(context.Background(), evictionRequest("ch-vmm-daemon-abc", false))
		if resp.Allowed || resp.Result.Code != http.StatusTooManyRequests {
			t.Fatalf("expected 429 refusal, got allowed=%v result=%+v", resp.Allowed, resp.Result)
		}
	})

	t.Run("daemon pod is refused while a VM migrates onto its node", func(t *testing.T) {
		vm, vmPod, hp, other := evictionTestObjects(v1beta1.EvictionStrategyLiveMigrate)
		vm.Status.NodeName = "node-b"
		vm.Status.Migration = &v1beta1.VirtualMachineStatusMigration{TargetNodeName: "node-a"}
		h, _ := newEvictionHandler(t, vm, vmPod, hp, other, daemonPod())
		h.DaemonSelector = selector
		if resp := h.Handle(context.Background(), evictionRequest("ch-vmm-daemon-abc", false)); resp.Allowed {
			t.Fatalf("expected refusal")
		}
	})

	t.Run("daemon pod is allowed once its node has no VMs", func(t *testing.T) {
		vm, vmPod, hp, other := evictionTestObjects(v1beta1.EvictionStrategyLiveMigrate)
		vm.Status.NodeName = "node-b"
		vm.Status.VMPodName = "vm-pod-on-node-b"
		h, _ := newEvictionHandler(t, vm, vmPod, hp, other, daemonPod())
		h.DaemonSelector = selector
		if resp := h.Handle(context.Background(), evictionRequest("ch-vmm-daemon-abc", false)); !resp.Allowed {
			t.Fatalf("expected allowed, got %+v", resp.Result)
		}
	})
}
