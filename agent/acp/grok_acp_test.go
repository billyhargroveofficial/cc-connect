package acp

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestAgent_ReasoningEffortAndCompressCommand(t *testing.T) {
	aDefault, err := New(map[string]any{"cmd": "true"})
	if err != nil {
		t.Fatal(err)
	}
	agDefault := aDefault.(*Agent)
	if got := agDefault.GetReasoningEffort(); got != "" {
		t.Fatalf("default reasoning effort = %q, want empty", got)
	}
	if got := agDefault.CompressCommand(); got != "" {
		t.Fatalf("default compress command = %q, want empty", got)
	}

	a, err := New(map[string]any{
		"cmd":              "true",
		"model":            "grok-4.6",
		"reasoning_effort": "high",
		"compress_command": "/compact",
	})
	if err != nil {
		t.Fatal(err)
	}
	ag := a.(*Agent)
	if got := ag.GetReasoningEffort(); got != "high" {
		t.Fatalf("GetReasoningEffort = %q, want high", got)
	}
	if got := ag.CompressCommand(); got != "/compact" {
		t.Fatalf("CompressCommand = %q, want /compact", got)
	}
	ag.SetReasoningEffort("medium")
	if got := ag.GetReasoningEffort(); got != "medium" {
		t.Fatalf("after SetReasoningEffort = %q, want medium", got)
	}
	if got := ag.AvailableReasoningEfforts(); len(got) == 0 {
		t.Fatal("AvailableReasoningEfforts returned empty slice")
	}

	opts := ag.WorkspaceAgentOptions()
	if got, _ := opts["model"].(string); got != "grok-4.6" {
		t.Fatalf("opts[model] = %q, want grok-4.6", got)
	}
	if got, _ := opts["reasoning_effort"].(string); got != "medium" {
		t.Fatalf("opts[reasoning_effort] = %q, want medium", got)
	}
	if got, _ := opts["compress_command"].(string); got != "/compact" {
		t.Fatalf("opts[compress_command] = %q, want /compact", got)
	}
}

func TestMapSessionUpdate_IgnoresMetadataUpdates(t *testing.T) {
	kinds := []string{
		"session_info_update",
		"available_commands_update",
		"current_mode_update",
		"usage_update",
		"config_option_update",
	}
	for _, kind := range kinds {
		t.Run(kind, func(t *testing.T) {
			params := json.RawMessage(`{
				"sessionId": "s1",
				"update": {
					"sessionUpdate": "` + kind + `",
					"title": "should not leak",
					"text": "should not leak",
					"message": "should not leak"
				}
			}`)
			evs := mapSessionUpdate("s1", params)
			if len(evs) != 0 {
				t.Fatalf("kind %q produced events %+v, want nil", kind, evs)
			}
		})
	}
}

func TestSummarizeACPToolInput_SearchAndBackend(t *testing.T) {
	if got := summarizeACPToolInput("search", json.RawMessage(`{"query":"grok 4.6"}`)); got != "grok 4.6" {
		t.Fatalf("search query = %q, want %q", got, "grok 4.6")
	}
	if got := summarizeACPToolInput("search", json.RawMessage(`{"variant":"XSearch","backend":true}`)); got != "" {
		t.Fatalf("backend search = %q, want empty", got)
	}
	if got := summarizeACPToolInput("", json.RawMessage(`{"variant":"XSearch","backend":true}`)); got != "" {
		t.Fatalf("backend fallback = %q, want empty", got)
	}

	params := json.RawMessage(`{
		"sessionId": "s1",
		"update": {
			"sessionUpdate": "tool_call",
			"toolCallId": "call-x1",
			"title": "X Search",
			"kind": "search",
			"rawInput": {"variant": "XSearch", "backend": true}
		}
	}`)
	evs := mapSessionUpdate("s1", params)
	if len(evs) != 1 || evs[0].Type != core.EventToolUse {
		t.Fatalf("got %+v", evs)
	}
	if evs[0].ToolName != "X Search" || evs[0].ToolInput != "X Search" {
		t.Fatalf("got tool=%q input=%q, want X Search / X Search", evs[0].ToolName, evs[0].ToolInput)
	}
}

type fakeACPServer struct {
	mu           sync.Mutex
	lastPrompt   map[string]any
	promptResp   map[string]any
	promptCount  int
	lastConfigID string
	lastConfigV  string
}

