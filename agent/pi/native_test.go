package pi

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

type nativeRPCWriter struct {
	commands chan map[string]any
}

func (w nativeRPCWriter) Write(data []byte) (int, error) {
	var command map[string]any
	if err := json.Unmarshal(data, &command); err != nil {
		return 0, err
	}
	w.commands <- command
	return len(data), nil
}

func (w nativeRPCWriter) Close() error { return nil }

func nativeRPCFixture(t *testing.T) (*piSession, <-chan map[string]any) {
	t.Helper()
	s := newTestSession(true)
	w := nativeRPCWriter{commands: make(chan map[string]any, 8)}
	s.rpcStdin = w
	t.Cleanup(func() { _ = s.Close() })
	return s, w.commands
}

func receiveNativeCommand(t *testing.T, commands <-chan map[string]any) map[string]any {
	t.Helper()
	select {
	case command := <-commands:
		return command
	case <-time.After(3 * time.Second):
		t.Fatal("RPC did not write a command")
		return nil
	}
}

func TestNativeToolEventsKeepActualCallIdentityAndLegacyMetadataNil(t *testing.T) {
	for _, native := range []bool{false, true} {
		s := newTestSession(false)
		s.nativeEvents = native
		calls := []map[string]any{
			{"toolCall": map[string]any{"type": "toolCall", "id": "read-one", "name": "read", "arguments": map[string]any{"path": "one"}}},
			{"message": map[string]any{"content": []any{map[string]any{"type": "toolCall", "id": "read-two", "name": "read", "arguments": map[string]any{"path": "two"}}}}, "contentIndex": float64(0)},
		}
		for i, call := range calls {
			id := []string{"read-one", "read-two"}[i]
			s.emitToolFromMessage(call)
			s.handleMessageEnd(map[string]any{"message": map[string]any{"role": "toolResult", "toolName": "read", "toolCallId": id, "content": []any{map[string]any{"type": "text", "text": "result"}}}})
			for range 2 {
				event := <-s.events
				if native && event.Metadata["toolCallId"] != id {
					t.Fatalf("same-name tool lost native identity: %+v", event)
				}
				if !native && event.Metadata != nil {
					t.Fatalf("legacy event metadata changed: %+v", event)
				}
			}
		}
		_ = s.Close()
	}
}

