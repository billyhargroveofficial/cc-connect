package codex

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/gorilla/websocket"
)

// connectManagedAppServer attaches to Codex Desktop's existing app-server.
// Its Unix socket speaks WebSocket, while the standalone app-server uses JSONL
// over stdio, so the adapters below preserve the session's JSONL interface.
func connectManagedAppServer(ctx context.Context, codexHome string) (io.Reader, io.WriteCloser, error) {
	if codexHome == "" {
		codexHome = os.Getenv("CODEX_HOME")
	}
	if codexHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, nil, fmt.Errorf("find home directory: %w", err)
		}
		codexHome = filepath.Join(home, ".codex")
	}
	socket := filepath.Join(codexHome, "app-server-control", "app-server-control.sock")
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
		NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}
	connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, _, err := dialer.DialContext(connectCtx, "ws://localhost/", nil)
	if err != nil {
		return nil, nil, fmt.Errorf("connect to %s: %w", socket, err)
	}
	return &managedMessageReader{conn: conn}, &managedMessageWriter{conn: conn}, nil
}

type managedMessageReader struct {
	conn    *websocket.Conn
	pending []byte
}

func (r *managedMessageReader) Read(p []byte) (int, error) {
	for len(r.pending) == 0 {
		kind, message, err := r.conn.ReadMessage()
		if err != nil {
			return 0, err
		}
		if kind != websocket.TextMessage {
			return 0, fmt.Errorf("managed app-server sent non-text WebSocket message")
		}
		r.pending = append(message, '\n')
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}

type managedMessageWriter struct {
	conn *websocket.Conn
}

func (w *managedMessageWriter) Write(p []byte) (int, error) {
	if len(p) == 0 || p[len(p)-1] != '\n' {
		return 0, fmt.Errorf("managed app-server expects one newline-terminated JSON message")
	}
	if err := w.conn.WriteMessage(websocket.TextMessage, bytes.TrimSuffix(p, []byte{'\n'})); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (w *managedMessageWriter) Close() error {
	return w.conn.Close()
}
