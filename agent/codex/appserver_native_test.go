package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/chenhg5/cc-connect/core"
)

type nativeTestMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params map[string]any  `json:"params"`
	Result json.RawMessage `json:"result"`
}

func nativeTestManagedServer(t *testing.T) (string, <-chan nativeTestMessage) {
	t.Helper()
	root, err := os.MkdirTemp("", "cc-native-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	control := filepath.Join(root, "app-server-control")
	if err := os.Mkdir(control, 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(control, "app-server-control.sock"))
	if err != nil {
		t.Fatal(err)
	}
	requests := make(chan nativeTestMessage, 32)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			var req nativeTestMessage
			if err := conn.ReadJSON(&req); err != nil {
				return
			}
			requests <- req
			if len(req.ID) == 0 {
				continue
			}
			result := map[string]any{}
			switch req.Method {
			case "initialize":
				result["protocolVersion"] = "2"
			case "thread/start", "thread/resume":
				for _, id := range []string{"unrelated-thread", "our-thread"} {
					if err := conn.WriteJSON(map[string]any{"method": "thread/started", "params": map[string]any{
						"thread": map[string]any{"id": id, "cwd": req.Params["cwd"]},
					}}); err != nil {
						return
					}
				}
				result = map[string]any{
					"thread": map[string]any{"id": "our-thread"},
					"cwd":    req.Params["cwd"], "model": "gpt-6-sol", "reasoningEffort": "ultra",
				}
			case "turn/start":
				result["turn"] = map[string]any{"id": "our-turn"}
			}
			if err := conn.WriteJSON(map[string]any{"id": req.ID, "result": result}); err != nil {
				return
			}
		}
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return root, requests
}

func takeNativeTestRequest(t *testing.T, messages <-chan nativeTestMessage, method string) nativeTestMessage {
	t.Helper()
	for {
		select {
		case message := <-messages:
			if message.Method == method {
				return message
			}
		case <-time.After(time.Second):
			t.Fatalf("no %s request", method)
		}
	}
}

