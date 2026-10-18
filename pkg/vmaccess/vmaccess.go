// Package vmaccess holds what ch-vmm-gateway and ch-vmm-daemon share for streaming access to
// VMs: the daemon's streaming endpoint layout, its mTLS setup and the WebSocket splice. The public
// API contract (group, subprotocol, subresource names) is in chvmm-api/subresources.
package vmaccess

import (
	"fmt"
	"path/filepath"

	"github.com/nalajala4naresh/chvmm-api/subresources"
	"k8s.io/apimachinery/pkg/types"
)

const (
	// DaemonAccessPort is where ch-vmm-daemon serves streaming endpoints to ch-vmm-gateway (mTLS).
	DaemonAccessPort = 8444
	// UserHeader carries the end user's name from ch-vmm-gateway to the daemon, for logging only.
	UserHeader = "X-Ch-Vmm-User"

	// SerialSocketName is the cloud-hypervisor serial socket inside the VM pod's ch-vmm emptyDir.
	SerialSocketName = "serial.sock"
	// VMPodRunDir is where the ch-vmm emptyDir is mounted in the VM pod.
	VMPodRunDir = "/var/run/ch-vmm"
)

// StreamingSubresources are long-running WebSocket subresources.
var StreamingSubresources = map[string]bool{
	subresources.Console:     true,
	subresources.PortForward: true,
}

// DaemonConsolePath is the daemon path for a VM's serial console.
func DaemonConsolePath(namespace, name string) string {
	return fmt.Sprintf("/v1/namespaces/%s/virtualmachines/%s/console", namespace, name)
}

// DaemonPortForwardPath is the daemon path for a TCP connection to a VM port.
func DaemonPortForwardPath(namespace, name string, port int) string {
	return fmt.Sprintf("/v1/namespaces/%s/virtualmachines/%s/portforward/%d", namespace, name, port)
}

// HostSerialSocketPath is the VM's serial socket as seen from the daemon, which mounts the
// kubelet pods directory at /pods.
func HostSerialSocketPath(vmPodUID types.UID) string {
	return filepath.Join("/pods", string(vmPodUID), "volumes/kubernetes.io~empty-dir/ch-vmm", SerialSocketName)
}
