# ch-operator: Lightweight Virtualization Add-on for Kubernetes

[![build](https://github.com/nalajala4naresh/ch-operator/actions/workflows/build.yml/badge.svg)](https://github.com/nalajala4naresh/ch-operator/actions/workflows/build.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/nalajala4naresh/ch-operator)](https://goreportcard.com/report/github.com/nalajala4naresh/ch-operator)
[![codecov](https://codecov.io/gh/nalajala4naresh/ch-operator/branch/main/graph/badge.svg?token=6GXYM2BFLT)](https://codecov.io/gh/nalajala4naresh/ch-operator)

ch-operator is a [Kubernetes](https://github.com/kubernetes/kubernetes) add-on for running [Cloud Hypervisor](https://github.com/cloud-hypervisor/cloud-hypervisor) virtual machines. By using Cloud Hypervisor as the underlying hypervisor, ch-operator enables a lightweight and secure way to run fully virtualized workloads in a canonical Kubernetes cluster.

Compared to [KubeVirt](https://github.com/kubevirt/kubevirt), ch-operator:

- does not use libvirt or QEMU. By leveraging Cloud Hypervisor, VMs have a lower memory footprint (≈30MB), higher performance and smaller attack surface.
- does not require a long-running per-Pod launcher process, which further reduces runtime memory overhead (≈80MB).

Compared to [VirtInk](https://github.com/smartxworks/virtink), ch-operator:
- supports VM snapshot and rollback.
- supports newer Kubernetes and controller-runtime versions, and Cloud Hypervisor v54.1.
- provides `VMPool` and `VMSet` to manage fleets of VMs; see [VMPool](docs/vmpool.md) and [VMSet](docs/vmset.md).

ch-operator consists of the following components:

- `ch-vmm-controller` is the cluster-wide controller, responsible for creating Pods to run Cloud Hypervisor VMs.
- `ch-vmm-daemon` is the per-Node daemon, responsible for further controlling Cloud Hypervisor VMs on a per-Node basis.
- `ch-vmm-prerunner` is the per-Pod pre-runner, responsible for preparing VM networks and building Cloud Hypervisor VM configuration.
- `ch-vmm-gateway` is an aggregated API server that serves VM subresources (console, port-forward, pause, restart, migrate) through `kube-apiserver`.
- `kubectl-ch` is the kubectl plugin for those subresources; see [VM access](docs/vm_access.md).

**NOTE**: ch-operator is still a work in progress, its API may change without prior notice.

## Installation

### Requirements

A few requirements need to be met before you can begin:

- Kubernetes v1.35+ for release v1.2.0 and later, which use in-place Pod resizing for [VM vertical scaling](docs/vm_vertical_scaling.md). On Kubernetes older than v1.35, use release v1.1.0.
- Kubernetes apiserver must have `--allow-privileged=true` in order to run ch-operator's privileged DaemonSet. It's usually set by default.
- [cert-manager](https://cert-manager.io/) v1.16 installed in the Kubernetes cluster. You can install it with `kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.16.1/cert-manager.yaml`.

### Install

1. Deploy ch-operator:

   ```bash
   kubectl apply -f https://github.com/nalajala4naresh/ch-operator/releases/latest/download/ch-operator.yaml
   ```

2. Deploy the [CDI](https://github.com/kubevirt/containerized-data-importer) operator, which manages the DataVolume objects used as VM disks:

   ```bash
   export VERSION=$(curl -s https://api.github.com/repos/kubevirt/containerized-data-importer/releases/latest | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/')
   kubectl create -f https://github.com/kubevirt/containerized-data-importer/releases/download/$VERSION/cdi-operator.yaml
   kubectl create -f https://github.com/kubevirt/containerized-data-importer/releases/download/$VERSION/cdi-cr.yaml
   ```

3. Deploy the [external snapshotter](https://github.com/kubernetes-csi/external-snapshotter) CRDs and snapshot controller, which disk snapshots depend on:

   ```bash
   git clone https://github.com/kubernetes-csi/external-snapshotter
   cd external-snapshotter
   kubectl kustomize client/config/crd | kubectl create -f -
   kubectl -n kube-system kustomize deploy/kubernetes/snapshot-controller | kubectl create -f -
   ```

4. Optionally, install the `kubectl-ch` plugin by downloading the `kubectl-ch_<os>_<arch>` binary from the [latest release](https://github.com/nalajala4naresh/ch-operator/releases/latest) and placing it on your `PATH` as `kubectl-ch`.

#### Hypervisor Version

ch-operator ships and is tested with Cloud Hypervisor v54.1.

#### Container Runtime Support

ch-operator currently supports the following container runtimes:

- Docker
- containerd

Other container runtimes, which do not use virtualization features, should work too. However, they are not tested officially.

#### Hardware Virtualization Support

Hardware with virtualization support is required. You should check if `/dev/kvm` exists on each Kubernetes nodes.

#### Host Kernel Version

- Minimum: v4.11

## Getting Started

### Create a VM

Apply the following manifest to Kubernetes. Note it uses a [container rootfs](docs/disks_and_volumes.md#containerrootfs-volume) and as such doesn’t persist data.

```bash
cat <<EOF | kubectl apply -f -
apiVersion: cloudhypervisor.quill.today/v1beta1
kind: VirtualMachine
metadata:
  name: ubuntu-container-rootfs
spec:
  instance:
    memory:
      size: 1Gi
    kernel:
      image: nalajalanaresh/ch-vmm-kernel:5.15
      cmdline: "console=ttyS0 root=/dev/vda rw"
    disks:
      - name: ubuntu
      - name: cloud-init
    interfaces:
      - name: pod
  volumes:
    - name: ubuntu
      containerRootfs:
        image: nalajalanaresh/virtmanager-container-rootfs-ubuntu
        size: 4Gi
    - name: cloud-init
      cloudInit:
        userData: |-
          #cloud-config
          password: password
          chpasswd: { expire: False }
          ssh_pwauth: True
  networks:
    - name: pod
      pod: {}
EOF
```


Like starting pods, it will take some time to pull the image and start running the VM. You can wait for the VM become running as follows:

```bash
kubectl wait vm ubuntu-container-rootfs --for jsonpath='{.status.phase}'=Running --timeout -1s
```

### Access the VM

With the `kubectl-ch` plugin installed, you can reach the VM through the API server:

```bash
kubectl ch console ubuntu-container-rootfs      # serial console, Ctrl+] to exit
kubectl ch ssh ubuntu@ubuntu-container-rootfs   # SSH through port-forward
```

See [VM access](docs/vm_access.md) for port-forwarding, RBAC and the Go SDK.

Without the plugin, you can use an SSH client inside the cluster:

```bash
export VM_NAME=ubuntu-container-rootfs
export VM_POD_NAME=$(kubectl get vm $VM_NAME -o jsonpath='{.status.vmPodName}')
export VM_IP=$(kubectl get pod $VM_POD_NAME -o jsonpath='{.status.podIP}')
kubectl run ssh-$VM_NAME --rm --image=alpine --restart=Never -it -- /bin/sh -c "apk add openssh-client && ssh ubuntu@$VM_IP"
```

Enter `password` when you are prompted to enter password, which is set by the cloud-init data in the VM manifest.


### Take a Snapshot of the VM

ch-operator supports taking snapshots of VMs with the `VMSnapShot` CRD. DataVolume and PersistentVolumeClaim based disks are snapshotted with the default VolumeSnapshotClass. The memory snapshot is stored in object storage, either S3 (`s3://<bucket>`) or GCS (`gcs://<bucket>`).

```yaml
apiVersion: cloudhypervisor.quill.today/v1beta1
kind: VMSnapShot 
metadata:
  name: jammy-snapshot
  namespace: default
spec:
  skipMemorySnapshot: false
  vm: ubuntu-ch
  bucket: gcs://ch-snapshots
```

### Restore with a VM Rollback

ch-operator supports VM rollback from a `VMSnapShot`. This creates a new VM and keeps the original VM untouched.

```yaml
apiVersion: cloudhypervisor.quill.today/v1beta1
kind: VMRollback
metadata:
  labels:
    app.kubernetes.io/name: ch-vmm
    app.kubernetes.io/managed-by: kustomize
  name: jammy-ch-rollback
spec:
  snapshot: jammy-snapshot
```

### Manage the VM

ch-operator supports various VM power actions. For example, you can power off the VM created above as follows:

```bash
export VM_NAME=ubuntu-container-rootfs
export POWER_ACTION=PowerOff
kubectl patch vm $VM_NAME --subresource=status --type=merge -p "{\"status\":{\"powerAction\":\"$POWER_ACTION\"}}"
```
You can also `Shutdown`, `Reset` or `Reboot` a running VM. To start a powered-off VM, you can `PowerOn` it.

Pausing, resuming, restarting and live-migrating a VM go through the `kubectl-ch` plugin:

```bash
kubectl ch pause $VM_NAME
kubectl ch unpause $VM_NAME
kubectl ch restart $VM_NAME
kubectl ch migrate $VM_NAME
```

### VFIO GPU Passthrough

ch-operator supports GPU passthrough to VMs using VFIO (Virtual Function I/O). This allows VMs to directly access physical GPU devices for high-performance workloads such as machine learning, graphics rendering, or GPU-accelerated computing.

#### Prerequisites

- GPU device plugin installed in the cluster (e.g., [NVIDIA Kubevirt Device Plugin](https://github.com/NVIDIA/kubevirt-gpu-device-plugin) or custom device plugin)
- IOMMU enabled on the host system (add `intel_iommu=on` or `amd_iommu=on` to kernel boot parameters)
- GPU devices must be bound to `vfio-pci` driver on the host nodes
- The GPU must be exposed as a Kubernetes extended resource (e.g., `nvidia.com/GP104_GEFORCE_GTX_1080`)

#### Creating a VM with GPU Passthrough

To create a VM with GPU passthrough, specify the GPU in the VM spec:

```yaml
apiVersion: cloudhypervisor.quill.today/v1beta1
kind: VirtualMachine
metadata:
  name: ubuntu-ch
  namespace: default
spec:
  instance:
    kernel:
      image: nalajalanaresh/ch-vmm-kernel:5.15
      cmdline: "console=ttyS0 root=/dev/vda1 rw ds=nocloud"
    memory:
      size: 4Gi
    gpus:
      - name: geforce
        resourceName: nvidia.com/GP104_GEFORCE_GTX_1080  # Change to your GPU resource name
    disks:
      - name: ubuntu
    interfaces:
      - name: pod
  volumes:
    - name: ubuntu
      dataVolume:
        volumeName: ubuntu-ch-jammy
  networks:
    - name: pod
      pod: {}
```

The `resourceName` should match the extended resource name exposed by your GPU device plugin. For NVIDIA GPUs, the webhook automatically sets the `resourceEnvName` if it's not specified. The GPU device will be passed through to the VM using VFIO, allowing the guest OS to directly access the physical GPU hardware.

**Note**: Ensure that the GPU device plugin is properly configured and the GPU resources are available on the target node before creating the VM.

## Documentation

- [Disks and volumes](docs/disks_and_volumes.md)
- [Interfaces and networks](docs/interfaces_and_networks.md)
- [Direct kernel boot](docs/direct_kernel_boot.md)
- [Dedicated CPU placement](docs/dedicated_cpu_placement.md)
- [VM vertical scaling](docs/vm_vertical_scaling.md)
- [VM access: console, port-forward, SSH and actions](docs/vm_access.md)
- [VMPool](docs/vmpool.md) and [VMSet](docs/vmset.md)
- [Vhost-user network](docs/vhost-user-net.md) and its [performance test](docs/vhost-user-net-test.md)
- [VM state machine](docs/fsm-knowledge-graph.md)
