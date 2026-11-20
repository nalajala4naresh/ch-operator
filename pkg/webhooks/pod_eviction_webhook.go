package webhooks

import (
	"context"
	"net/http"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	v1beta1 "github.com/nalajala4naresh/chvmm-api/v1beta1"

	"github.com/nalajala4naresh/ch-operator/pkg/evacuation"
)

var podevictionlog = logf.Log.WithName("pod-eviction")

const podEvictionWebhookPath = "/validate-v1-pod-eviction"

// SetupPodEvictionWebhookWithManager registers the pods/eviction webhook, which turns
// evictions of VM pods into live migrations according to the VM's EvictionStrategy and
// keeps the node daemon (selected by daemonSelector) in place while VMs on its node need it.
func SetupPodEvictionWebhookWithManager(mgr ctrl.Manager, defaultStrategy v1beta1.EvictionStrategy, daemonSelector labels.Selector) {
	mgr.GetWebhookServer().Register(podEvictionWebhookPath, &webhook.Admission{
		Handler: &PodEvictionHandler{Client: mgr.GetClient(), DefaultStrategy: defaultStrategy, DaemonSelector: daemonSelector},
	})
}

// +kubebuilder:webhook:path=/validate-v1-pod-eviction,mutating=false,failurePolicy=fail,sideEffects=NoneOnDryRun,groups="",resources=pods/eviction,verbs=create,versions=v1,name=vpodeviction.kb.io,admissionReviewVersions=v1

// PodEvictionHandler admits or refuses evictions of VM pods and of the hotplug-volume pods
// that serve them. It refuses with 429 Too Many Requests, which eviction clients such as
// kubectl drain treat as "retry later", while the VM is being live-migrated away.
type PodEvictionHandler struct {
	Client          client.Client
	DefaultStrategy v1beta1.EvictionStrategy
	// DaemonSelector matches the node daemon's pods. A nil selector disables the
	// daemon guard.
	DaemonSelector labels.Selector
}

var _ admission.Handler = &PodEvictionHandler{}

func (h *PodEvictionHandler) Handle(ctx context.Context, req admission.Request) admission.Response {
	if req.Operation != admissionv1.Create {
		return admission.Allowed("")
	}

	var pod corev1.Pod
	if err := h.Client.Get(ctx, types.NamespacedName{Namespace: req.Namespace, Name: req.Name}, &pod); err != nil {
		if apierrors.IsNotFound(err) {
			return admission.Allowed("")
		}
		return admission.Errored(http.StatusInternalServerError, err)
	}

	if h.isDaemonPod(&pod) {
		return h.handleDaemonPod(ctx, &pod)
	}

	vmPod, vm, err := h.resolveVM(ctx, &pod)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}
	if vm == nil {
		return admission.Allowed("")
	}

	decision := evacuation.Decide(vm, vmPod, h.DefaultStrategy)
	if decision.Allow {
		return admission.Allowed("")
	}

	if decision.Evacuate && vm.Status.EvacuationNodeName == "" && vmPod.Spec.NodeName != "" &&
		(req.DryRun == nil || !*req.DryRun) {
		patch := client.MergeFrom(vm.DeepCopy())
		vm.Status.EvacuationNodeName = vmPod.Spec.NodeName
		if err := h.Client.Status().Patch(ctx, vm, patch); err != nil {
			return admission.Errored(http.StatusInternalServerError, err)
		}
		podevictionlog.Info("eviction triggered VM evacuation", "vm", client.ObjectKeyFromObject(vm), "pod", pod.Name, "node", vmPod.Spec.NodeName)
	}

	return tooManyRequests(decision.Reason)
}

// isDaemonPod reports whether pod is a node daemon pod: run by a DaemonSet and matching
// DaemonSelector.
func (h *PodEvictionHandler) isDaemonPod(pod *corev1.Pod) bool {
	if h.DaemonSelector == nil || h.DaemonSelector.Empty() {
		return false
	}
	owner := metav1.GetControllerOf(pod)
	return owner != nil && owner.Kind == "DaemonSet" && h.DaemonSelector.Matches(labels.Set(pod.Labels))
}

// handleDaemonPod refuses to evict the node daemon while a VM on its node depends on it:
// the daemon carries live-migration traffic and drives graceful guest shutdown, so
// evicting it mid-migration would kill the transfer. The DaemonSet would recreate it on
// the cordoned node anyway, so refusing costs the eviction client nothing but a retry.
func (h *PodEvictionHandler) handleDaemonPod(ctx context.Context, pod *corev1.Pod) admission.Response {
	node := pod.Spec.NodeName
	if node == "" || pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
		return admission.Allowed("")
	}
	var vms v1beta1.VirtualMachineList
	if err := h.Client.List(ctx, &vms); err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}
	if inUse, why := evacuation.NodeInUse(vms.Items, node); inUse {
		return tooManyRequests("node daemon is still needed: " + why)
	}
	return admission.Allowed("")
}

// resolveVM returns the VM pod and VM behind pod: pod itself when a VM controls it, or the
// VM pod that controls pod when pod is a hotplug-volume pod. It returns a nil VM for any
// other pod.
func (h *PodEvictionHandler) resolveVM(ctx context.Context, pod *corev1.Pod) (*corev1.Pod, *v1beta1.VirtualMachine, error) {
	vmPod := pod
	owner := metav1.GetControllerOf(pod)
	if owner != nil && owner.APIVersion == "v1" && owner.Kind == "Pod" {
		var parent corev1.Pod
		if err := h.Client.Get(ctx, types.NamespacedName{Namespace: pod.Namespace, Name: owner.Name}, &parent); err != nil {
			return nil, nil, client.IgnoreNotFound(err)
		}
		if parent.UID != owner.UID {
			return nil, nil, nil
		}
		vmPod = &parent
		owner = metav1.GetControllerOf(vmPod)
	}
	if owner == nil || owner.APIVersion != v1beta1.GroupVersion.String() || owner.Kind != "VirtualMachine" {
		return nil, nil, nil
	}

	var vm v1beta1.VirtualMachine
	if err := h.Client.Get(ctx, types.NamespacedName{Namespace: vmPod.Namespace, Name: owner.Name}, &vm); err != nil {
		return nil, nil, client.IgnoreNotFound(err)
	}
	if vm.UID != owner.UID {
		return nil, nil, nil
	}
	return vmPod, &vm, nil
}

func tooManyRequests(msg string) admission.Response {
	return admission.Response{AdmissionResponse: admissionv1.AdmissionResponse{
		Allowed: false,
		Result: &metav1.Status{
			Status:  metav1.StatusFailure,
			Code:    http.StatusTooManyRequests,
			Reason:  metav1.StatusReasonTooManyRequests,
			Message: msg,
		},
	}}
}
