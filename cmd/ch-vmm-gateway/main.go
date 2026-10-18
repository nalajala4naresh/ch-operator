// ch-vmm-gateway is an aggregated API server that gives users RBAC-controlled access to their VMs:
// serial console and port forwarding (streamed through ch-vmm-daemon on the VM's node) and VM
// actions (pause, unpause, restart, migrate).
package main

import (
	"fmt"
	"net/http"
	"os"

	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	genericapiserver "k8s.io/apiserver/pkg/server"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/component-base/logs"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nalajala4naresh/ch-operator/pkg/apiserver"
	"github.com/nalajala4naresh/ch-operator/pkg/vmaccess"
	v1beta1 "github.com/nalajala4naresh/chvmm-api/v1beta1"
)

func main() {
	if err := run(); err != nil {
		klog.ErrorS(err, "ch-vmm-gateway failed")
		os.Exit(1)
	}
}

func run() error {
	opts := apiserver.NewOptions()
	var (
		daemonNamespace  = "ch-vmm-system"
		daemonSelector   = "name=ch-vmm-daemon"
		daemonServerName = "ch-vmm-daemon.ch-vmm-system.svc"
		daemonPort       = vmaccess.DaemonAccessPort
		clientCertDir    = "/var/lib/ch-vmm-gateway/client-cert"
	)
	fs := pflag.CommandLine
	opts.AddFlags(fs)
	fs.StringVar(&daemonNamespace, "daemon-namespace", daemonNamespace, "Namespace of the ch-vmm-daemon pods.")
	fs.StringVar(&daemonSelector, "daemon-selector", daemonSelector, "Label selector of the ch-vmm-daemon pods.")
	fs.StringVar(&daemonServerName, "daemon-server-name", daemonServerName, "Server name in the ch-vmm-daemon certificate.")
	fs.IntVar(&daemonPort, "daemon-port", daemonPort, "Port of the ch-vmm-daemon VM access endpoint.")
	fs.StringVar(&clientCertDir, "daemon-client-cert-dir", clientCertDir,
		"Directory with tls.crt, tls.key and ca.crt used to authenticate to ch-vmm-daemon.")
	logs.AddFlags(fs)
	pflag.Parse()
	logs.InitLogs()
	defer logs.FlushLogs()

	selector, err := labels.Parse(daemonSelector)
	if err != nil {
		return fmt.Errorf("parse --daemon-selector: %w", err)
	}
	tlsConfig, err := vmaccess.ClientTLSConfig(clientCertDir, daemonServerName)
	if err != nil {
		return fmt.Errorf("load daemon client TLS config: %w", err)
	}

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(v1beta1.AddToScheme(scheme))
	// An uncached client: requests are user-driven and low volume, and daemon pods are looked up
	// with a server-side spec.nodeName field selector.
	c, err := client.New(ctrl.GetConfigOrDie(), client.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("create client: %w", err)
	}

	handler := apiserver.NewHandler(c, apiserver.DaemonConfig{
		Namespace:  daemonNamespace,
		Selector:   selector,
		Port:       daemonPort,
		ServerName: daemonServerName,
		// WebSocket upgrades need HTTP/1.1.
		Transport: &http.Transport{TLSClientConfig: tlsConfig, ForceAttemptHTTP2: false},
	})
	server, err := opts.NewServer(handler)
	if err != nil {
		return err
	}
	return apiserver.Run(genericapiserver.SetupSignalContext(), server)
}
