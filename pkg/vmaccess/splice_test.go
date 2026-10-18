package vmaccess

import (
	"context"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gorilla/websocket"
)

// pipeListener hands out a single pre-connected net.Pipe end, so an HTTP server can run on an
// in-memory connection inside a synctest bubble.
type pipeListener struct {
	conns chan net.Conn
}

func (l *pipeListener) Accept() (net.Conn, error) {
	c, ok := <-l.conns
	if !ok {
		return nil, net.ErrClosed
	}
	return c, nil
}
func (l *pipeListener) Close() error   { return nil }
func (l *pipeListener) Addr() net.Addr { return &net.UnixAddr{Name: "pipe", Net: "pipe"} }

// spliceSession starts Splice on the server side of an in-memory WebSocket and returns the
// client WebSocket and the backend end of the spliced connection.
func spliceSession(t *testing.T) (client *websocket.Conn, backend net.Conn, spliceDone <-chan error) {
	t.Helper()
	wsServer, wsClient := net.Pipe()
	backendNear, backendFar := net.Pipe()

	done := make(chan error, 1)
	upgrader := websocket.Upgrader{}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			done <- err
			return
		}
		done <- Splice(ws, backendNear)
	})}
	l := &pipeListener{conns: make(chan net.Conn, 1)}
	l.conns <- wsServer
	close(l.conns)
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })

	dialer := websocket.Dialer{
		NetDialContext: func(context.Context, string, string) (net.Conn, error) { return wsClient, nil },
	}
	ws, _, err := dialer.Dial("ws://pipe/", nil)
	if err != nil {
		t.Fatal(err)
	}
	return ws, backendFar, done
}

func TestSpliceCopiesBothWays(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ws, backend, _ := spliceSession(t)
		defer ws.Close()

		go func() { _ = ws.WriteMessage(websocket.BinaryMessage, []byte("to-backend")) }()
		buf := make([]byte, 32)
		n, err := backend.Read(buf)
		if err != nil || string(buf[:n]) != "to-backend" {
			t.Fatalf("backend read %q, %v", buf[:n], err)
		}

		go func() { _, _ = backend.Write([]byte("to-client")) }()
		_, msg, err := ws.ReadMessage()
		if err != nil || string(msg) != "to-client" {
			t.Fatalf("client read %q, %v", msg, err)
		}
	})
}

// TestSplicePingsIdleSessions verifies the keepalive: an idle session is pinged every
// pingInterval. synctest's fake clock makes the 90s of idleness instantaneous.
func TestSplicePingsIdleSessions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ws, _, _ := spliceSession(t)
		defer ws.Close()

		var pings atomic.Int32
		ws.SetPingHandler(func(string) error {
			pings.Add(1)
			return nil
		})
		// Control frames are only processed while reading.
		go func() {
			for {
				if _, _, err := ws.NextReader(); err != nil {
					return
				}
			}
		}()

		time.Sleep(3*pingInterval + time.Second)
		synctest.Wait()
		if got := pings.Load(); got != 3 {
			t.Fatalf("got %d pings after %s, want 3", got, 3*pingInterval+time.Second)
		}
	})
}

// TestSpliceClosesWebSocketWhenBackendCloses checks that the client sees a normal close when
// the console or TCP connection ends, and that Splice reports it as a clean shutdown.
func TestSpliceClosesWebSocketWhenBackendCloses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ws, backend, done := spliceSession(t)
		defer ws.Close()

		backend.Close()
		_, _, err := ws.ReadMessage()
		if !websocket.IsCloseError(err, websocket.CloseNormalClosure) {
			t.Fatalf("client got %v, want normal close", err)
		}
		if err := <-done; err != nil {
			t.Fatalf("Splice returned %v, want nil", err)
		}
	})
}
