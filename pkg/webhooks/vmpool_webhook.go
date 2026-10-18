package webhooks

import (
	"context"
	"reflect"

	types "github.com/nalajala4naresh/chvmm-api/v1beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// nolint:unused
// log is for logging in this package.
var vmpoollog = logf.Log.WithName("vmpool-resource")

// SetupVMPoolWebhookWithManager registers the webhook for VMPool in the manager.
func SetupVMPoolWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &types.VMPool{}).
		WithValidator(&VMPoolCustomValidator{}).
		WithDefaulter(&VMPoolCustomDefaulter{}).
		Complete()
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

// +kubebuilder:webhook:path=/mutate-cloudhypervisor-quill-today-v1beta1-vmpool,mutating=true,failurePolicy=fail,sideEffects=None,groups=cloudhypervisor.quill.today,resources=vmpools,verbs=create;update,versions=v1beta1,name=mvmpool-v1beta1.kb.io,admissionReviewVersions=v1

// VMPoolCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind VMPool when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type VMPoolCustomDefaulter struct {
	// TODO(user): Add more fields as needed for defaulting
}

var _ admission.Defaulter[*types.VMPool] = &VMPoolCustomDefaulter{}

// Default implements admission.Defaulter so a webhook will be registered for the Kind VMPool.
func (d *VMPoolCustomDefaulter) Default(ctx context.Context, vmpool *types.VMPool) error {
	if vmpool.Namespace == "" {
		vmpool.Namespace = "default"
	}
	vmpoollog.Info("Defaulting for VMPool", "name", vmpool.GetName())

	// TODO(user): fill in your defaulting logic.

	return nil
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// Modifying the path for an invalid path can cause API server errors; failing to locate the webhook.
// +kubebuilder:webhook:path=/validate-cloudhypervisor-quill-today-v1beta1-vmpool,mutating=false,failurePolicy=fail,sideEffects=None,groups=cloudhypervisor.quill.today,resources=vmpools,verbs=create;update,versions=v1beta1,name=vvmpool-v1beta1.kb.io,admissionReviewVersions=v1

// VMPoolCustomValidator struct is responsible for validating the VMPool resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type VMPoolCustomValidator struct {
	// TODO(user): Add more fields as needed for validation
}

var _ admission.Validator[*types.VMPool] = &VMPoolCustomValidator{}

// ValidateCreate implements admission.Validator so a webhook will be registered for the type VMPool.
func (v *VMPoolCustomValidator) ValidateCreate(ctx context.Context, vmpool *types.VMPool) (admission.Warnings, error) {
	// For CREATE, just validate that selector is not empty
	if len(vmpool.Spec.Selector.MatchLabels) == 0 {
		return nil, apierrors.NewInvalid(
			types.GroupVersion.WithKind("VMPool").GroupKind(),
			vmpool.Name,
			field.ErrorList{
				field.Required(field.NewPath("spec").Child("selector").Child("matchLabels"),
					"selector cannot be empty"),
			},
		)
	}

	return nil, nil
}

// ValidateUpdate implements admission.Validator so a webhook will be registered for the type VMPool.
func (v *VMPoolCustomValidator) ValidateUpdate(ctx context.Context, oldPool, newPool *types.VMPool) (admission.Warnings, error) {
	vmsetlog.Info("Validation for VMPool upon update", "name", newPool.GetName())

	if !reflect.DeepEqual(newPool.Spec.Selector, oldPool.Spec.Selector) {
		return nil, apierrors.NewInvalid(
			types.GroupVersion.WithKind("VMPool").GroupKind(),
			newPool.Name,
			field.ErrorList{
				field.Forbidden(field.NewPath("spec").Child("selector"),
					"selector is immutable"),
			},
		)
	}

	return nil, nil
}

// ValidateDelete implements admission.Validator so a webhook will be registered for the type VMPool.
func (v *VMPoolCustomValidator) ValidateDelete(ctx context.Context, vmpool *types.VMPool) (admission.Warnings, error) {
	vmpoollog.Info("Validation for VMPool upon deletion", "name", vmpool.GetName())

	// TODO(user): fill in your validation logic upon object deletion.

	return nil, nil
}
