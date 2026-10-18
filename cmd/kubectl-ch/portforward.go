package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/nalajala4naresh/chvmm-api/vmclient"
	"github.com/spf13/cobra"
)

type portMapping struct {
	local, remote int
}

func parsePortMapping(s string) (portMapping, error) {
	localStr, remoteStr, found := strings.Cut(s, ":")
	if !found {
		remoteStr = localStr
	}
	remote, err := strconv.Atoi(remoteStr)
	if err != nil || remote < 1 || remote > 65535 {
		return portMapping{}, fmt.Errorf("invalid port %q", remoteStr)
	}
	local := remote
	if localStr != "" {
		if local, err = strconv.Atoi(localStr); err != nil || local < 0 || local > 65535 {
			return portMapping{}, fmt.Errorf("invalid local port %q", localStr)
		}
	}
	return portMapping{local: local, remote: remote}, nil
}

func portForwardCommand(opts *globalOptions) *cobra.Command {
	var address string
	var stdio bool
	cmd := &cobra.Command{
		Use:   "port-forward VM [LOCAL_PORT:]REMOTE_PORT...",
		Short: "Forward local ports to a VM",
		Example: `  kubectl ch port-forward myvm 8080:80
  kubectl ch port-forward --stdio myvm 22   # for use as an ssh ProxyCommand`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			vms, ns, err := opts.vms()
			if err != nil {
				return err
			}
			vm := args[0]
			var mappings []portMapping
			for _, a := range args[1:] {
				m, err := parsePortMapping(a)
				if err != nil {
					return err
				}
				mappings = append(mappings, m)
			}

			if stdio {
				if len(mappings) != 1 {
					return fmt.Errorf("--stdio takes exactly one port")
				}
				return forward(cmd.Context(), vms, vm, mappings[0].remote, stdioConn{})
			}

			errCh := make(chan error, len(mappings))
			for _, m := range mappings {
				l, err := net.Listen("tcp", net.JoinHostPort(address, strconv.Itoa(m.local)))
				if err != nil {
					return err
				}
				defer l.Close()
				fmt.Fprintf(os.Stderr, "Forwarding from %s -> %s/%s:%d\n", l.Addr(), ns, vm, m.remote)
				go func(l net.Listener, remote int) {
					for {
						conn, err := l.Accept()
						if err != nil {
							errCh <- err
							return
						}
						go func() {
							if err := forward(cmd.Context(), vms, vm, remote, conn); err != nil {
								fmt.Fprintf(os.Stderr, "forward to port %d: %v\n", remote, err)
							}
						}()
					}
				}(l, m.remote)
			}
			select {
			case <-cmd.Context().Done():
				return nil
			case err := <-errCh:
				return err
			}
		},
	}
	cmd.Flags().StringVar(&address, "address", "127.0.0.1", "Local address to listen on.")
	cmd.Flags().BoolVar(&stdio, "stdio", false, "Forward stdin/stdout to the single given port instead of listening.")
	return cmd
}

func forward(ctx context.Context, vms vmclient.VirtualMachineInterface, vm string, port int, local io.ReadWriteCloser) error {
	defer local.Close()
	remote, err := vms.PortForward(ctx, vm, port)
	if err != nil {
		return err
	}
	defer remote.Close()

	// Return when either side is done; the deferred closes unblock the other copy.
	errCh := make(chan error, 2)
	go func() {
		_, err := io.Copy(remote, local)
		errCh <- err
	}()
	go func() {
		_, err := io.Copy(local, remote)
		errCh <- err
	}()
	if err := <-errCh; err != nil && !errors.Is(err, net.ErrClosed) {
		return err
	}
	return nil
}

type stdioConn struct{}

func (stdioConn) Read(p []byte) (int, error)  { return os.Stdin.Read(p) }
func (stdioConn) Write(p []byte) (int, error) { return os.Stdout.Write(p) }
func (stdioConn) Close() error                { return os.Stdout.Close() }

func sshCommand(opts *globalOptions) *cobra.Command {
	var port int
	var user string
	cmd := &cobra.Command{
		Use:   "ssh [USER@]VM [-- SSH_ARGS...]",
		Short: "SSH into a VM through the API server (uses the local ssh client)",
		Example: `  kubectl ch ssh ubuntu@myvm
  kubectl ch ssh myvm -l ubuntu -- -i ~/.ssh/id_ed25519`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, ns, err := opts.vms()
			if err != nil {
				return err
			}
			target := args[0]
			vm := target
			if u, name, found := strings.Cut(target, "@"); found {
				user, vm = u, name
			}
			self, err := os.Executable()
			if err != nil {
				return err
			}

			proxy := append([]string{self, "port-forward", "--stdio"}, opts.passthroughArgs(ns)...)
			proxy = append(proxy, vm, strconv.Itoa(port))
			sshArgs := []string{
				"-o", "ProxyCommand=" + shellJoin(proxy),
				// The host key belongs to the VM, not to a resolvable host name.
				"-o", "HostKeyAlias=" + vm + "." + ns + ".ch-vmm",
			}
			if user != "" {
				sshArgs = append(sshArgs, "-l", user)
			}
			sshArgs = append(sshArgs, args[1:]...)
			sshArgs = append(sshArgs, vm+"."+ns)

			ssh := exec.CommandContext(cmd.Context(), "ssh", sshArgs...)
			ssh.Stdin, ssh.Stdout, ssh.Stderr = os.Stdin, os.Stdout, os.Stderr
			return ssh.Run()
		},
	}
	cmd.Flags().IntVarP(&port, "port", "p", 22, "SSH port in the VM.")
	cmd.Flags().StringVarP(&user, "login", "l", "", "User to log in as.")
	return cmd
}

// shellJoin quotes args for the shell that ssh runs ProxyCommand with.
func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(quoted, " ")
}
