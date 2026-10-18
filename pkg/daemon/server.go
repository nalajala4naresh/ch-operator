package daemon

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/nalajala4naresh/ch-operator/pkg/cloudhypervisor"
	"github.com/nalajala4naresh/ch-operator/pkg/cloudutils"
	"github.com/nalajala4naresh/ch-operator/pkg/daemon/proto"
	daemon_proto "github.com/nalajala4naresh/ch-operator/pkg/daemon/proto"
	"github.com/nalajala4naresh/ch-operator/pkg/vmactions"
	"github.com/nalajala4naresh/ch-operator/pkg/volumeutil"
	"github.com/nalajala4naresh/chvmm-api/v1beta1"

	emptypb "google.golang.org/protobuf/types/known/emptypb"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// VMManager serves the gRPC VmService.
//
// Deprecated: VM actions are served by ch-vmm-gateway as subresources (kubectl ch pause|unpause|restart),
// which enforces Kubernetes RBAC. This gRPC service will be removed in a future release.
type VMManager struct {
	client.Client
	daemon_proto.UnimplementedVmServiceServer
}

func (vmm *VMManager) GetVMStatus(ctx context.Context, req *daemon_proto.GetVmstatusRequest) (*daemon_proto.GetVmStatusResponse, error) {

	// take the VM name and go to kube apiserver
	var vm v1beta1.VirtualMachine
	name := types.NamespacedName{Namespace: req.Namespace, Name: req.Vmname}
	err := vmm.Get(ctx, name, &vm)
	if err != nil {

		if k8s_errors.IsNotFound(err) {
			st := status.New(codes.NotFound, "")
			return nil, st.Err()
		} else {
			return nil, err
		}

	}

	res := &proto.GetVmStatusResponse{Status: &daemon_proto.VmStatus{Status: string(vm.Status.Phase)}}

	return res, nil

}

func (vmm *VMManager) PauseVM(ctx context.Context, req *daemon_proto.PauseVMRequest) (*emptypb.Empty, error) {
	err := vmactions.Pause(ctx, vmm.Client, types.NamespacedName{Namespace: req.Namespace, Name: req.Name})
	return new(emptypb.Empty), toGRPCError(err)
}

func (vmm *VMManager) getVM(ctx context.Context, name, namespace string) (*v1beta1.VirtualMachine, error) {

	nn := types.NamespacedName{Name: name, Namespace: namespace}
	vm := &v1beta1.VirtualMachine{}

	err := vmm.Client.Get(ctx, nn, vm)

	if err != nil {

		if k8s_errors.IsNotFound(err) {
			st := status.New(codes.NotFound, fmt.Sprintf("vm not found %s in namespace %s", name, namespace))
			return nil, st.Err()
		} else {
			return nil, err
		}

	}
	return vm, nil
}

func (vmm *VMManager) ResumeVM(ctx context.Context, req *daemon_proto.ResumeVMRequest) (*emptypb.Empty, error) {
	err := vmactions.Unpause(ctx, vmm.Client, types.NamespacedName{Namespace: req.Namespace, Name: req.Name})
	return new(emptypb.Empty), toGRPCError(err)
}

func (vmm *VMManager) RebootVM(ctx context.Context, req *daemon_proto.RebootVMRequest) (*emptypb.Empty, error) {
	err := vmactions.Restart(ctx, vmm.Client, types.NamespacedName{Namespace: req.Namespace, Name: req.Name})
	return new(emptypb.Empty), toGRPCError(err)
}

// toGRPCError maps a vmactions error to a gRPC status error.
func toGRPCError(err error) error {
	switch {
	case err == nil:
		return nil
	case k8s_errors.IsNotFound(err):
		return status.New(codes.NotFound, err.Error()).Err()
	case k8s_errors.IsConflict(err):
		return status.New(codes.FailedPrecondition, err.Error()).Err()
	default:
		return status.New(codes.Internal, err.Error()).Err()
	}
}

func (vmm *VMManager) CreateSnapshot(ctx context.Context, req *daemon_proto.CreateVMSnapshot) (*emptypb.Empty, error) {
	vm, err := vmm.getVM(ctx, req.Name, req.Namespace)
	if err != nil {
		return nil, err
	}
	destination := filepath.Join(getVMDataDirPath(vm), req.Id)
	// now get the directory where we vm and create a directory if does not exists
	if _, err := os.Stat(destination); os.IsNotExist(err) {
		fmt.Printf("creating destination %s for snapshot %s", destination, req.Id)
		os.MkdirAll(destination, 0755)
		defer os.RemoveAll(destination)

	} else {
		return nil, status.New(codes.AlreadyExists, fmt.Sprintf("snapshot %s already exists", req.Id)).Err()
	}
	var snapdestination string
	if req.Destination != "" {
		snapdestination = filepath.Join(req.Destination, req.Destination, req.Id)

	} else {
		snapdestination = filepath.Join("/var/run/ch-vmm/", req.Destination, req.Id)
	}

	// now get clh client or firecracker
	out := new(emptypb.Empty)

	chClient := getCloudHypervisorClient(vm)

	// check if vm is in paused condition

	info, err := chClient.VmInfo(ctx)
	if err != nil {
		return nil, status.New(codes.Internal, fmt.Sprintf("failed to get vm info with error in snapshot %s", err)).Err()
	}
	if info.State != "Paused" {
		return nil, status.New(codes.FailedPrecondition, fmt.Sprintf("vm is not in paused state, current state is   %s", info.State)).Err()

	}
	//create snapshot
	destionationUrl := "file://" + snapdestination
	snapshot := cloudhypervisor.VmSnapshotConfig{DestinationUrl: destionationUrl}
	err = chClient.VmSnapshot(ctx, &snapshot)
	if err != nil {
		return nil, status.New(codes.Internal, fmt.Sprintf("failed to create snapshot %s error is %s , snapshotdir %s", req.Id, err, destionationUrl)).Err()
	}

	//after taking snapshot compress and create a tar archive out of it

	dirEntries, err := os.ReadDir(destination)
	if err != nil {
		log.Fatalf("Failed to read directory: %v", err)
	}
	files := []string{}
	for _, file := range dirEntries {
		files = append(files, filepath.Join(destination, file.Name()))
	}

	archd := filepath.Join(getVMDataDirPath(vm), req.Id+".zstd.tar")
	defer os.RemoveAll(archd)

	err = volumeutil.CreateArchive(archd, files)
	if err != nil {
		return nil, status.New(codes.Internal, fmt.Sprintf("failed to create archive out of snapshot %s error is %s", req.Id, err)).Err()
	}

	bucketName := "fc-snapshots"
	err = cloudutils.UploadObjectToGCS(bucketName, archd, req.Id+".zstd.tar")

	if err != nil {
		return nil, status.New(codes.Internal, fmt.Sprintf("failed to upload snapshot (%s)to gcs  %s error is %s", req.Id, bucketName, err)).Err()
	}

	return out, nil
}

func (vmm *VMManager) RestoreSnapshot(context.Context, *daemon_proto.RestoreVMSnapshot) (*emptypb.Empty, error) {
	return nil, status.Errorf(codes.Unimplemented, "method RestoreSnapshot not implemented")
}

func NewServer(client client.Client) *VMManager {
	return &VMManager{Client: client}
}