func TestNativeRPC_CorrelatesOutOfOrderResponses(t *testing.T) {
	s, commands := nativeRPCFixture(t)
	errors := make(chan error, 2)
	results := make([]map[string]string, 2)
	for i := range results {
		go func(i int) {
			errors <- s.RPC(context.Background(), "get_state", map[string]any{"marker": fmt.Sprint(i)}, &results[i])
		}(i)
	}
	first, second := receiveNativeCommand(t, commands), receiveNativeCommand(t, commands)
	if first["id"] == second["id"] {
		t.Fatal("concurrent RPCs share a request id")
	}
	for _, command := range []map[string]any{second, first} {
		s.handleEvent(map[string]any{
			"type": "response", "id": command["id"], "command": "get_state", "success": true,
			"data": map[string]any{"marker": command["marker"]},
		})
	}
	for range results {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
	for i, result := range results {
		if result["marker"] != fmt.Sprint(i) {
			t.Errorf("RPC %d received another call's response: %v", i, result)
		}
	}
	s.rpcMu.Lock()
	defer s.rpcMu.Unlock()
	if len(s.rpcPending) != 0 {
		t.Fatal("completed requests remain pending")
	}
}

func TestNativeRPC_FailureAndCancellationCleanPendingRequests(t *testing.T) {
	s, commands := nativeRPCFixture(t)
	errs := make(chan error, 1)
	go func() { errs <- s.RPC(context.Background(), "set_model", nil, nil) }()
	command := receiveNativeCommand(t, commands)
	s.handleEvent(map[string]any{
		"type": "response", "id": command["id"], "command": "set_model", "success": false,
		"error": "Model not found",
	})
	if err := <-errs; err == nil || !strings.Contains(err.Error(), "Model not found") {
		t.Fatalf("command failure was lost: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { errs <- s.RPC(ctx, "get_state", nil, nil) }()
	late := receiveNativeCommand(t, commands)
	cancel()
	if err := <-errs; !errors.Is(err, context.Canceled) {
		t.Fatalf("RPC did not observe caller cancellation: %v", err)
	}
	// A response after cancellation must be harmless and must not resurrect it.
	s.handleEvent(map[string]any{"type": "response", "id": late["id"], "success": true})
	s.rpcMu.Lock()
	defer s.rpcMu.Unlock()
	if len(s.rpcPending) != 0 {
		t.Fatalf("failed/cancelled calls remain pending: %v", s.rpcPending)
	}
}

func TestNativeRPC_CloseUnblocksRequestsAndIsIdempotent(t *testing.T) {
	s, commands := nativeRPCFixture(t)
	errs := make(chan error, 1)
	go func() { errs <- s.RPC(context.Background(), "get_state", nil, nil) }()
	receiveNativeCommand(t, commands)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errs:
		if err == nil {
			t.Fatal("closed session reported RPC success")
		}
	case <-time.After(time.Second):
		t.Fatal("Close left an RPC waiter blocked")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.RPC(context.Background(), "get_state", nil, nil); err == nil {
		t.Fatal("RPC accepted a command after Close")
	}
}

func TestNativeRPC_ReservedFieldsCannotChangeCommand(t *testing.T) {
	s, commands := nativeRPCFixture(t)
	params := map[string]any{"type": "wrong", "id": "shared", "level": "max"}
	errs := make(chan error, 1)
	go func() { errs <- s.RPC(context.Background(), "set_thinking_level", params, nil) }()
	command := receiveNativeCommand(t, commands)
	if command["type"] != "set_thinking_level" || command["id"] == "shared" || command["level"] != "max" {
		t.Fatalf("bad RPC command: %v", command)
	}
	s.handleEvent(map[string]any{"type": "response", "id": command["id"], "success": true})
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if params["id"] != "shared" || params["type"] != "wrong" {
		t.Fatal("RPC modified caller-owned params")
	}
	if err := s.RPC(context.Background(), "get_state", []string{"bad"}, nil); err == nil {
		t.Fatal("RPC accepted non-object params")
	}
}

func TestNativeRPC_ExtensionUIResponseIsOneWayWithOriginalID(t *testing.T) {
	s, commands := nativeRPCFixture(t)
	if err := s.RPC(context.Background(), "extension_ui_response", map[string]any{"id": "extension-dialog-id", "value": "edited text"}, nil); err != nil {
		t.Fatal(err)
	}
	command := receiveNativeCommand(t, commands)
	if command["id"] != "extension-dialog-id" || command["value"] != "edited text" || command["type"] != "extension_ui_response" {
		t.Fatalf("UI response lost its extension request ID: %v", command)
	}
	if err := s.RPC(context.Background(), "extension_ui_response", map[string]any{"value": "missing id"}, nil); err == nil {
		t.Fatal("invalid UI response was sent without an extension request ID")
	}
	s.rpcMu.Lock()
	defer s.rpcMu.Unlock()
	if len(s.rpcPending) != 0 {
		t.Fatal("one-way UI response created a response waiter")
	}
}

func TestNativeSend_AttachesImageContentAndSurfacesPromptRejection(t *testing.T) {
	s, commands := nativeRPCFixture(t)
	s.nativeEvents = true
	s.attachDir = t.TempDir()
	imageData := []byte("\x89PNG\r\n\x1a\nimage bytes")
	errs := make(chan error, 1)
	go func() {
		errs <- s.Send("describe", "image-message", []core.ImageAttachment{{MimeType: "image/png", Data: imageData}}, nil)
	}()
	command := receiveNativeCommand(t, commands)
	images, _ := command["images"].([]any)
	if command["message"] != "describe" || len(images) != 1 {
		t.Fatalf("RPC vision input was replaced by a text path: %v", command)
	}
	image, _ := images[0].(map[string]any)
	if image["type"] != "image" || image["mimeType"] != "image/png" || image["data"] != base64.StdEncoding.EncodeToString(imageData) {
		t.Fatalf("image content did not reach the RPC prompt: %v", image)
	}
	s.handleEvent(map[string]any{
		"type": "response", "id": command["id"], "command": "prompt", "success": false, "error": "agent is busy",
	})
	if err := <-errs; err == nil || !strings.Contains(err.Error(), "agent is busy") {
		t.Fatalf("Send ignored Pi's prompt rejection: %v", err)
	}
}

func TestNativeObserver_PreservesUnknownEventsAndFullToolOutput(t *testing.T) {
	s := newTestSession()
	defer s.cancel()
	var observed []core.NativeEvent
	s.nativeHandler = func(event core.NativeEvent) { observed = append(observed, event) }
	output := strings.Repeat("полный результат ", 200)
	raw := map[string]any{
		"type": "tool_execution_end", "toolCallId": "tool-1", "toolName": "Agent", "isError": false,
		"result": map[string]any{
			"content": []any{map[string]any{"type": "text", "text": output}},
			"details": map[string]any{"agentId": "child-1", "status": "completed", "tokens": 720.0},
		},
	}
	s.handleEvent(raw)
	s.handleEvent(map[string]any{"type": "future_event", "id": 42.0, "new": "retained"})
	if len(observed) != 2 {
		t.Fatalf("events lost: %v", observed)
	}
	var decoded map[string]any
	if err := json.Unmarshal(observed[0].Params, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, raw) {
		t.Fatal("native event discarded tool output or structured details")
	}
	if observed[0].Backend != "pi" || observed[0].Method != "tool_execution_end" || observed[0].Timestamp.IsZero() {
		t.Fatalf("incomplete event envelope: %+v", observed[0])
	}
	if string(observed[1].RequestID) != "42" || observed[1].Method != "future_event" {
		t.Fatalf("unknown event or numeric request id lost: %+v", observed[1])
	}
	// The snapshot must not share mutable maps with the provider parser.
	raw["result"] = "changed after delivery"
	if strings.Contains(string(observed[0].Params), "changed after delivery") {
		t.Fatal("native event aliases parser-owned data")
	}
}

func TestNativeLifecycle_WaitsForSettledAcrossRetryAndCompaction(t *testing.T) {
	s := newTestSession(true)
	defer s.cancel()
	s.nativeEvents = true
	s.sessionID.Store("persistent")
	s.handleEvent(map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "errorMessage": "transient"}})
	s.handleEvent(map[string]any{"type": "agent_end", "willRetry": true})
	s.handleEvent(map[string]any{"type": "compaction_end", "willRetry": true})
	s.handleEvent(map[string]any{"type": "agent_end", "willRetry": false})
	if events := drainEvents(s); len(events) != 0 {
		t.Fatalf("native run closed before settling: %v", events)
	}
	s.handleEvent(map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant"}})
	s.handleEvent(map[string]any{"type": "agent_settled"})
	events := drainEvents(s)
	if len(events) != 1 || events[0].Type != core.EventResult || !events[0].Done || events[0].SessionID != "persistent" {
		t.Fatalf("native settlement did not close the recovered run once: %v", events)
	}
	legacy := newTestSession(true)
	defer legacy.cancel()
	legacy.handleEvent(map[string]any{"type": "agent_end"})
	legacy.handleEvent(map[string]any{"type": "agent_settled"})
	if events := drainEvents(legacy); len(events) != 1 || events[0].Type != core.EventResult {
		t.Fatalf("legacy agent_end behavior changed: %v", events)
	}
}

func TestNativeLifecycle_DefersTerminalErrorUntilSettled(t *testing.T) {
	s := newTestSession(true)
	defer s.cancel()
	s.nativeEvents = true
	s.handleEvent(map[string]any{"type": "compaction_end", "errorMessage": "summary failed"})
	if events := drainEvents(s); len(events) != 0 {
		t.Fatalf("failure surfaced before Pi finished recovery: %v", events)
	}
	s.handleEvent(map[string]any{"type": "agent_settled"})
	events := drainEvents(s)
	if len(events) != 2 || events[0].Type != core.EventError || events[1].Type != core.EventResult {
		t.Fatalf("terminal failure was not delivered with settlement: %v", events)
	}
}

func TestNativeUsage_ProviderCountsAreCumulativeAndContextFollowsModel(t *testing.T) {
	s := newTestSession(true)
	defer s.cancel()
	s.nativeEvents = true
	s.handleEvent(map[string]any{
		"type": "response", "command": "set_model", "success": true,
		"data": map[string]any{"id": "new-model", "contextWindow": 1_000_000.0},
	})
	for _, output := range []float64{10, 23} {
		s.handleEvent(map[string]any{
			"type": "message_update", "usage": map[string]any{"input": 120.0, "output": output, "cacheRead": 40.0, "cacheWrite": 5.0},
		})
	}
	usage := s.GetContextUsage()
	if usage == nil || usage.OutputTokens != 23 || usage.UsedTokens != 165 || usage.TotalTokens != 188 || usage.ContextWindow != 1_000_000 {
		t.Fatalf("provider cumulative counts were lost or counted twice: %+v", usage)
	}
	s.handleEvent(map[string]any{
		"type": "agent_end", "messages": []any{map[string]any{
			"role": "assistant", "model": "new-model", "usage": map[string]any{"input": 120.0, "output": 30.0, "cacheRead": 40.0, "cacheWrite": 5.0},
		}},
	})
	usage = s.GetContextUsage()
	if usage.OutputTokens != 30 || usage.ContextWindow != 1_000_000 {
		t.Fatalf("final usage discarded the live model's context window: %+v", usage)
	}
}

func TestProductSessionDirectory_ResumeIsStrictAndLegacyUnchanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "2026-10-01_bot-session.jsonl")
	if err := os.WriteFile(path, []byte("{\"type\":\"session\",\"id\":\"bot-session\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &piSession{sessionDir: dir}
	args, err := s.appendSessionArgs([]string{"--mode", "rpc"}, "bot-session")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(args, []string{"--mode", "rpc", "--session-dir", dir, "--session", path}) {
		t.Fatalf("resume must reference the existing file: %v", args)
	}
	for _, id := range []string{"missing", filepath.Join(t.TempDir(), "outside.jsonl"), "../outside.jsonl"} {
		if _, err := s.appendSessionArgs(nil, id); err == nil {
			t.Errorf("accepted a missing or outside session %q", id)
		}
	}
	legacy := &piSession{}
	args, err = legacy.appendSessionArgs(nil, "missing")
	if err != nil || !reflect.DeepEqual(args, []string{"--session-id", "missing"}) {
		t.Fatalf("default resume behavior changed: %v %v", args, err)
	}
	a := &Agent{sessionDir: dir}
	sessions, err := a.ListSessions(context.Background())
	if err != nil || len(sessions) != 1 || sessions[0].ID != "bot-session" {
		t.Fatalf("custom session listing used native global storage: %+v %v", sessions, err)
	}
}

func TestProductSessionDirectory_RejectsOutsideSymlink(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "external.jsonl")
	if err := os.WriteFile(outside, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "2026-10-01_other-session.jsonl")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := strictSessionFile(dir, "other-session"); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("resumed another bot's session through a symlink: %v", err)
	}
}