func newMockACPSession(t *testing.T, workDir string, supportsImage bool, srv *fakeACPServer) (*acpSession, func()) {
	t.Helper()
	clientR, serverW := io.Pipe()
	serverR, clientW := io.Pipe()

	ctx, cancel := context.WithCancel(context.Background())
	s := &acpSession{
		workDir:       workDir,
		events:        make(chan core.Event, 64),
		ctx:           ctx,
		cancel:        cancel,
		permByID:      make(map[string]permState),
		toolInputByID: make(map[string]string),
	}
	s.alive.Store(true)
	s.tr = newTransport(clientR, clientW, s.onNotification, s.onServerRequest)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.tr.readLoop(ctx)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(serverR)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		writeResp := func(id json.RawMessage, result any) {
			b, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result":  result,
			})
			_, _ = serverW.Write(append(b, '\n'))
		}
		for sc.Scan() {
			var req struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if json.Unmarshal(sc.Bytes(), &req) != nil || len(req.ID) == 0 {
				continue
			}
			switch req.Method {
			case "initialize":
				writeResp(req.ID, map[string]any{
					"protocolVersion": 1,
					"agentCapabilities": map[string]any{
						"loadSession": true,
						"promptCapabilities": map[string]any{
							"image": supportsImage,
						},
					},
				})
			case "authenticate":
				writeResp(req.ID, map[string]any{})
			case "session/new":
				writeResp(req.ID, map[string]any{
					"sessionId": "grok-sess-1",
					"models": map[string]any{
						"currentModelId": "grok-4.7",
						"availableModels": []map[string]any{
							{
								"modelId": "grok-4.7",
								"name":    "Grok 4.7",
								"_meta":   map[string]any{"totalContextTokens": 256000, "reasoningEffort": "high"},
							},
							{
								"modelId": "grok-4.6",
								"name":    "Grok 4.6",
								"_meta":   map[string]any{"totalContextTokens": 256000, "reasoningEffort": "high"},
							},
						},
					},
				})
			case "session/set_model":
				writeResp(req.ID, map[string]any{"_meta": map[string]any{"model": map[string]any{"Ok": "grok-4.6"}}})
			case "session/set_config_option":
				var p struct {
					ConfigID string `json:"configId"`
					Value    string `json:"value"`
				}
				_ = json.Unmarshal(req.Params, &p)
				srv.mu.Lock()
				srv.lastConfigID = p.ConfigID
				srv.lastConfigV = p.Value
				srv.mu.Unlock()
				writeResp(req.ID, map[string]any{"configOptions": []any{}})
			case "session/prompt":
				var p map[string]any
				_ = json.Unmarshal(req.Params, &p)
				srv.mu.Lock()
				srv.lastPrompt = p
				srv.promptCount++
				resp := srv.promptResp
				srv.mu.Unlock()
				if resp == nil {
					resp = map[string]any{"stopReason": "end_turn"}
				}
				writeResp(req.ID, resp)
			}
		}
	}()

	if err := s.handshake("", "cached_token"); err != nil {
		t.Fatalf("handshake failed: %v", err)
	}
	if !s.applyModel("grok-4.6") {
		t.Fatal("applyModel(grok-4.6) failed")
	}
	if !s.applyReasoningEffort("xhigh") {
		t.Fatal("applyReasoningEffort(xhigh) failed")
	}
	srv.mu.Lock()
	gotCID, gotCV := srv.lastConfigID, srv.lastConfigV
	srv.mu.Unlock()
	if gotCID != "reasoning_effort" || gotCV != "xhigh" {
		t.Fatalf("set_config_option got (%q, %q), want (reasoning_effort, xhigh)", gotCID, gotCV)
	}

	cleanup := func() {
		s.alive.Store(false)
		cancel()
		_ = clientW.Close()
		_ = serverW.Close()
		_ = clientR.Close()
		_ = serverR.Close()
		wg.Wait()
	}
	return s, cleanup
}