func TestNativeManagedSessionRetainsWorkspaceOverridesAndEarlyThreadNotification(t *testing.T) {
	for _, resume := range []bool{false, true} {
		t.Run(fmt.Sprintf("resume=%t", resume), func(t *testing.T) {
			root, requests := nativeTestManagedServer(t)
			workspace := t.TempDir()
			config := map[string]any{"skills.config": []map[string]any{{"path": "/skills/example/SKILL.md", "enabled": false}}}
			base, err := New(map[string]any{
				"cmd": os.Args[0], "backend": "app_server", "app_server_url": "managed",
				"work_dir": workspace, "codex_home": root, "model": "gpt-6-sol",
				"reasoning_effort": "ultra", "native_events": true,
				"app_server_config": config, "developer_instructions": "Shared bot rules.",
			})
			if err != nil {
				t.Fatal(err)
			}
			a := base.(*Agent)
			// Mutating a caller-owned map must not change the session config.
			config["skills.config"] = nil
			native := make(chan core.NativeEvent, 8)
			a.SetNativeEventHandler(func(event core.NativeEvent) { native <- event })
			a.SetDynamicTools([]map[string]any{{
				"type": "function", "name": "list_bots", "description": "List bots",
				"inputSchema": map[string]any{"type": "object"},
			}}, nil)
			id, method := "", "thread/start"
			if resume {
				id, method = "our-thread", "thread/resume"
			}
			s, err := a.StartSession(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			initialize := takeNativeTestRequest(t, requests, "initialize")
			if _, exists := initialize.Params["capabilities"].(map[string]any)["optOutNotificationMethods"]; exists {
				t.Fatal("native session opted out of deltas")
			}
			start := takeNativeTestRequest(t, requests, method)
			if start.Params["cwd"] != workspace || start.Params["developerInstructions"] != "Shared bot rules." {
				t.Fatalf("thread workspace/instructions lost: %v", start.Params)
			}
			gotConfig := start.Params["config"].(map[string]any)
			if gotConfig["model_reasoning_effort"] != "ultra" || gotConfig["skills.config"] == nil {
				t.Fatalf("thread overrides lost: %v", gotConfig)
			}
			if !resume && start.Params["dynamicTools"] == nil {
				t.Fatal("new thread omitted dynamic tools")
			}
			if resume && start.Params["dynamicTools"] != nil {
				t.Fatal("resume sent unsupported dynamicTools field")
			}
			select {
			case event := <-native:
				if event.Method != "thread/started" || event.RootThreadID != "our-thread" || !strings.Contains(string(event.Params), "our-thread") {
					t.Fatalf("wrong startup event: %#v", event)
				}
			case <-time.After(time.Second):
				t.Fatal("own early thread notification was lost")
			}
			select {
			case event := <-native:
				t.Fatalf("unrelated early notification leaked: %#v", event)
			default:
			}
			if err := s.Send("hello", "message-1", nil, nil); err != nil {
				t.Fatal(err)
			}
			turn := takeNativeTestRequest(t, requests, "turn/start")
			if turn.Params["cwd"] != workspace || turn.Params["effort"] != "ultra" {
				t.Fatalf("turn workspace/effort lost: %v", turn.Params)
			}
		})
	}
}

func TestNativeEventsAreScopedToRootAndItsDescendants(t *testing.T) {
	var got []core.NativeEvent
	s := &appServerSession{
		events: make(chan core.Event, 8), stdin: &lockedWriteCloser{},
		native: appServerNativeOptions{enabled: true, observer: func(event core.NativeEvent) { got = append(got, event) }},
	}
	s.threadID.Store("root")
	notify := func(method, params string) { s.handleNotification(method, json.RawMessage(params)) }
	notify("item/completed", `{"threadId":"foreign","item":{"type":"collabAgentToolCall","tool":"spawnAgent","receiverThreadIds":["foreign-child"]}}`)
	notify("item/reasoning/summaryTextDelta", `{"threadId":"foreign-child","delta":"private"}`)
	notify("item/completed", `{"threadId":"root","item":{"type":"collabAgentToolCall","tool":"spawnAgent","receiverThreadIds":["child"]}}`)
	notify("item/reasoning/summaryTextDelta", `{"threadId":"child","delta":"summary"}`)
	notify("thread/started", `{"thread":{"id":"grandchild","parentThreadId":"child"}}`)
	notify("item/agentMessage/delta", `{"threadId":"grandchild","delta":"answer"}`)
	notify("account/updated", `{"account":{"email":"private@example.invalid"}}`)
	notify("error", `{"threadId":"foreign","message":"another bot failed"}`)
	s.handleServerRequest(map[string]json.RawMessage{
		"id": json.RawMessage(`"tool-7"`), "method": json.RawMessage(`"item/tool/call"`),
		"params": json.RawMessage(`{"threadId":"child","turnId":"child-turn","callId":"call-7","tool":"list_bots","arguments":{}}`),
	})
	if len(got) != 5 || string(got[4].RequestID) != `"tool-7"` {
		t.Fatalf("scoped native events = %#v", got)
	}
	for _, event := range got {
		if event.Backend != "codex" || event.RootThreadID != "root" || event.Timestamp.IsZero() || strings.Contains(string(event.Params), "foreign") {
			t.Fatalf("invalid/leaked native event: %#v", event)
		}
	}
	if !strings.Contains(s.stdin.(*lockedWriteCloser).String(), "tool-7") {
		t.Fatal("descendant tool request was not answered")
	}
	select {
	case event := <-s.events:
		t.Fatalf("foreign error or descendant text entered legacy stream: %#v", event)
	default:
	}
}

func TestNormalizedTurnIdentitySurvivesGoalContinuation(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(fmt.Sprintf("native=%t", native), func(t *testing.T) {
			s := &appServerSession{
				events: make(chan core.Event, 16),
				native: appServerNativeOptions{enabled: native},
			}
			s.threadID.Store("root")
			notify := func(method, params string) { s.handleNotification(method, json.RawMessage(params)) }
			notify("turn/started", `{"threadId":"root","turn":{"id":"first"}}`)
			notify("item/completed", `{"threadId":"root","turnId":"first","item":{"type":"agentMessage","text":"Working."}}`)
			notify("item/started", `{"threadId":"root","turnId":"first","item":{"type":"commandExecution","command":"pwd"}}`)
			notify("item/completed", `{"threadId":"root","turnId":"first","item":{"type":"commandExecution","command":"pwd","status":"completed","exitCode":0}}`)
			notify("item/completed", `{"threadId":"root","turnId":"first","item":{"type":"reasoning","summary":["Summary."]}}`)
			notify("item/completed", `{"threadId":"root","turnId":"first","item":{"type":"agentMessage","text":"Finished."}}`)
			notify("turn/completed", `{"threadId":"root","turn":{"id":"first","status":"completed"}}`)
			// The runtime has not read the legacy queue when the goal's next
			// native turn arrives. Every preceding event still belongs to first.
			notify("turn/started", `{"threadId":"root","turn":{"id":"second"}}`)
			want := []core.EventType{core.EventThinking, core.EventToolUse, core.EventToolResult, core.EventThinking, core.EventText, core.EventResult}
			for _, typ := range want {
				select {
				case event := <-s.events:
					if event.Type != typ {
						t.Fatalf("type = %v, want %v", event.Type, typ)
					}
					if native {
						if event.Metadata["threadId"] != "root" || event.Metadata["turnId"] != "first" {
							t.Fatalf("previous turn identity lost: %#v", event)
						}
					} else if event.Metadata != nil {
						t.Fatalf("legacy payload changed: %#v", event.Metadata)
					}
				default:
					t.Fatalf("missing %v event", typ)
				}
			}
			notify("turn/completed", `{"threadId":"root","turn":{"id":"second","status":"failed","error":{"message":"failed"}}}`)
			notify("turn/started", `{"threadId":"root","turn":{"id":"third"}}`)
			failed := <-s.events
			if failed.Type != core.EventError || native && failed.Metadata["turnId"] != "second" {
				t.Fatalf("failed turn identity lost: %#v", failed)
			}
			if native {
				result := <-s.events
				if result.Type != core.EventResult || !result.Done || result.Metadata["turnId"] != "second" || result.Metadata["threadId"] != "root" {
					t.Fatalf("failed turn did not settle: %#v", result)
				}
			}
			// Server errors use their own turnId, even if another turn is active.
			notify("error", `{"threadId":"root","turnId":"second","message":"late error"}`)
			late := <-s.events
			if late.Type != core.EventError || native && late.Metadata["turnId"] != "second" {
				t.Fatalf("error attributed to current turn: %#v", late)
			}
		})
	}
}

