package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/nalajala4naresh/ch-operator/pkg/vmaccess"
)

// escapeByte (Ctrl+]) ends a console session, as in telnet and virtctl.
const escapeByte = 0x1d

func consoleCommand(opts *globalOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "console VM",
		Short: "Connect to the serial console of a VM (Ctrl+] to exit)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, ns, err := opts.restConfig()
			if err != nil {
				return err
			}
			ws, err := dialStream(cmd.Context(), cfg, subresourcePath(ns, args[0], vmaccess.SubresourceConsole))
			if err != nil {
				return err
			}
			defer ws.Close()

			fmt.Fprintf(os.Stderr, "Connected to %s/%s. Escape sequence is ^]\n", ns, args[0])
			if term.IsTerminal(int(os.Stdin.Fd())) {
				state, err := term.MakeRaw(int(os.Stdin.Fd()))
				if err != nil {
					return fmt.Errorf("set terminal to raw mode: %w", err)
				}
				defer func() { _ = term.Restore(int(os.Stdin.Fd()), state) }()
			}
			return runConsole(ws, os.Stdin, os.Stdout)
		},
	}
}

// runConsole copies stdin to the console until the escape byte or EOF, and the console to stdout.
func runConsole(ws *websocket.Conn, stdin io.Reader, stdout io.Writer) error {
	errCh := make(chan error, 2)
	go func() {
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				errCh <- err
				return
			}
			if _, err := stdout.Write(data); err != nil {
				errCh <- err
				return
			}
		}
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
					if werr := ws.WriteMessage(websocket.BinaryMessage, data); werr != nil {
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
	_ = ws.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	if err == nil || errors.Is(err, io.EOF) || websocket.IsCloseError(err, websocket.CloseNormalClosure) {
		fmt.Fprint(os.Stderr, "\r\nDisconnected.\r\n")
		return nil
	}
	return err
}
