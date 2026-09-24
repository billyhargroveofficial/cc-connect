package codex

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/chenhg5/cc-connect/core"
)

func TestManagedAppServerResumesThreadThroughExistingSocket(t *testing.T) {
	// A short path avoids Linux's Unix socket path length limit.
	root, err := os.MkdirTemp("", "cc-managed-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	controlDir := filepath.Join(root, "app-server-control")
	if err := os.Mkdir(controlDir, 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(controlDir, "app-server-control.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	var mu sync.Mutex
	var methods []string
	var resumeParams map[string]any
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var req struct {
				ID     any            `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			if err := json.Unmarshal(data, &req); err != nil {
				return
			}
			mu.Lock()
			methods = append(methods, req.Method)
			if req.Method == "thread/resume" {
				resumeParams = req.Params
			}
			mu.Unlock()
			if req.ID == nil {
				continue
			}
			var result any = map[string]any{}
			switch req.Method {
			case "initialize":
				result = map[string]any{"protocolVersion": "2"}
			case "thread/resume":
				result = map[string]any{
					"thread": map[string]any{"id": "thread-123"},
					"cwd":    "/tmp", "model": "gpt-6-sol", "reasoningEffort": "max",
				}
			case "turn/start":
				result = map[string]any{"turn": map[string]any{"id": "turn-123"}}
			}
			if err := conn.WriteJSON(map[string]any{"id": req.ID, "result": result}); err != nil {
				return
			}
		}
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	s := &appServerSession{
		url: "managed://", codexHome: root, workDir: "/tmp",
		model: "gpt-6-sol", effort: "max", mode: "yolo",
		ctx: ctx, cancel: cancel, events: make(chan core.Event, 8),
		pending: make(map[int64]chan rpcResponseEnvelope),
	}
	s.alive.Store(true)
	if err := s.connect(); err != nil {
		t.Fatalf("connect to managed app-server: %v", err)
	}
	defer s.Close()
	if err := s.initialize(); err != nil {
		t.Fatalf("initialize managed app-server: %v", err)
	}
	if err := s.ensureThread("thread-123"); err != nil {
		t.Fatalf("resume existing thread: %v", err)
	}
	if got := s.CurrentSessionID(); got != "thread-123" {
		t.Fatalf("session id = %q, want thread-123", got)
	}
	if err := s.Send("ping", "message-123", nil, nil); err != nil {
		t.Fatalf("start turn through managed app-server: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"initialize", "initialized", "thread/resume", "turn/start"}
	if len(methods) != len(want) {
		t.Fatalf("methods = %v, want %v", methods, want)
	}
	for i := range want {
		if methods[i] != want[i] {
			t.Fatalf("methods = %v, want %v", methods, want)
		}
	}
	if resumeParams["threadId"] != "thread-123" || resumeParams["excludeTurns"] != true {
		t.Fatalf("resume params = %v, want thread ID and excludeTurns=true", resumeParams)
	}
}

func TestManagedAppServerLiveResume(t *testing.T) {
	threadID := os.Getenv("CC_CONNECT_MANAGED_TEST_THREAD")
	if threadID == "" {
		t.Skip("set CC_CONNECT_MANAGED_TEST_THREAD to test against a running Codex daemon")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := newAppServerSession(ctx, "managed://", ".", "", "", "yolo", threadID, "", "", nil, os.Getenv("CODEX_HOME"), "", "")
	if err != nil {
		t.Fatalf("resume thread through managed app-server: %v", err)
	}
	defer s.Close()
	if got := s.CurrentSessionID(); got != threadID {
		t.Fatalf("session id = %q, want %q", got, threadID)
	}
}