func TestFailedNativeTurnSettlesOnceAndRetryableErrorsDoNotSettle(t *testing.T) {
	s := &appServerSession{
		events: make(chan core.Event, 16),
		native: appServerNativeOptions{enabled: true},
	}
	s.threadID.Store("root")
	notify := func(method, params string) { s.handleNotification(method, json.RawMessage(params)) }
	notify("turn/started", `{"threadId":"root","turn":{"id":"failed-turn"}}`)
	notify("error", `{"threadId":"root","turnId":"failed-turn","message":"retrying","willRetry":true}`)
	retry := <-s.events
	if retry.Type != core.EventError || !s.isCurrentTurn("failed-turn") {
		t.Fatalf("retry notification stopped the active turn: %#v", retry)
	}
	select {
	case event := <-s.events:
		t.Fatalf("retry notification emitted terminal event: %#v", event)
	default:
	}
	notify("turn/completed", `{"threadId":"root","turn":{"id":"failed-turn","status":"failed","error":{"message":"model unavailable"}}}`)
	notify("thread/status/changed", `{"threadId":"root","status":{"type":"idle"}}`)
	notify("turn/completed", `{"threadId":"root","turn":{"id":"failed-turn","status":"failed"}}`)
	errorEvent, result := <-s.events, <-s.events
	if errorEvent.Type != core.EventError || errorEvent.Error == nil || errorEvent.Error.Error() != "model unavailable" {
		t.Fatalf("failure details lost: %#v", errorEvent)
	}
	if result.Type != core.EventResult || !result.Done || result.Metadata["threadId"] != "root" || result.Metadata["turnId"] != "failed-turn" {
		t.Fatalf("failure has no terminal result: %#v", result)
	}
	select {
	case event := <-s.events:
		t.Fatalf("duplicate terminal event: %#v", event)
	default:
	}
	notify("turn/started", `{"threadId":"root","turn":{"id":"next-turn"}}`)
	if !s.isCurrentTurn("next-turn") {
		t.Fatal("failure left the session unable to start another turn")
	}
}

