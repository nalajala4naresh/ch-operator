/*
Copyright 2024.

Licensed under the MIT License. See LICENSE file in the project root for full license information.
*/

package webhooks

import (
	"context"

	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	cloudhypervisorquilltodayv1beta1 "github.com/nalajala4naresh/chvmm-api/v1beta1"
)

// nolint:unused
// log is for logging in this package.
var virtualdisklog = logf.Log.WithName("virtualdisk-resource")

// SetupVirtualDiskWebhookWithManager registers the webhook for VirtualDisk in the manager.
func SetupVirtualDiskWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &cloudhypervisorquilltodayv1beta1.VirtualDisk{}).
		WithValidator(&VirtualDiskCustomValidator{}).
		WithDefaulter(&VirtualDiskCustomDefaulter{}).
		Complete()
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

// +kubebuilder:webhook:path=/mutate-cloudhypervisor-quill-today-v1beta1-virtualdisk,mutating=true,failurePolicy=fail,sideEffects=None,groups=cloudhypervisor.quill.today,resources=virtualdisks,verbs=create;update,versions=v1beta1,name=mvirtualdisk-v1beta1.kb.io,admissionReviewVersions=v1

// VirtualDiskCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind VirtualDisk when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type VirtualDiskCustomDefaulter struct {
	// TODO(user): Add more fields as needed for defaulting
}

var _ admission.Defaulter[*cloudhypervisorquilltodayv1beta1.VirtualDisk] = &VirtualDiskCustomDefaulter{}

// Default implements admission.Defaulter so a webhook will be registered for the Kind VirtualDisk.
func (d *VirtualDiskCustomDefaulter) Default(_ context.Context, virtualdisk *cloudhypervisorquilltodayv1beta1.VirtualDisk) error {
	virtualdisklog.Info("Defaulting for VirtualDisk", "name", virtualdisk.GetName())

	// TODO(user): fill in your defaulting logic.

	return nil
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// Modifying the path for an invalid path can cause API server errors; failing to locate the webhook.
// +kubebuilder:webhook:path=/validate-cloudhypervisor-quill-today-v1beta1-virtualdisk,mutating=false,failurePolicy=fail,sideEffects=None,groups=cloudhypervisor.quill.today,resources=virtualdisks,verbs=create;update,versions=v1beta1,name=vvirtualdisk-v1beta1.kb.io,admissionReviewVersions=v1

// VirtualDiskCustomValidator struct is responsible for validating the VirtualDisk resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type VirtualDiskCustomValidator struct {
	// TODO(user): Add more fields as needed for validation
}

var _ admission.Validator[*cloudhypervisorquilltodayv1beta1.VirtualDisk] = &VirtualDiskCustomValidator{}

// ValidateCreate implements admission.Validator so a webhook will be registered for the type VirtualDisk.
func (v *VirtualDiskCustomValidator) ValidateCreate(_ context.Context, virtualdisk *cloudhypervisorquilltodayv1beta1.VirtualDisk) (admission.Warnings, error) {
	virtualdisklog.Info("Validation for VirtualDisk upon creation", "name", virtualdisk.GetName())

	// TODO(user): fill in your validation logic upon object creation.

	return nil, nil
}

// ValidateUpdate implements admission.Validator so a webhook will be registered for the type VirtualDisk.
func (v *VirtualDiskCustomValidator) ValidateUpdate(_ context.Context, oldObj, virtualdisk *cloudhypervisorquilltodayv1beta1.VirtualDisk) (admission.Warnings, error) {
	virtualdisklog.Info("Validation for VirtualDisk upon update", "name", virtualdisk.GetName())

	// TODO(user): fill in your validation logic upon object update.

	return nil, nil
}

// ValidateDelete implements admission.Validator so a webhook will be registered for the type VirtualDisk.
func (v *VirtualDiskCustomValidator) ValidateDelete(ctx context.Context, virtualdisk *cloudhypervisorquilltodayv1beta1.VirtualDisk) (admission.Warnings, error) {
	virtualdisklog.Info("Validation for VirtualDisk upon deletion", "name", virtualdisk.GetName())

	// TODO(user): fill in your validation logic upon object deletion.

	return nil, nil
}
