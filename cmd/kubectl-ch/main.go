// kubectl-ch is a kubectl plugin for ch-operator VMs: `kubectl ch console|port-forward|ssh|pause|
// unpause|restart|migrate`. It talks to ch-vmm-gateway through kube-apiserver using the caller's
// kubeconfig, so access is governed by RBAC on virtualmachines/<subresource>.
package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"

	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	wstransport "k8s.io/client-go/transport/websocket"

	"github.com/nalajala4naresh/ch-operator/pkg/vmaccess"
)

type globalOptions struct {
	kubeconfig string
	context    string
	namespace  string
}

func main() {
	opts := &globalOptions{}
	root := &cobra.Command{
		Use:           "kubectl-ch",
		Short:         "Access and manage ch-operator virtual machines",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&opts.kubeconfig, "kubeconfig", "", "Path to the kubeconfig file.")
	root.PersistentFlags().StringVar(&opts.context, "context", "", "The kubeconfig context to use.")
	root.PersistentFlags().StringVarP(&opts.namespace, "namespace", "n", "", "Namespace of the VM.")

	root.AddCommand(
		consoleCommand(opts),
		portForwardCommand(opts),
		sshCommand(opts),
		actionCommand(opts, vmaccess.SubresourcePause, "Pause a running VM"),
		actionCommand(opts, vmaccess.SubresourceUnpause, "Resume a paused VM"),
		actionCommand(opts, vmaccess.SubresourceRestart, "Reboot a running VM"),
		actionCommand(opts, vmaccess.SubresourceMigrate, "Live-migrate a running VM to another node"),
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func (o *globalOptions) clientConfig() clientcmd.ClientConfig {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	rules.ExplicitPath = o.kubeconfig
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{CurrentContext: o.context})
}

func (o *globalOptions) restConfig() (*rest.Config, string, error) {
	cc := o.clientConfig()
	cfg, err := cc.ClientConfig()
	if err != nil {
		return nil, "", err
	}
	ns := o.namespace
	if ns == "" {
		if ns, _, err = cc.Namespace(); err != nil {
			return nil, "", err
		}
	}
	return cfg, ns, nil
}

// passthroughArgs are the global flags to pass to a nested kubectl-ch invocation.
func (o *globalOptions) passthroughArgs(namespace string) []string {
	args := []string{"--namespace", namespace}
	if o.kubeconfig != "" {
		args = append(args, "--kubeconfig", o.kubeconfig)
	}
	if o.context != "" {
		args = append(args, "--context", o.context)
	}
	return args
}

func subresourcePath(namespace, name, subresource string) string {
	return fmt.Sprintf("/apis/%s/%s/namespaces/%s/virtualmachines/%s/%s",
		vmaccess.GroupName, vmaccess.Version, url.PathEscape(namespace), url.PathEscape(name), subresource)
}

// dialStream opens a WebSocket to a streaming subresource through kube-apiserver.
func dialStream(ctx context.Context, cfg *rest.Config, path string) (*websocket.Conn, error) {
	rt, holder, err := wstransport.RoundTripperFor(cfg)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(cfg.Host)
	if err != nil {
		return nil, err
	}
	u.Path = path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	conn, err := wstransport.Negotiate(rt, holder, req, vmaccess.PlainStreamProtocol)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", path, err)
	}
	return conn, nil
}

func actionCommand(opts *globalOptions, action, short string) *cobra.Command {
	return &cobra.Command{
		Use:   action + " VM",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, ns, err := opts.restConfig()
			if err != nil {
				return err
			}
			cs, err := kubernetes.NewForConfig(cfg)
			if err != nil {
				return err
			}
			result := cs.Discovery().RESTClient().Post().AbsPath(subresourcePath(ns, args[0], action)).Do(cmd.Context())
			if err := result.Error(); err != nil {
				return err
			}
			fmt.Printf("VM %s/%s: %s requested\n", ns, args[0], action)
			return nil
		},
	}
}