func TestNativePermissionMetadataUsesRequestThreadAndTurn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &appServerSession{
		ctx: ctx, stdin: &lockedWriteCloser{}, events: make(chan core.Event, 8),
		native:           appServerNativeOptions{enabled: true},
		pendingApprovals: make(map[string]chan core.PermissionResult),
		currentTurn:      "root-turn", pendingMsgs: []string{"Root commentary."},
	}
	s.threadID.Store("root")
	s.addNativeDescendant("child")
	s.handleServerRequest(map[string]json.RawMessage{
		"id": json.RawMessage(`"approve-1"`), "method": json.RawMessage(`"item/commandExecution/requestApproval"`),
		"params": json.RawMessage(`{"threadId":"child","turnId":"child-turn","command":"pwd"}`),
	})
	thinking, permission := <-s.events, <-s.events
	if thinking.Type != core.EventThinking || thinking.Metadata["threadId"] != "root" || thinking.Metadata["turnId"] != "root-turn" {
		t.Fatalf("root commentary attributed to child: %#v", thinking)
	}
	if permission.Type != core.EventPermissionRequest || permission.Metadata["threadId"] != "child" || permission.Metadata["turnId"] != "child-turn" {
		t.Fatalf("request metadata lost: %#v", permission)
	}
}

func TestNativeToolMetadataPreservesDistinctCallIDs(t *testing.T) {
	for _, itemType := range []string{"dynamicToolCall", "commandExecution", "mcpToolCall", "webSearch", "fileChange"} {
		for _, native := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/native=%t", itemType, native), func(t *testing.T) {
				s := &appServerSession{events: make(chan core.Event, 8), native: appServerNativeOptions{enabled: native}}
				s.threadID.Store("root")
				s.handleNotification("turn/started", json.RawMessage(`{"threadId":"root","turn":{"id":"turn-1"}}`))
				for _, callID := range []string{"call-1", "call-2"} {
					params := map[string]any{
						"threadId": "root", "turnId": "turn-1",
						"item": map[string]any{"type": itemType, "id": callID, "tool": "bots_send", "command": "pwd", "arguments": map[string]any{"botId": "target"}, "status": "completed", "contentItems": []any{}},
					}
					raw, err := json.Marshal(params)
					if err != nil {
						t.Fatal(err)
					}
					s.handleNotification("item/started", raw)
					s.handleNotification("item/completed", raw)
					// Legacy Patch has no result mapping; retain that existing contract.
					types := []core.EventType{core.EventToolUse, core.EventToolResult}
					if itemType == "fileChange" {
						types = types[:1]
					}
					for _, typ := range types {
						event := <-s.events
						if event.Type != typ || itemType == "dynamicToolCall" && event.ToolName != "bots_send" {
							t.Fatalf("wrong tool event: %#v", event)
						}
						if native {
							if event.Metadata["toolCallId"] != callID || event.Metadata["threadId"] != "root" || event.Metadata["turnId"] != "turn-1" {
								t.Fatalf("call identity lost or overwritten: %#v", event.Metadata)
							}
						} else if event.Metadata != nil {
							t.Fatalf("legacy tool metadata changed: %#v", event.Metadata)
						}
					}
				}
			})
		}
	}
}

