package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// escapeByte (Ctrl+]) ends a console session, as in telnet and virtctl.
const escapeByte = 0x1d

func consoleCommand(opts *globalOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "console VM",
		Short: "Connect to the serial console of a VM (Ctrl+] to exit)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vms, ns, err := opts.vms()
			if err != nil {
				return err
			}
			conn, err := vms.Console(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			defer conn.Close()

			fmt.Fprintf(os.Stderr, "Connected to %s/%s. Escape sequence is ^]\n", ns, args[0])
			if term.IsTerminal(int(os.Stdin.Fd())) {
				state, err := term.MakeRaw(int(os.Stdin.Fd()))
				if err != nil {
					return fmt.Errorf("set terminal to raw mode: %w", err)
				}
				defer func() { _ = term.Restore(int(os.Stdin.Fd()), state) }()
			}
			return runConsole(conn, os.Stdin, os.Stdout)
		},
	}
}

// runConsole copies stdin to the console until the escape byte or EOF, and the console to stdout.
func runConsole(conn net.Conn, stdin io.Reader, stdout io.Writer) error {
	errCh := make(chan error, 2)
	go func() {
		_, err := io.Copy(stdout, conn)
		errCh <- err
	}()
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := stdin.Read(buf)
			if n > 0 {
				data := buf[:n]
				i := bytes.IndexByte(data, escapeByte)
				if i >= 0 {
					data = data[:i]
				}
				if len(data) > 0 {
					if _, werr := conn.Write(data); werr != nil {
						errCh <- werr
						return
					}
				}
				if i >= 0 {
					errCh <- nil
					return
				}
			}
			if err != nil {
				errCh <- err
				return
			}
		}
	}()

	err := <-errCh
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		fmt.Fprint(os.Stderr, "\r\nDisconnected.\r\n")
		return nil
	}
	return err
}