func TestProductCLIArgs_ValidateAndCopyWithoutShellExpansion(t *testing.T) {
	args := []string{"--append-system-prompt", "path with spaces/AGENTS.md", "--skill", "literal-$(false)`false`"}
	agent, err := New(map[string]any{"cmd": "echo", "cli_args": args, "native_events": true, "rpc": true})
	if err != nil {
		t.Fatal(err)
	}
	a := agent.(*Agent)
	args[1] = "caller changed"
	snapshot := a.WorkspaceAgentOptions()
	if snapshot["native_events"] != true || snapshot["rpc"] != true {
		t.Fatalf("product options lost in workspace reconstruction: %v", snapshot)
	}
	got := snapshot["cli_args"].([]string)
	if got[1] != "path with spaces/AGENTS.md" || got[3] != "literal-$(false)`false`" {
		t.Fatalf("argv changed or expanded: %v", got)
	}
	got[1] = "snapshot changed"
	if a.cliArgs[1] != "path with spaces/AGENTS.md" {
		t.Fatal("snapshot aliases agent argv")
	}
	if _, err := New(map[string]any{"cmd": "echo", "cli_args": []any{"--skill", 3}}); err == nil {
		t.Fatal("invalid argv element accepted")
	}
}

// TestPiNativeRPCProcess runs as an isolated subprocess only in the fixture.
// It never starts Pi or contacts a model provider.
func TestPiNativeRPCProcess(t *testing.T) {
	if os.Getenv("PI_ADAPTER_TEST_HELPER") != "1" {
		return
	}
	encode := json.NewEncoder(os.Stdout)
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var command map[string]any
		if json.Unmarshal(scanner.Bytes(), &command) != nil {
			continue
		}
		if command["type"] == "exit" {
			os.Exit(0)
		}
		if command["type"] == "wait" {
			continue
		}
		data := map[string]any{"argv": os.Args[1:]}
		switch command["type"] {
		case "get_state":
			data["sessionId"] = "native-test-session"
			data["model"] = map[string]any{"id": "deepseek-flash", "provider": "deepseek", "contextWindow": 1_000_000}
		case "get_available_thinking_levels":
			data["levels"] = []string{"off", "low", "high", "max"}
		}
		_ = encode.Encode(map[string]any{"type": "response", "id": command["id"], "command": command["type"], "success": true, "data": data})
	}
	os.Exit(0)
}

