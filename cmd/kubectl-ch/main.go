// kubectl-ch is a kubectl plugin for ch-operator VMs: `kubectl ch console|port-forward|ssh|pause|
// unpause|restart|migrate`. It talks to ch-vmm-gateway through kube-apiserver using the caller's
// kubeconfig, so access is governed by RBAC on virtualmachines/<subresource>.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/nalajala4naresh/chvmm-api/subresources"
	"github.com/nalajala4naresh/chvmm-api/vmclient"
	"github.com/spf13/cobra"
	"k8s.io/client-go/tools/clientcmd"
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
		actionCommand(opts, subresources.Pause, "Pause a running VM", vmclient.VirtualMachineInterface.Pause),
		actionCommand(opts, subresources.Unpause, "Resume a paused VM", vmclient.VirtualMachineInterface.Unpause),
		actionCommand(opts, subresources.Restart, "Reboot a running VM", vmclient.VirtualMachineInterface.Restart),
		migrateCommand(opts),
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

// vms returns a client for VMs in the selected namespace, and that namespace.
func (o *globalOptions) vms() (vmclient.VirtualMachineInterface, string, error) {
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
	c, err := vmclient.NewForConfig(cfg)
	if err != nil {
		return nil, "", err
	}
	return c.VirtualMachines(ns), ns, nil
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

func actionCommand(opts *globalOptions, action, short string,
	do func(vmclient.VirtualMachineInterface, context.Context, string) error) *cobra.Command {
	return &cobra.Command{
		Use:   action + " VM",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vms, ns, err := opts.vms()
			if err != nil {
				return err
			}
			if err := do(vms, cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Printf("VM %s/%s: %s requested\n", ns, args[0], action)
			return nil
		},
	}
}

func migrateCommand(opts *globalOptions) *cobra.Command {
	return &cobra.Command{
		Use:   subresources.Migrate + " VM",
		Short: "Live-migrate a running VM to another node",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vms, ns, err := opts.vms()
			if err != nil {
				return err
			}
			migration, err := vms.Migrate(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			fmt.Printf("VM %s/%s: migration %s created\n", ns, args[0], migration.Name)
			return nil
		},
	}
}
