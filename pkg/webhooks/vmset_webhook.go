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
var vmsetlog = logf.Log.WithName("vmset-resource")

// SetupVMSetWebhookWithManager registers the webhook for VMSet in the manager.
func SetupVMSetWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &types.VMSet{}).
		WithValidator(&VMSetCustomValidator{}).
		WithDefaulter(&VMSetCustomDefaulter{}).
		Complete()
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

// +kubebuilder:webhook:path=/mutate-cloudhypervisor-quill-today-v1beta1-vmset,mutating=true,failurePolicy=fail,sideEffects=None,groups=cloudhypervisor.quill.today,resources=vmsets,verbs=create;update,versions=v1beta1,name=mvmset-v1beta1.kb.io,admissionReviewVersions=v1

// VMSetCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind VMSet when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type VMSetCustomDefaulter struct {
	// TODO(user): Add more fields as needed for defaulting
}

var _ admission.Defaulter[*types.VMSet] = &VMSetCustomDefaulter{}

// Default implements admission.Defaulter so a webhook will be registered for the Kind VMSet.
func (d *VMSetCustomDefaulter) Default(ctx context.Context, vmset *types.VMSet) error {
	vmsetlog.Info("Defaulting for VMSet", "name", vmset.GetName())

	// TODO(user): fill in your defaulting logic.

	return nil
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// Modifying the path for an invalid path can cause API server errors; failing to locate the webhook.
// +kubebuilder:webhook:path=/validate-cloudhypervisor-quill-today-v1beta1-vmset,mutating=false,failurePolicy=fail,sideEffects=None,groups=cloudhypervisor.quill.today,resources=vmsets,verbs=create;update,versions=v1beta1,name=vvmset-v1beta1.kb.io,admissionReviewVersions=v1

// VMSetCustomValidator struct is responsible for validating the VMSet resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type VMSetCustomValidator struct {
	// TODO(user): Add more fields as needed for validation
}

var _ admission.Validator[*types.VMSet] = &VMSetCustomValidator{}

// ValidateCreate implements admission.Validator so a webhook will be registered for the type VMSet.
func (v *VMSetCustomValidator) ValidateCreate(ctx context.Context, vmset *types.VMSet) (admission.Warnings, error) {
	// For CREATE, just validate that selector is not empty
	if len(vmset.Spec.Selector.MatchLabels) == 0 {
		return nil, apierrors.NewInvalid(
			types.GroupVersion.WithKind("VMSet").GroupKind(),
			vmset.Name,
			field.ErrorList{
				field.Required(field.NewPath("spec").Child("selector").Child("matchLabels"),
					"selector cannot be empty"),
			},
		)
	}

	return nil, nil

}

// ValidateUpdate implements admission.Validator so a webhook will be registered for the type VMSet.
func (v *VMSetCustomValidator) ValidateUpdate(ctx context.Context, oldSet, newSet *types.VMSet) (admission.Warnings, error) {
	vmsetlog.Info("Validation for VMSet upon update", "name", newSet.GetName())

	if !reflect.DeepEqual(newSet.Spec.Selector, oldSet.Spec.Selector) {
		return nil, apierrors.NewInvalid(
			types.GroupVersion.WithKind("VMSet").GroupKind(),
			newSet.Name,
			field.ErrorList{
				field.Forbidden(field.NewPath("spec").Child("selector"),
					"selector is immutable"),
			},
		)
	}

	return nil, nil
}

// ValidateDelete implements admission.Validator so a webhook will be registered for the type VMSet.
func (v *VMSetCustomValidator) ValidateDelete(ctx context.Context, vmset *types.VMSet) (admission.Warnings, error) {
	vmsetlog.Info("Validation for VMSet upon deletion", "name", vmset.GetName())

	// TODO(user): fill in your validation logic upon object deletion.

	return nil, nil
}
