package vmaccess

import (
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	pingInterval = 30 * time.Second
	writeTimeout = 10 * time.Second
	bufferSize   = 32 * 1024
)

// Splice copies bytes between a WebSocket and conn until either side closes, then closes both.
// Binary and text messages from the WebSocket are written to conn as-is; bytes read from conn
// are sent as binary messages. It pings the WebSocket periodically so idle sessions survive
// proxies with idle timeouts.
func Splice(ws *websocket.Conn, conn io.ReadWriteCloser) error {
	var once sync.Once
	closeAll := func() {
		once.Do(func() {
			_ = ws.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(writeTimeout))
			_ = ws.Close()
			_ = conn.Close()
		})
	}
	defer closeAll()

	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(pingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if err := ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeTimeout)); err != nil {
					closeAll()
					return
				}
			}
		}
	}()

	errCh := make(chan error, 2)
	go func() {
		errCh <- copyToWebSocket(ws, conn)
		closeAll()
	}()
	go func() {
		errCh <- copyFromWebSocket(conn, ws)
		closeAll()
	}()

	err := <-errCh
	if isExpectedClose(err) {
		return nil
	}
	return err
}

func copyToWebSocket(ws *websocket.Conn, r io.Reader) error {
	buf := make([]byte, bufferSize)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			_ = ws.SetWriteDeadline(time.Now().Add(writeTimeout))
			if werr := ws.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
				return werr
			}
		}
		if err != nil {
			return err
		}
	}
}

func copyFromWebSocket(w io.Writer, ws *websocket.Conn) error {
	for {
		msgType, r, err := ws.NextReader()
		if err != nil {
			return err
		}
		if msgType != websocket.BinaryMessage && msgType != websocket.TextMessage {
			continue
		}
		if _, err := io.Copy(w, r); err != nil {
			return err
		}
	}
}

func isExpectedClose(err error) bool {
	return err == nil ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, net.ErrClosed) ||
		websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway)
}