func TestLegacyManagedSessionStillOptsOutOfTextDeltas(t *testing.T) {
	root, requests := nativeTestManagedServer(t)
	s, err := newAppServerSession(context.Background(), "managed://", t.TempDir(), "gpt-6-sol", "max", "yolo", "", "", "", nil, root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	initialize := takeNativeTestRequest(t, requests, "initialize")
	capabilities := initialize.Params["capabilities"].(map[string]any)
	optOut, ok := capabilities["optOutNotificationMethods"].([]any)
	if !ok {
		t.Fatal("legacy client no longer opts out of streaming deltas")
	}
	var found bool
	for _, method := range optOut {
		found = found || method == "item/agentMessage/delta"
	}
	if !found {
		t.Fatal("legacy message stream changed")
	}
}

func TestNativeExplicitUnixEndpointDoesNotAttachSharedSocketOrSpawnCLI(t *testing.T) {
	root, requests := nativeTestManagedServer(t)
	sharedHome, sharedRequests := nativeTestManagedServer(t)
	endpoint := "unix://" + filepath.Join(root, "app-server-control", "app-server-control.sock")
	base, err := New(map[string]any{
		"cmd": os.Args[0], "backend": "app_server", "native_events": true,
		"app_server_url": endpoint, "codex_home": sharedHome, "work_dir": t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := base.StartSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	adapter := session.(*appServerSession)
	if adapter.cmd != nil || !adapter.attachedTransport {
		t.Fatal("explicit endpoint spawned a per-session Codex process")
	}
	takeNativeTestRequest(t, requests, "initialize")
	takeNativeTestRequest(t, requests, "thread/start")
	select {
	case request := <-sharedRequests:
		t.Fatalf("explicit endpoint silently used shared CODEX_HOME socket: %#v", request)
	default:
	}
	if err := session.Send("fake prompt", "message-1", nil, nil); err != nil {
		t.Fatal(err)
	}
	takeNativeTestRequest(t, requests, "turn/start")
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	// Detaching a bot only closes its own connection, not the dedicated daemon.
	reopened, err := base.StartSession(context.Background(), "our-thread")
	if err != nil {
		t.Fatalf("session Close damaged explicit daemon: %v", err)
	}
	defer reopened.Close()
}

func TestNativeExplicitWebSocketEndpointUsesExistingServer(t *testing.T) {
	methods := make(chan string, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			var request struct {
				ID     any    `json:"id"`
				Method string `json:"method"`
			}
			if err := conn.ReadJSON(&request); err != nil {
				return
			}
			methods <- request.Method
			if request.ID != nil {
				if err := conn.WriteJSON(map[string]any{"id": request.ID, "result": map[string]any{"protocolVersion": "2"}}); err != nil {
					return
				}
			}
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	s := &appServerSession{ctx: ctx, cancel: cancel, url: "ws" + strings.TrimPrefix(server.URL, "http"), native: appServerNativeOptions{enabled: true}, events: make(chan core.Event, 8)}
	s.alive.Store(true)
	if err := s.connect(); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.initialize(); err != nil {
		t.Fatal(err)
	}
	if s.cmd != nil || !s.attachedTransport {
		t.Fatal("WebSocket endpoint spawned a CLI instead of attaching")
	}
	if method := <-methods; method != "initialize" {
		t.Fatalf("method = %q, want initialize", method)
	}
}

func TestNativeMissingExplicitUnixSocketNeverStartsFallbackProcess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	s := &appServerSession{
		ctx: ctx, url: "unix://" + filepath.Join(t.TempDir(), "missing.sock"),
		native: appServerNativeOptions{enabled: true},
	}
	if err := s.connect(); err == nil || s.cmd != nil || s.attachedTransport {
		t.Fatalf("missing explicit socket started fallback: err=%v cmd=%v", err, s.cmd)
	}
}

type nativeSignalWriter struct {
	writes chan nativeTestMessage
}

func (w *nativeSignalWriter) Write(data []byte) (int, error) {
	var message nativeTestMessage
	if err := json.Unmarshal(data, &message); err != nil {
		return 0, err
	}
	w.writes <- message
	return len(data), nil
}
func (w *nativeSignalWriter) Close() error { return nil }

type nativeInlineWriter struct {
	write func([]byte) (int, error)
}

func (w *nativeInlineWriter) Write(data []byte) (int, error) { return w.write(data) }
func (w *nativeInlineWriter) Close() error                   { return nil }

func TestNativeSendResponseCannotOverwriteNewerTurnLifecycle(t *testing.T) {
	for _, outcome := range []string{"running", "completed", "goal_continuation"} {
		t.Run(outcome, func(t *testing.T) {
			s := &appServerSession{
				ctx: context.Background(), workDir: t.TempDir(),
				events: make(chan core.Event, 16),
				native: appServerNativeOptions{enabled: true},
			}
			s.alive.Store(true)
			s.threadID.Store("root")
			notify := func(method, params string) { s.handleNotification(method, json.RawMessage(params)) }
			s.stdin = &nativeInlineWriter{write: func(data []byte) (int, error) {
				var request struct {
					ID     int64  `json:"id"`
					Method string `json:"method"`
				}
				if err := json.Unmarshal(data, &request); err != nil {
					return 0, err
				}
				if request.Method != "turn/start" {
					return 0, fmt.Errorf("unexpected method %s", request.Method)
				}
				notify("turn/started", `{"threadId":"root","turn":{"id":"first"}}`)
				notify("item/completed", `{"threadId":"root","turnId":"first","item":{"type":"agentMessage","text":"First answer."}}`)
				s.handleResponse(rpcResponseEnvelope{ID: request.ID, Result: json.RawMessage(`{"turn":{"id":"first"}}`)})
				// Hold the write until these lifecycle events are handled, forcing
				// the response to reach Send only after its provider state is old.
				if outcome != "running" {
					notify("turn/completed", `{"threadId":"root","turn":{"id":"first","status":"completed"}}`)
				}
				if outcome == "goal_continuation" {
					notify("turn/started", `{"threadId":"root","turn":{"id":"second"}}`)
					notify("item/completed", `{"threadId":"root","turnId":"second","item":{"type":"agentMessage","text":"Second answer."}}`)
				}
				return len(data), nil
			}}
			if err := s.Send("hello", "message-1", nil, nil); err != nil {
				t.Fatal(err)
			}
			s.stateMu.Lock()
			current, pending := s.currentTurn, append([]string(nil), s.pendingMsgs...)
			s.stateMu.Unlock()
			switch outcome {
			case "running":
				if current != "first" || len(pending) != 1 || pending[0] != "First answer." {
					t.Fatalf("response erased live turn output: turn=%q pending=%v", current, pending)
				}
				notify("turn/completed", `{"threadId":"root","turn":{"id":"first","status":"completed"}}`)
			case "completed":
				if current != "" || len(pending) != 0 {
					t.Fatalf("response resurrected completed turn: turn=%q pending=%v", current, pending)
				}
			case "goal_continuation":
				if current != "second" || len(pending) != 1 || pending[0] != "Second answer." {
					t.Fatalf("response overwrote goal continuation: turn=%q pending=%v", current, pending)
				}
				notify("turn/completed", `{"threadId":"root","turn":{"id":"second","status":"completed"}}`)
			}
			for _, turn := range []struct{ id, text string }{{"first", "First answer."}, {"second", "Second answer."}} {
				if turn.id == "second" && outcome != "goal_continuation" {
					break
				}
				text, result := <-s.events, <-s.events
				if text.Type != core.EventText || text.Content != turn.text || text.Metadata["turnId"] != turn.id || result.Type != core.EventResult || result.Metadata["turnId"] != turn.id {
					t.Fatalf("turn events lost: text=%#v result=%#v", text, result)
				}
			}
		})
	}
}

func TestDynamicToolMayIssueRPCWithoutBlockingReadLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r, input := io.Pipe()
	output := &nativeSignalWriter{writes: make(chan nativeTestMessage, 8)}
	s := &appServerSession{ctx: ctx, cancel: cancel, stdin: output, events: make(chan core.Event, 8)}
	s.threadID.Store("root")
	s.native.toolHandler = func(ctx context.Context, call core.DynamicToolCall) (core.DynamicToolResult, error) {
		var response map[string]any
		if err := s.RPC(ctx, "model/list", map[string]any{}, &response); err != nil {
			return core.DynamicToolResult{}, err
		}
		return core.DynamicToolResult{Success: true, ContentItems: []map[string]any{{"type": "inputText", "text": response["model"].(string)}}}, nil
	}
	s.wg.Add(1)
	go s.readLoop(r)
	t.Cleanup(func() {
		cancel()
		_ = input.Close()
		_ = r.Close()
		_ = s.Close()
	})
	if _, err := io.WriteString(input, `{"id":"tool-1","method":"item/tool/call","params":{"threadId":"root","turnId":"turn-1","callId":"call-1","tool":"models","arguments":{}}}`+"\n"); err != nil {
		t.Fatal(err)
	}
	request := takeNativeTestRequest(t, output.writes, "model/list")
	if _, err := fmt.Fprintf(input, "{\"id\":%s,\"result\":{\"model\":\"gpt-6-sol\"}}\n", request.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case response := <-output.writes:
		if string(response.ID) != `"tool-1"` || !strings.Contains(string(response.Result), `"success":true`) || !strings.Contains(string(response.Result), "gpt-6-sol") {
			t.Fatalf("dynamic tool response = %#v", response)
		}
	case <-time.After(time.Second):
		t.Fatal("read loop deadlocked while a dynamic tool waited for RPC")
	}
}

func TestAppServerRPCCancellationCleansPendingRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := &nativeSignalWriter{writes: make(chan nativeTestMessage, 1)}
	s := &appServerSession{stdin: output}
	done := make(chan error, 1)
	go func() { done <- s.RPC(ctx, "thread/goal/get", map[string]any{"threadId": "root"}, nil) }()
	request := takeNativeTestRequest(t, output.writes, "thread/goal/get")
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RPC cancellation = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("RPC did not honour caller cancellation")
	}
	s.pendingMu.Lock()
	n := len(s.pending)
	s.pendingMu.Unlock()
	if n != 0 {
		t.Fatalf("cancelled RPC retained %d pending requests", n)
	}
	var id int64
	if err := json.Unmarshal(request.ID, &id); err != nil {
		t.Fatal(err)
	}
	s.handleResponse(rpcResponseEnvelope{ID: id, Result: json.RawMessage(`{"goal":null}`)})
}

