// Package vmactions implements user-initiated VM actions (pause, unpause, restart, migrate).
// Errors are Kubernetes API errors, so callers can map them to HTTP or gRPC status codes.
package vmactions

import (
	"context"
	"fmt"

	v1beta1 "github.com/nalajala4naresh/chvmm-api/v1beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var vmResource = schema.GroupResource{Group: v1beta1.GroupVersion.Group, Resource: "virtualmachines"}

// Pause requests that a running VM be paused.
func Pause(ctx context.Context, c client.Client, key types.NamespacedName) error {
	return updateStatus(ctx, c, key, func(vm *v1beta1.VirtualMachine) error {
		if vm.Status.Phase != v1beta1.VirtualMachineRunning {
			return preconditionFailed(vm, "VM must be Running to pause, current phase is %q", vm.Status.Phase)
		}
		vm.Status.Phase = v1beta1.VirtualMachinePaused
		return nil
	})
}

// Unpause requests that a paused VM be resumed.
func Unpause(ctx context.Context, c client.Client, key types.NamespacedName) error {
	return updateStatus(ctx, c, key, func(vm *v1beta1.VirtualMachine) error {
		if vm.Status.Phase != v1beta1.VirtualMachinePaused {
			return preconditionFailed(vm, "VM must be Paused to unpause, current phase is %q", vm.Status.Phase)
		}
		vm.Status.Phase = v1beta1.VirtualMachineResumed
		return nil
	})
}

// Restart requests a reboot of a running VM.
func Restart(ctx context.Context, c client.Client, key types.NamespacedName) error {
	return updateStatus(ctx, c, key, func(vm *v1beta1.VirtualMachine) error {
		if vm.Status.Phase != v1beta1.VirtualMachineRunning {
			return preconditionFailed(vm, "VM must be Running to restart, current phase is %q", vm.Status.Phase)
		}
		vm.Status.PowerAction = v1beta1.VirtualMachineReboot
		return nil
	})
}

// Migrate creates a VirtualMachineMigration for a running VM and returns it.
func Migrate(ctx context.Context, c client.Client, key types.NamespacedName) (*v1beta1.VirtualMachineMigration, error) {
	var vm v1beta1.VirtualMachine
	if err := c.Get(ctx, key, &vm); err != nil {
		return nil, err
	}
	if vm.Status.Phase != v1beta1.VirtualMachineRunning {
		return nil, preconditionFailed(&vm, "VM must be Running to migrate, current phase is %q", vm.Status.Phase)
	}
	if vm.Status.Migration != nil && vm.Status.Migration.Phase != "" &&
		vm.Status.Migration.Phase != v1beta1.VirtualMachineMigrationSucceeded &&
		vm.Status.Migration.Phase != v1beta1.VirtualMachineMigrationFailed {
		return nil, preconditionFailed(&vm, "VM already has a migration in phase %q", vm.Status.Migration.Phase)
	}

	migration := &v1beta1.VirtualMachineMigration{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: vm.Name + "-migration-",
			Namespace:    vm.Namespace,
		},
		Spec: v1beta1.VirtualMachineMigrationSpec{VMName: vm.Name},
	}
	if err := c.Create(ctx, migration); err != nil {
		return nil, err
	}
	return migration, nil
}

// updateStatus applies mutate to the latest VM and updates its status, retrying on write
// conflicts. An error from mutate is a failed precondition and is returned without retrying.
func updateStatus(ctx context.Context, c client.Client, key types.NamespacedName, mutate func(*v1beta1.VirtualMachine) error) error {
	var mutateErr error
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var vm v1beta1.VirtualMachine
		if err := c.Get(ctx, key, &vm); err != nil {
			return err
		}
		if mutateErr = mutate(&vm); mutateErr != nil {
			return nil
		}
		return c.Status().Update(ctx, &vm)
	})
	if mutateErr != nil {
		return mutateErr
	}
	return err
}

func preconditionFailed(vm *v1beta1.VirtualMachine, format string, args ...any) error {
	return apierrors.NewConflict(vmResource, vm.Name, fmt.Errorf(format, args...))
}
