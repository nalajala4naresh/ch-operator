// Package vmaccess holds what ch-vmm-gateway, ch-vmm-daemon and kubectl-ch share for streaming
// access to VMs: API names, the daemon's streaming endpoint layout and the WebSocket splice.
package vmaccess

import (
	"fmt"
	"path/filepath"

	"k8s.io/apimachinery/pkg/types"
)

const (
	// GroupName is the API group served by ch-vmm-gateway through the kube-apiserver aggregation layer.
	GroupName = "subresources.cloudhypervisor.quill.today"
	Version   = "v1beta1"

	// PlainStreamProtocol is the WebSocket subprotocol for a raw byte stream: every binary
	// message carries bytes of the console or TCP stream, in order.
	PlainStreamProtocol = "plain.cloudhypervisor.quill.today"

	// DaemonAccessPort is where ch-vmm-daemon serves streaming endpoints to ch-vmm-gateway (mTLS).
	DaemonAccessPort = 8444
	// UserHeader carries the end user's name from ch-vmm-gateway to the daemon, for logging only.
	UserHeader = "X-Ch-Vmm-User"

	// SerialSocketName is the cloud-hypervisor serial socket inside the VM pod's ch-vmm emptyDir.
	SerialSocketName = "serial.sock"
	// VMPodRunDir is where the ch-vmm emptyDir is mounted in the VM pod.
	VMPodRunDir = "/var/run/ch-vmm"
)

// Subresources served by ch-vmm-gateway.
const (
	SubresourceConsole     = "console"
	SubresourcePortForward = "portforward"
	SubresourcePause       = "pause"
	SubresourceUnpause     = "unpause"
	SubresourceRestart     = "restart"
	SubresourceMigrate     = "migrate"
)

// StreamingSubresources are long-running WebSocket subresources.
var StreamingSubresources = map[string]bool{
	SubresourceConsole:     true,
	SubresourcePortForward: true,
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