func TestSession_PromptUsageImagesAndCompactReset(t *testing.T) {
	workDir := t.TempDir()
	srv := &fakeACPServer{
		promptResp: map[string]any{
			"stopReason": "end_turn",
			"_meta": map[string]any{
				"modelId":             "grok-4.6",
				"inputTokens":         40430,
				"outputTokens":        120,
				"totalTokens":         40550,
				"cachedReadTokens":    39000,
				"cacheCreationTokens": 0,
			},
		},
	}
	s, cleanup := newMockACPSession(t, workDir, true, srv)
	defer cleanup()

	imgBytes := []byte("fake-png-bytes")
	err := s.Send("check this image", "m1", []core.ImageAttachment{{
		MimeType: "image/png",
		Data:     imgBytes,
	}}, nil)
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	// Verify native image block + saved file in .cc-connect/images.
	srv.mu.Lock()
	promptBlocks, _ := srv.lastPrompt["prompt"].([]any)
	srv.mu.Unlock()
	if len(promptBlocks) != 2 {
		t.Fatalf("expected 2 prompt blocks (text + image), got %d: %#v", len(promptBlocks), promptBlocks)
	}
	textBlock := promptBlocks[0].(map[string]any)
	textStr := textBlock["text"].(string)
	if !strings.Contains(textStr, "[attached image files: ") || !strings.Contains(textStr, filepath.Join(".cc-connect", "images")) {
		t.Fatalf("text prompt missing saved image ref: %q", textStr)
	}
	imgFiles, err := filepath.Glob(filepath.Join(workDir, ".cc-connect", "images", "img_*.png"))
	if err != nil || len(imgFiles) != 1 {
		t.Fatalf("expected 1 saved image in .cc-connect/images, got %v (err=%v)", imgFiles, err)
	}
	saved, err := os.ReadFile(imgFiles[0])
	if err != nil || string(saved) != string(imgBytes) {
		t.Fatalf("saved image content mismatch: %q", string(saved))
	}
	imgBlock := promptBlocks[1].(map[string]any)
	if imgBlock["type"] != "image" || imgBlock["mimeType"] != "image/png" || imgBlock["data"] != base64.StdEncoding.EncodeToString(imgBytes) {
		t.Fatalf("unexpected native image block: %#v", imgBlock)
	}

	// Verify ContextUsageReporter and EventResult tokens prefer totalTokens
	// (real session context) over inputTokens (cumulative across tool steps).
	usage := s.GetContextUsage()
	if usage == nil {
		t.Fatal("GetContextUsage() = nil, want populated usage")
	}
	if usage.UsedTokens != 40550 || usage.ContextWindow != 256000 || usage.CachedInputTokens != 39000 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
	ev := <-s.Events()
	if ev.Type != core.EventResult || ev.InputTokens != 40550 || ev.OutputTokens != 120 || !ev.Done {
		t.Fatalf("unexpected result event: %+v", ev)
	}

	// Simulate multi-step server-side XSearch turn where inputTokens sums across
	// internal tool calls (227073) while totalTokens is actual context (38506).
	srv.mu.Lock()
	srv.promptResp = map[string]any{
		"stopReason": "end_turn",
		"_meta": map[string]any{
			"modelId":          "grok-4.6",
			"inputTokens":      227073,
			"outputTokens":     2824,
			"totalTokens":      38506,
			"cachedReadTokens": 172544,
		},
	}
	srv.mu.Unlock()
	if err := s.Send("search x", "m1b", nil, nil); err != nil {
		t.Fatalf("Send xsearch failed: %v", err)
	}
	<-s.Events()
	if u := s.GetContextUsage(); u == nil || u.UsedTokens != 38506 || u.ContextWindow != 256000 {
		t.Fatalf("xsearch usage = %+v, want UsedTokens=38506 ContextWindow=256000", u)
	}

	// Verify /compact with 0 tokens clears cached context usage.
	srv.mu.Lock()
	srv.promptResp = map[string]any{
		"stopReason": "end_turn",
		"_meta": map[string]any{
			"totalTokens": 0,
		},
	}
	srv.mu.Unlock()
	if err := s.Send("/compact", "", nil, nil); err != nil {
		t.Fatalf("/compact Send failed: %v", err)
	}
	<-s.Events()
	if got := s.GetContextUsage(); got != nil {
		t.Fatalf("GetContextUsage() after /compact = %+v, want nil", got)
	}
}

func TestSession_NoNativeImageBlockWhenUnsupported(t *testing.T) {
	workDir := t.TempDir()
	srv := &fakeACPServer{}
	s, cleanup := newMockACPSession(t, workDir, false, srv)
	defer cleanup()

	err := s.Send("desc", "m2", []core.ImageAttachment{{
		MimeType: "image/jpeg",
		Data:     []byte("fake-jpg"),
	}}, nil)
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}
	srv.mu.Lock()
	promptBlocks, _ := srv.lastPrompt["prompt"].([]any)
	srv.mu.Unlock()
	if len(promptBlocks) != 1 {
		t.Fatalf("expected only 1 text prompt block when supportsImage=false, got %d", len(promptBlocks))
	}
}