func TestNativeStartup_ObserverReceivesProbeAndLiveRPCCapabilities(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	observed := make(chan core.NativeEvent, 16)
	agent, err := New(map[string]any{
		"cmd": executable, "work_dir": t.TempDir(), "rpc": true, "native_events": true,
		"cli_args": []string{"-test.run=TestPiNativeRPCProcess", "--", "--append-system-prompt", "product instructions", "--approve"},
		"env":      map[string]any{"PI_ADAPTER_TEST_HELPER": "1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	agent.(core.NativeEventObserver).SetNativeEventHandler(func(event core.NativeEvent) { observed <- event })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := agent.StartSession(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if session.CurrentSessionID() != "native-test-session" {
		t.Fatal("StartSession returned before the startup probe recorded its ID")
	}
	select {
	case event := <-observed:
		if event.Method != "response" || !strings.Contains(string(event.Params), stateProbeID) {
			t.Fatalf("initial provider response was lost: %+v", event)
		}
	default:
		t.Fatal("observer was installed after process startup")
	}
	var capabilities struct {
		Levels []string `json:"levels"`
		Argv   []string `json:"argv"`
	}
	if err := session.(core.AgentRPCSession).RPC(ctx, "get_available_thinking_levels", nil, &capabilities); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(capabilities.Levels, []string{"off", "low", "high", "max"}) {
		t.Fatalf("live reasoning capabilities lost: %+v", capabilities)
	}
	if !strings.Contains(strings.Join(capabilities.Argv, "|"), "--append-system-prompt|product instructions|--approve|--mode|rpc") {
		t.Fatalf("explicit product argv was not passed literally: %v", capabilities.Argv)
	}
}

func TestNativeRPC_ProcessExitUnblocksWaiters(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := newPiSession(ctx, executable, []string{"-test.run=TestPiNativeRPCProcess", "--"}, t.TempDir(), "", "", "", true, "", []string{"PI_ADAPTER_TEST_HELPER=1"})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	err = session.RPC(ctx, "exit", nil, nil)
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("process death did not promptly fail the pending RPC: %v", err)
	}
}

func TestNativeRPC_RejectsJSONMode(t *testing.T) {
	s := newTestSession()
	defer s.cancel()
	if err := s.RPC(context.Background(), "get_state", nil, nil); err == nil {
		t.Fatal("one-shot JSON session accepted an RPC")
	}
	var _ io.WriteCloser = nativeRPCWriter{}
}
