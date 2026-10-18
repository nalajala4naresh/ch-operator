/*
Copyright 2024.

Licensed under the MIT License. See LICENSE file in the project root for full license information.
*/

package webhooks

import (
	"context"

	types "github.com/nalajala4naresh/chvmm-api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// nolint:unused
// log is for logging in this package.
var virtualdisksnapshotlog = logf.Log.WithName("virtualdisksnapshot-resource")

// SetupVirtualDiskSnapshotWebhookWithManager registers the webhook for VirtualDiskSnapshot in the manager.
func SetupVirtualDiskSnapshotWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &types.VirtualDiskSnapshot{}).
		WithValidator(&VirtualDiskSnapshotCustomValidator{}).
		WithDefaulter(&VirtualDiskSnapshotCustomDefaulter{}).
		Complete()
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

// +kubebuilder:webhook:path=/mutate-cloudhypervisor-quill-today-v1beta1-virtualdisksnapshot,mutating=true,failurePolicy=fail,sideEffects=None,groups=cloudhypervisor.quill.today,resources=virtualdisksnapshots,verbs=create;update,versions=v1beta1,name=mvirtualdisksnapshot-v1beta1.kb.io,admissionReviewVersions=v1

// VirtualDiskSnapshotCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind VirtualDiskSnapshot when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type VirtualDiskSnapshotCustomDefaulter struct {
	// TODO(user): Add more fields as needed for defaulting
}

var _ admission.Defaulter[*types.VirtualDiskSnapshot] = &VirtualDiskSnapshotCustomDefaulter{}

// Default implements admission.Defaulter so a webhook will be registered for the Kind VirtualDiskSnapshot.
func (d *VirtualDiskSnapshotCustomDefaulter) Default(_ context.Context, virtualdisksnapshot *types.VirtualDiskSnapshot) error {
	virtualdisksnapshotlog.Info("Defaulting for VirtualDiskSnapshot", "name", virtualdisksnapshot.GetName())

	// TODO(user): fill in your defaulting logic.

	return nil
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// Modifying the path for an invalid path can cause API server errors; failing to locate the webhook.
// +kubebuilder:webhook:path=/validate-cloudhypervisor-quill-today-v1beta1-virtualdisksnapshot,mutating=false,failurePolicy=fail,sideEffects=None,groups=cloudhypervisor.quill.today,resources=virtualdisksnapshots,verbs=create;update,versions=v1beta1,name=vvirtualdisksnapshot-v1beta1.kb.io,admissionReviewVersions=v1

// VirtualDiskSnapshotCustomValidator struct is responsible for validating the VirtualDiskSnapshot resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type VirtualDiskSnapshotCustomValidator struct {
	// TODO(user): Add more fields as needed for validation
}

var _ admission.Validator[*types.VirtualDiskSnapshot] = &VirtualDiskSnapshotCustomValidator{}

// ValidateCreate implements admission.Validator so a webhook will be registered for the type VirtualDiskSnapshot.
func (v *VirtualDiskSnapshotCustomValidator) ValidateCreate(_ context.Context, virtualdisksnapshot *types.VirtualDiskSnapshot) (admission.Warnings, error) {
	virtualdisksnapshotlog.Info("Validation for VirtualDiskSnapshot upon creation", "name", virtualdisksnapshot.GetName())

	// TODO(user): fill in your validation logic upon object creation.

	return nil, nil
}

// ValidateUpdate implements admission.Validator so a webhook will be registered for the type VirtualDiskSnapshot.
func (v *VirtualDiskSnapshotCustomValidator) ValidateUpdate(_ context.Context, oldObj, virtualdisksnapshot *types.VirtualDiskSnapshot) (admission.Warnings, error) {
	virtualdisksnapshotlog.Info("Validation for VirtualDiskSnapshot upon update", "name", virtualdisksnapshot.GetName())

	// TODO(user): fill in your validation logic upon object update.

	return nil, nil
}

// ValidateDelete implements admission.Validator so a webhook will be registered for the type VirtualDiskSnapshot.
func (v *VirtualDiskSnapshotCustomValidator) ValidateDelete(ctx context.Context, virtualdisksnapshot *types.VirtualDiskSnapshot) (admission.Warnings, error) {
	virtualdisksnapshotlog.Info("Validation for VirtualDiskSnapshot upon deletion", "name", virtualdisksnapshot.GetName())

	// TODO(user): fill in your validation logic upon object deletion.

	return nil, nil
}