func TestAppServerRPCSettingsUpdateChangesNextTurnOverrides(t *testing.T) {
	output := &nativeSignalWriter{writes: make(chan nativeTestMessage, 1)}
	s := &appServerSession{stdin: output, model: "gpt-6-sol", effort: "max", workDir: "/original"}
	s.threadID.Store("root")
	done := make(chan error, 1)
	go func() {
		done <- s.RPC(context.Background(), "thread/settings/update", map[string]any{
			"threadId": "root", "model": "gpt-6-astra", "effort": "ultra", "cwd": "/changed",
		}, nil)
	}()
	request := takeNativeTestRequest(t, output.writes, "thread/settings/update")
	var id int64
	if err := json.Unmarshal(request.ID, &id); err != nil {
		t.Fatal(err)
	}
	s.handleResponse(rpcResponseEnvelope{ID: id, Result: json.RawMessage(`{}`)})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if s.GetModel() != "gpt-6-astra" || s.GetReasoningEffort() != "ultra" || s.GetWorkDir() != "/changed" {
		t.Fatalf("settings RPC left stale Send overrides: %s %s %s", s.GetModel(), s.GetReasoningEffort(), s.GetWorkDir())
	}
}

func TestAppServerRPCDeadlineAbortsBlockedWrite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	output := newBlockingWriteCloser()
	defer output.Close()
	s := &appServerSession{stdin: output}
	if err := s.RPC(ctx, "thread/goal/get", map[string]any{"threadId": "root"}, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked RPC deadline = %v", err)
	}
	if !output.Closed() {
		t.Fatal("cancelled write could still arrive after RPC returned")
	}
}

