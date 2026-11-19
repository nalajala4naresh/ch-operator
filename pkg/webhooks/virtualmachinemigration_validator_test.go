package webhooks

import (
	"context"
	"strings"
	"testing"

	types "github.com/nalajala4naresh/chvmm-api/v1beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestMigrationValidatorValidateCreate(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := types.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	vm := func(name string, conditions ...metav1.Condition) *types.VirtualMachine {
		v := &types.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}}
		v.Status.Conditions = conditions
		return v
	}
	migratable := func(status metav1.ConditionStatus, message string) metav1.Condition {
		return metav1.Condition{
			Type:               string(types.VirtualMachineMigratable),
			Status:             status,
			Reason:             "Test",
			Message:            message,
			LastTransitionTime: metav1.Now(),
		}
	}

	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		vm("ready", migratable(metav1.ConditionTrue, "")),
		vm("not-migratable", migratable(metav1.ConditionFalse, "VM has a hostPath volume")),
		vm("unknown"),
	).Build()
	validator := &VirtualMachineMigrationCustomValidator{Reader: reader}

	tests := []struct {
		vmName  string
		wantErr string
	}{
		{vmName: "ready"},
		{vmName: "", wantErr: "Required value"},
		{vmName: "missing", wantErr: "Not found"},
		{vmName: "unknown", wantErr: "migratable condition status is unknown"},
		{vmName: "not-migratable", wantErr: "VM has a hostPath volume"},
	}
	for _, tt := range tests {
		t.Run(tt.vmName, func(t *testing.T) {
			vmm := &types.VirtualMachineMigration{ObjectMeta: metav1.ObjectMeta{Name: "m", Namespace: "default"}}
			vmm.Spec.VMName = tt.vmName

			_, err := validator.ValidateCreate(context.Background(), vmm)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if !apierrors.IsInvalid(err) {
				t.Fatalf("want an Invalid error, got %v", err)
			}
			if strings.Contains(err.Error(), "Internal error") {
				t.Fatalf("validation failed internally: %v", err)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tt.wantErr)
			}
		})
	}
}