func TestDynamicToolTimeoutCancelsHandlerAndReturnsFailure(t *testing.T) {
	output := &nativeSignalWriter{writes: make(chan nativeTestMessage, 1)}
	cancelled := make(chan struct{})
	s := &appServerSession{stdin: output, native: appServerNativeOptions{toolTimeout: 10 * time.Millisecond}}
	s.native.toolHandler = func(ctx context.Context, call core.DynamicToolCall) (core.DynamicToolResult, error) {
		<-ctx.Done()
		close(cancelled)
		return core.DynamicToolResult{}, ctx.Err()
	}
	s.runDynamicTool(json.RawMessage(`"tool-timeout"`), json.RawMessage(`{"threadId":"root","callId":"call-1","tool":"wait","arguments":{}}`))
	select {
	case response := <-output.writes:
		if !strings.Contains(string(response.Result), `"success":false`) || !strings.Contains(string(response.Result), "deadline exceeded") {
			t.Fatalf("timeout response = %#v", response)
		}
	case <-time.After(time.Second):
		t.Fatal("dynamic tool timeout did not return a result")
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("dynamic tool handler was not cancelled")
	}
	s.wg.Wait()
}

func TestNativeAgentOptionsAndToolDefinitionsAreCopied(t *testing.T) {
	a := &Agent{}
	tools := []map[string]any{{"name": "list", "inputSchema": map[string]any{"type": "object"}}}
	a.SetDynamicTools(tools, nil)
	tools[0]["inputSchema"].(map[string]any)["type"] = "string"
	if a.appServerNative.tools[0]["inputSchema"].(map[string]any)["type"] != "object" {
		t.Fatal("caller mutation changed tool schema")
	}
	for _, input := range []string{"ultra", " ULTRA "} {
		got := normalizeReasoningEffort(input)
		if got != "ultra" {
			t.Fatalf("normalizeReasoningEffort(%q) = %q", input, got)
		}
	}
}

func TestNativePermissionAcceptsDecodedStringRequestID(t *testing.T) {
	approval := make(chan core.PermissionResult, 1)
	s := &appServerSession{pendingApprovals: map[string]chan core.PermissionResult{`"approval-1"`: approval}}
	if err := s.RespondPermission("approval-1", core.PermissionResult{Behavior: "allow"}); err != nil {
		t.Fatal(err)
	}
	if result := <-approval; result.Behavior != "allow" {
		t.Fatalf("permission result = %#v", result)
	}
	if err := s.RespondPermission(`"approval-1"`, core.PermissionResult{Behavior: "deny"}); err != nil {
		t.Fatal(err)
	}
	if result := <-approval; result.Behavior != "deny" {
		t.Fatalf("legacy permission result = %#v", result)
	}
}
