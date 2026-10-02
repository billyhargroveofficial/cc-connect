package bots

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

type runtimeFakeFactory struct {
	mu           sync.Mutex
	created      []*runtimeFakeAgent
	sends        chan runtimeFakeSend
	strictResume bool
	listErr      error
}

type runtimeFakeSend struct {
	session *runtimeFakeSession
	prompt  string
	images  []core.ImageAttachment
	files   []core.FileAttachment
}
type runtimeFakeAgent struct {
	backend  string
	opts     map[string]any
	observer core.NativeEventHandler
	tools    []map[string]any
	session  *runtimeFakeSession
	resume   string
	starts   int
}
type runtimeFakeSession struct {
	mu              sync.Mutex
	id              string
	agent           *runtimeFakeAgent
	factory         *runtimeFakeFactory
	events          chan core.Event
	alive           atomic.Bool
	closeOnce       sync.Once
	permissions     map[string]core.PermissionResult
	rpcCalls        []string
	rpcParams       []map[string]any
	goal            map[string]any
	terminals       []ownedBackgroundProcess
	terminateFalse  bool
	falseKeepsAlive bool
	sendErr         error
	interruptErr    error
	steerErr        error
	steerEntered    chan struct{}
	steerRelease    chan struct{}
	materialized    atomic.Bool
}

func (f *runtimeFakeFactory) create(backend string, opts map[string]any) (core.Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := &runtimeFakeAgent{backend: backend, opts: opts}
	s := &runtimeFakeSession{id: fmt.Sprintf("thread_%d", len(f.created)+1), agent: a, factory: f, events: make(chan core.Event, 64), permissions: make(map[string]core.PermissionResult)}
	s.alive.Store(true)
	a.session = s
	f.created = append(f.created, a)
	return a, nil
}
func (a *runtimeFakeAgent) Name() string { return a.backend }
func (a *runtimeFakeAgent) StartSession(_ context.Context, resume string) (core.AgentSession, error) {
	f := a.session.factory
	f.mu.Lock()
	defer f.mu.Unlock()
	var prior *runtimeFakeSession
	for _, previous := range f.created {
		if previous != a && previous.backend == a.backend && previous.opts["work_dir"] == a.opts["work_dir"] && previous.session.id == resume && previous.session.materialized.Load() {
			prior = previous.session
			break
		}
	}
	if f.strictResume && resume != "" && prior == nil {
		return nil, fmt.Errorf("native session %s is not materialized", resume)
	}
	a.starts++
	if !a.session.Alive() {
		s := &runtimeFakeSession{id: fmt.Sprintf("thread_%d_fresh_%d", len(f.created), a.starts), agent: a, factory: f, events: make(chan core.Event, 64), permissions: make(map[string]core.PermissionResult)}
		s.alive.Store(true)
		a.session = s
	}
	a.resume = resume
	if resume != "" {
		a.session.id = resume
		if prior != nil {
			prior.mu.Lock()
			if prior.goal != nil {
				a.session.goal = map[string]any{}
				for key, value := range prior.goal {
					a.session.goal[key] = value
				}
			}
			a.session.terminals = append([]ownedBackgroundProcess(nil), prior.terminals...)
			prior.mu.Unlock()
			a.session.materialized.Store(true)
		}
	}
	return a.session, nil
}
func (a *runtimeFakeAgent) ListSessions(context.Context) ([]core.AgentSessionInfo, error) {
	f := a.session.factory
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	result := []core.AgentSessionInfo{}
	for _, previous := range f.created {
		if previous.backend == a.backend && previous.opts["work_dir"] == a.opts["work_dir"] && previous.session.materialized.Load() {
			result = append(result, core.AgentSessionInfo{ID: previous.session.id})
		}
	}
	return result, nil
}
func (a *runtimeFakeAgent) Stop() error { return nil }
func (a *runtimeFakeAgent) SetNativeEventHandler(handler core.NativeEventHandler) {
	a.observer = handler
}
func (a *runtimeFakeAgent) SetDynamicTools(tools []map[string]any, _ core.DynamicToolHandler) {
	a.tools = tools
}
func (s *runtimeFakeSession) Send(prompt, _ string, images []core.ImageAttachment, files []core.FileAttachment) error {
	s.mu.Lock()
	err := s.sendErr
	s.mu.Unlock()
	if err == nil {
		s.materialized.Store(true)
	}
	s.factory.sends <- runtimeFakeSend{session: s, prompt: prompt, images: images, files: files}
	return err
}
func (s *runtimeFakeSession) Events() <-chan core.Event { return s.events }
func (s *runtimeFakeSession) CurrentSessionID() string  { return s.id }
func (s *runtimeFakeSession) Alive() bool               { return s.alive.Load() }
func (s *runtimeFakeSession) Close() error {
	s.closeOnce.Do(func() { s.mu.Lock(); s.alive.Store(false); close(s.events); s.mu.Unlock() })
	return nil
}
func (s *runtimeFakeSession) RespondPermission(id string, result core.PermissionResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.permissions[id] = result
	return nil
}
func (s *runtimeFakeSession) emit(event core.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.alive.Load() {
		s.events <- event
	}
}
func (s *runtimeFakeSession) native(method string, params map[string]any) {
	data, _ := json.Marshal(params)
	s.agent.observer(core.NativeEvent{Backend: s.agent.backend, Method: method, Params: data, RootThreadID: s.id, Timestamp: time.Now()})
}
func (s *runtimeFakeSession) complete(text string) {
	s.emit(core.Event{Type: core.EventText, Content: text})
	s.emit(core.Event{Type: core.EventResult, SessionID: s.id, Done: true})
}
func (s *runtimeFakeSession) RPC(_ context.Context, method string, params any, result any) error {
	s.mu.Lock()
	if method == "turn/steer" || method == "steer" {
		entered, release := s.steerEntered, s.steerRelease
		s.mu.Unlock()
		if entered != nil {
			close(entered)
		}
		if release != nil {
			<-release
		}
		s.mu.Lock()
	}
	defer s.mu.Unlock()
	s.rpcCalls = append(s.rpcCalls, method)
	var fields map[string]any
	encodedParams, _ := json.Marshal(params)
	_ = json.Unmarshal(encodedParams, &fields)
	s.rpcParams = append(s.rpcParams, fields)
	var data any = map[string]any{}
	switch method {
	case "thread/goal/get":
		data = map[string]any{"goal": s.goal}
	case "thread/goal/set":
		s.materialized.Store(true)
		fields, _ := params.(map[string]any)
		if s.goal == nil {
			s.goal = map[string]any{}
		}
		for k, v := range fields {
			if k != "threadId" {
				s.goal[k] = v
			}
		}
		data = map[string]any{"goal": s.goal}
	case "thread/goal/clear":
		s.goal = nil
		data = map[string]any{"goal": nil}
	case "model/list":
		data = map[string]any{"data": []map[string]any{{"id": "sol", "model": "gpt-6-sol", "displayName": "Sol", "supportedReasoningEfforts": []map[string]any{{"reasoningEffort": "max"}, {"reasoningEffort": "ultra"}}}}}
	case "get_available_models":
		data = map[string]any{"models": []map[string]any{{"provider": "deepseek", "id": "deepseek-flash", "name": "Flash"}, {"provider": "other", "id": "reasoner", "name": "Other"}}}
	case "get_state":
		data = map[string]any{"model": map[string]any{"provider": "deepseek", "id": "deepseek-flash"}}
	case "get_available_thinking_levels":
		data = map[string]any{"levels": []string{"off", "low", "high", "max"}}
	case "thread/backgroundTerminals/list":
		data = map[string]any{"data": s.terminals}
	case "thread/backgroundTerminals/terminate":
		processID, _ := fields["processId"].(string)
		if !s.falseKeepsAlive {
			kept := s.terminals[:0]
			for _, process := range s.terminals {
				if process.ProcessID != processID {
					kept = append(kept, process)
				}
			}
			s.terminals = kept
		}
		data = map[string]any{"terminated": !s.terminateFalse}
	case "turn/interrupt":
		if s.interruptErr != nil {
			return s.interruptErr
		}
	case "turn/steer", "steer":
		if s.steerErr != nil {
			return s.steerErr
		}
	}
	if result != nil {
		encoded, _ := json.Marshal(data)
		return json.Unmarshal(encoded, result)
	}
	return nil
}

func setupRuntime(t *testing.T) (*Store, *Runtime, *runtimeFakeFactory, Bot) {
	t.Helper()
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	factory := &runtimeFakeFactory{sends: make(chan runtimeFakeSend, 32)}
	runtime := NewRuntime(store, RuntimeConfig{AgentFactory: factory.create, AgentOptions: runtimeFakeAgentOptions(), ToolTimeout: time.Second})
	t.Cleanup(func() {
		if err := runtime.Close(); err != nil {
			t.Errorf("runtime close: %v", err)
		}
	})
	return store, runtime, factory, store.ListBots()[0]
}

func runtimeFakeAgentOptions() map[string]map[string]any {
	return map[string]map[string]any{"codex": {"app_server_url": "unix:///connect-bots-test/app-server.sock"}}
}

func nextRuntimeSend(t *testing.T, f *runtimeFakeFactory) runtimeFakeSend {
	t.Helper()
	select {
	case send := <-f.sends:
		return send
	case <-time.After(3 * time.Second):
		t.Fatal("adapter was not sent a message")
		return runtimeFakeSend{}
	}
}
func waitRuntimeTurn(t *testing.T, r *Runtime, id, turnID string) TurnResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := r.WaitTurn(ctx, id, turnID)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func waitRuntimeCondition(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("runtime state did not settle")
}

func TestRuntimeAsyncTurnSurvivesRequestAndKeepsFullNativeJournal(t *testing.T) {
	store, r, f, bot := setupRuntime(t)
	ctx, cancel := context.WithCancel(context.Background())
	id, err := r.SendMessage(ctx, bot.ID, MessageRequest{Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	send := nextRuntimeSend(t, f)
	if _, err := r.sendMessage(context.Background(), r.ctx, bot.ID, MessageRequest{Text: "internal overlap"}); !errors.Is(err, ErrBusy) {
		t.Fatalf("busy error: %v", err)
	}
	large := strings.Repeat("complete native tool output ", 2000)
	send.session.native("item/completed", map[string]any{"threadId": send.session.id, "turnId": "native-1", "item": map[string]any{"id": "tool1", "type": "commandExecution", "aggregatedOutput": large}})
	send.session.complete("answer")
	if result := waitRuntimeTurn(t, r, bot.ID, id); result.Status != "completed" || result.Text != "answer" {
		t.Fatalf("result %+v", result)
	}
	events, _ := store.Events(bot.ID, 0)
	messages, native := 0, false
	for _, event := range events {
		if event.TurnID != id {
			continue
		}
		if event.Type == "message" {
			messages++
		}
		if event.Type == "native" {
			var raw core.NativeEvent
			_ = json.Unmarshal(event.Data, &raw)
			native = strings.Contains(string(raw.Params), large)
		}
	}
	if messages != 2 || !native {
		t.Fatalf("canonical messages=%d full native=%v", messages, native)
	}
}

func TestRuntimeBackendSwitchRetainsBothThreadsAndVisibleHandoff(t *testing.T) {
	store, r, f, bot := setupRuntime(t)
	id, _ := r.SendMessage(context.Background(), bot.ID, MessageRequest{Text: "first question"})
	first := nextRuntimeSend(t, f)
	first.session.complete("first answer")
	waitRuntimeTurn(t, r, bot.ID, id)
	firstID := first.session.id
	if err := r.WithIdleBot(bot.ID, func() error {
		_, err := store.UpdateBot(bot.ID, func(bot *Bot) error { bot.Backend = "pi"; bot.Model = "deepseek/deepseek-flash"; return nil })
		return err
	}); err != nil {
		t.Fatal(err)
	}
	id, _ = r.SendMessage(context.Background(), bot.ID, MessageRequest{Text: "second question"})
	second := nextRuntimeSend(t, f)
	if !strings.Contains(second.prompt, "first answer") {
		t.Fatalf("handoff omitted previous answer: %s", second.prompt)
	}
	second.session.complete("second answer")
	waitRuntimeTurn(t, r, bot.ID, id)
	if err := r.WithIdleBot(bot.ID, func() error {
		_, err := store.UpdateBot(bot.ID, func(bot *Bot) error { bot.Backend = "codex"; bot.Model = "gpt-6-sol"; return nil })
		return err
	}); err != nil {
		t.Fatal(err)
	}
	id, _ = r.SendMessage(context.Background(), bot.ID, MessageRequest{Text: "third question"})
	third := nextRuntimeSend(t, f)
	if third.session.agent.resume != firstID || !strings.Contains(third.prompt, "second answer") {
		t.Fatalf("return resume=%s prompt=%s", third.session.agent.resume, third.prompt)
	}
	third.session.complete("third answer")
	waitRuntimeTurn(t, r, bot.ID, id)
	updated, _ := store.GetBot(bot.ID)
	if updated.Threads["codex"] != firstID || updated.Threads["pi"] == "" {
		t.Fatalf("backend threads %+v", updated.Threads)
	}
}

func TestRuntimeInstructionChangeRotatesLoadedCodexButModelChangeResumes(t *testing.T) {
	store, r, f, bot := setupRuntime(t)
	id, _ := r.SendMessage(context.Background(), bot.ID, MessageRequest{Text: "remember"})
	first := nextRuntimeSend(t, f)
	first.session.complete("remembered")
	waitRuntimeTurn(t, r, bot.ID, id)
	if err := r.WithIdleBot(bot.ID, func() error {
		_, err := store.UpdateBot(bot.ID, func(bot *Bot) error { bot.Role = "Changed persistent responsibility"; return nil })
		return err
	}); err != nil {
		t.Fatal(err)
	}
	id, _ = r.SendMessage(context.Background(), bot.ID, MessageRequest{Text: "new role"})
	second := nextRuntimeSend(t, f)
	if second.session.agent.resume != "" || second.session.id == first.session.id || !strings.Contains(second.prompt, "remembered") {
		t.Fatalf("config resume=%q prompt=%s", second.session.agent.resume, second.prompt)
	}
	second.session.complete("new result")
	waitRuntimeTurn(t, r, bot.ID, id)
	if err := r.WithIdleBot(bot.ID, func() error {
		_, err := store.UpdateBot(bot.ID, func(bot *Bot) error { bot.Model = "gpt-6-luna"; return nil })
		return err
	}); err != nil {
		t.Fatal(err)
	}
	id, _ = r.SendMessage(context.Background(), bot.ID, MessageRequest{Text: "new model"})
	third := nextRuntimeSend(t, f)
	if third.session.agent.resume != second.session.id {
		t.Fatalf("model change discarded native history: %q", third.session.agent.resume)
	}
	third.session.complete("done")
	waitRuntimeTurn(t, r, bot.ID, id)
	events, _ := store.Events(bot.ID, 0)
	oldRetained := false
	for _, event := range events {
		if event.Type == "system" && strings.Contains(string(event.Data), first.session.id) {
			oldRetained = true
		}
	}
	if !oldRetained {
		t.Fatal("old native thread ID disappeared from journal")
	}
}

func TestRuntimePermissionWireStringAliasResolvedOnceAndAnswerDurable(t *testing.T) {
	store, r, f, bot := setupRuntime(t)
	id, _ := r.SendMessage(context.Background(), bot.ID, MessageRequest{Text: "approval"})
	send := nextRuntimeSend(t, f)
	send.session.emit(core.Event{Type: core.EventPermissionRequest, RequestID: `"wire-request"`, ToolName: "AskUserQuestion"})
	waitRuntimeCondition(t, func() bool {
		s, _ := r.state(bot.ID)
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.pendingPermissions[`"wire-request"`]
	})
	decision := core.PermissionResult{Behavior: "allow", UpdatedInput: map[string]any{"answers": map[string]any{"question": "chosen"}}}
	if err := r.Permission(context.Background(), bot.ID, "wire-request", decision); err != nil {
		t.Fatal(err)
	}
	if err := r.Permission(context.Background(), bot.ID, "wire-request", decision); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale answer: %v", err)
	}
	send.session.complete("approved")
	waitRuntimeTurn(t, r, bot.ID, id)
	events, _ := store.Events(bot.ID, 0)
	found := false
	for _, event := range events {
		if event.Type == "permission" && strings.Contains(string(event.Data), "chosen") {
			found = true
		}
	}
	if !found {
		t.Fatal("permission answer was not journalled")
	}
}

func TestRuntimeDelegationWaitsForResultAndRejectsCyclesAndIdleTools(t *testing.T) {
	store, r, f, chief := setupRuntime(t)
	target, err := store.CreateBot(Bot{Name: "Specialist", Role: "Do bounded work"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.DynamicTool(context.Background(), chief.ID, "bots_list", json.RawMessage(`{}`)); err == nil {
		t.Fatal("idle bot tool allowed")
	}
	sourceID, _ := r.SendMessage(context.Background(), chief.ID, MessageRequest{Text: "delegate"})
	source := nextRuntimeSend(t, f)
	resultCh := make(chan core.DynamicToolResult, 1)
	go func() {
		args, _ := json.Marshal(map[string]any{"botId": target.ID, "message": "do this"})
		result, _ := r.DynamicTool(context.Background(), chief.ID, "bots_send", args)
		resultCh <- result
	}()
	targetSend := nextRuntimeSend(t, f)
	cycleArgs, _ := json.Marshal(map[string]any{"botId": chief.ID, "message": "back"})
	cycle, err := r.DynamicTool(context.Background(), target.ID, "bots_send", cycleArgs)
	if err != nil || cycle.Success {
		t.Fatalf("cycle accepted result=%+v err=%v", cycle, err)
	}
	create, err := r.DynamicTool(context.Background(), target.ID, "bots_create", json.RawMessage(`{"name":"Unauthorized","role":"role"}`))
	if err != nil || create.Success {
		t.Fatalf("specialist create accepted: %+v %v", create, err)
	}
	select {
	case <-resultCh:
		t.Fatal("delegation returned before target completed")
	default:
	}
	targetSend.session.complete("actual target result")
	select {
	case result := <-resultCh:
		if !result.Success || !strings.Contains(fmt.Sprint(result.ContentItems), "actual target result") {
			t.Fatalf("delegation result %+v", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("delegation did not return")
	}
	created, err := r.DynamicTool(context.Background(), chief.ID, "bots_create", json.RawMessage(`{"name":"New specialist","role":"Keep one responsibility"}`))
	if err != nil || !created.Success {
		t.Fatalf("chief create: %+v %v", created, err)
	}
	source.session.complete("coordinated")
	waitRuntimeTurn(t, r, chief.ID, sourceID)
}

func TestRuntimeGoalImmediateContinuationHasSeparateProductTurn(t *testing.T) {
	store, r, f, bot := setupRuntime(t)
	id, _ := r.SendMessage(context.Background(), bot.ID, MessageRequest{Text: "goal"})
	send := nextRuntimeSend(t, f)
	send.session.native("turn/started", map[string]any{"threadId": send.session.id, "turn": map[string]any{"id": "provider-first"}})
	send.session.native("turn/completed", map[string]any{"threadId": send.session.id, "turn": map[string]any{"id": "provider-first", "status": "completed"}})
	// The next native goal turn is announced before the normalized reader has
	// finalized the old one. Child activity must not settle either root turn.
	send.session.native("turn/started", map[string]any{"threadId": send.session.id, "turn": map[string]any{"id": "provider-second"}})
	send.session.native("turn/completed", map[string]any{"threadId": "child-thread", "turn": map[string]any{"id": "child-turn", "status": "completed"}})
	send.session.emit(core.Event{Type: core.EventText, Content: "first answer", Metadata: map[string]any{"turnId": "provider-first", "threadId": send.session.id}})
	send.session.emit(core.Event{Type: core.EventResult, Done: true, SessionID: send.session.id, Metadata: map[string]any{"turnId": "provider-first", "threadId": send.session.id}})
	first := waitRuntimeTurn(t, r, bot.ID, id)
	if first.Text != "first answer" {
		t.Fatalf("first result %+v", first)
	}
	s, _ := r.state(bot.ID)
	s.mu.Lock()
	next := s.current
	s.mu.Unlock()
	if next == nil || next.id == id || !r.Busy(bot.ID) {
		t.Fatal("next goal turn was lost or merged")
	}
	updated, _ := store.GetBot(bot.ID)
	if updated.Status != "running" {
		t.Fatalf("old finalizer marked ongoing goal %s", updated.Status)
	}
	send.session.emit(core.Event{Type: core.EventText, Content: "second answer", Metadata: map[string]any{"turnId": "provider-second", "threadId": send.session.id}})
	send.session.native("turn/completed", map[string]any{"threadId": send.session.id, "turn": map[string]any{"id": "provider-second", "status": "completed"}})
	send.session.emit(core.Event{Type: core.EventResult, Done: true, SessionID: send.session.id, Metadata: map[string]any{"turnId": "provider-second", "threadId": send.session.id}})
	second := waitRuntimeTurn(t, r, bot.ID, next.id)
	if second.Text != "second answer" || second.Status != "completed" {
		t.Fatalf("second result %+v", second)
	}
	events, _ := store.Events(bot.ID, 0)
	for _, event := range events {
		if event.Type == "native" && strings.Contains(string(event.Data), "provider-second") && event.TurnID != next.id {
			t.Fatalf("wrong continuation attribution %+v", event)
		}
	}
}

func TestGenerationEstimateExcludesToolAndPermissionWaiting(t *testing.T) {
	m := generationMetrics{}
	start := time.Unix(100, 0)
	item := func(kind string) map[string]any { return map[string]any{"item": map[string]any{"type": kind}} }
	m.nativeEvent("item/started", item("reasoning"), start)
	m.nativeEvent("item/completed", item("reasoning"), start.Add(time.Second))
	m.nativeEvent("item/started", item("commandExecution"), start.Add(time.Second))
	m.nativeEvent("item/completed", item("commandExecution"), start.Add(20*time.Second))
	m.nativeEvent("item/started", item("agentMessage"), start.Add(20*time.Second))
	m.nativeEvent("item/completed", item("agentMessage"), start.Add(22*time.Second))
	if m.elapsed != 3*time.Second {
		t.Fatalf("generation included waiting: %s", m.elapsed)
	}
}

func TestRuntimeCatalogAndGoalUseActualRPCAndOwnedThread(t *testing.T) {
	store, r, f, bot := setupRuntime(t)
	_, err := store.CreateBot(Bot{Name: "Pi", Role: "Use Pi", Backend: "pi"})
	if err != nil {
		t.Fatal(err)
	}
	caps, err := r.Capabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !caps.Backends["codex"].Available || !caps.Backends["codex"].Goals || !caps.Backends["pi"].Available {
		t.Fatalf("actual caps %+v", caps)
	}
	for _, model := range caps.Models {
		if model.ID == "gpt-6-sol" && !strings.Contains(strings.Join(model.Efforts, ","), "ultra") {
			t.Fatal("actual ultra lost")
		}
		if model.ID == "other/reasoner" && len(model.Efforts) != 0 {
			t.Fatal("Pi current efforts fabricated on another model")
		}
	}
	if _, err := r.Goal(context.Background(), bot.ID, "set", map[string]any{"threadId": "another-owner", "objective": "Bounded goal", "status": "paused", "tokenBudget": 100}); err != nil {
		t.Fatal(err)
	}
	state, _ := r.state(bot.ID)
	state.mu.Lock()
	session := state.session.(*runtimeFakeSession)
	state.mu.Unlock()
	session.mu.Lock()
	objective := session.goal["objective"]
	session.mu.Unlock()
	if objective != "Bounded goal" {
		t.Fatal("native goal not set")
	}
	if _, err := r.Goal(context.Background(), bot.ID, "arbitrary", nil); err == nil {
		t.Fatal("arbitrary goal RPC allowed")
	}
	select {
	case send := <-f.sends:
		t.Fatalf("catalog sent inference: %s", send.prompt)
	default:
	}
}

func TestRuntimeIdleMutationSerializesSendAndRejectsWorkingBot(t *testing.T) {
	store, r, f, bot := setupRuntime(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	mutated := make(chan error, 1)
	go func() {
		mutated <- r.WithIdleBot(bot.ID, func() error {
			close(entered)
			<-release
			_, err := store.UpdateBot(bot.ID, func(bot *Bot) error { bot.Role = "Latest responsibility"; return nil })
			return err
		})
	}()
	<-entered
	accepted := make(chan string, 1)
	go func() {
		id, _ := r.SendMessage(context.Background(), bot.ID, MessageRequest{Text: "use updated role"})
		accepted <- id
	}()
	select {
	case <-accepted:
		t.Fatal("send crossed an ongoing idle mutation")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-mutated; err != nil {
		t.Fatal(err)
	}
	id := <-accepted
	send := nextRuntimeSend(t, f)
	if !strings.Contains(send.session.agent.opts["developer_instructions"].(string), "Latest responsibility") {
		t.Fatal("send used a stale role")
	}
	if err := r.WithIdleBot(bot.ID, func() error { return nil }); !errors.Is(err, ErrBusy) {
		t.Fatalf("working mutation allowed: %v", err)
	}
	send.session.complete("done")
	waitRuntimeTurn(t, r, bot.ID, id)
}

func TestRuntimeAttachmentsCannotUseClientPathsWithoutResolver(t *testing.T) {
	_, r, _, bot := setupRuntime(t)
	_, err := r.SendMessage(context.Background(), bot.ID, MessageRequest{Attachments: []Attachment{{Name: "secret", Path: "/etc/passwd"}}})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("client path accepted: %v", err)
	}
}

func TestRuntimeStopPausesOwnedGoalThenInterruptsAndResumesLater(t *testing.T) {
	store, r, f, bot := setupRuntime(t)
	id, _ := r.SendMessage(context.Background(), bot.ID, MessageRequest{Text: "work"})
	send := nextRuntimeSend(t, f)
	send.session.native("turn/started", map[string]any{"threadId": send.session.id, "turn": map[string]any{"id": "ongoing-native"}})
	if _, err := r.Goal(context.Background(), bot.ID, "set", map[string]any{"objective": "Keep working", "status": "active"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Stop(context.Background(), bot.ID); err != nil {
		t.Fatal(err)
	}
	result := waitRuntimeTurn(t, r, bot.ID, id)
	if result.Status != "stopped" {
		t.Fatalf("stop result %+v", result)
	}
	send.session.mu.Lock()
	calls := append([]string(nil), send.session.rpcCalls...)
	paused := send.session.goal["status"]
	send.session.mu.Unlock()
	sequence := strings.Join(calls, ",")
	if paused != "paused" || !strings.Contains(sequence, "thread/goal/get,thread/goal/set,turn/interrupt,thread/backgroundTerminals/list") {
		t.Fatalf("stop goal=%v calls=%v", paused, calls)
	}
	updated, _ := store.GetBot(bot.ID)
	if updated.Threads["codex"] != send.session.id {
		t.Fatal("stop lost native resume ID")
	}
	id, _ = r.SendMessage(context.Background(), bot.ID, MessageRequest{Text: "resume after stop"})
	next := nextRuntimeSend(t, f)
	if next.session.agent.resume != send.session.id {
		t.Fatal("stop lost backend resume")
	}
	next.session.complete("resumed")
	waitRuntimeTurn(t, r, bot.ID, id)
}

func TestRuntimeCloseInterruptsActiveOwnedThreadAndWaitTurnReplays(t *testing.T) {
	store, r, f, bot := setupRuntime(t)
	id, _ := r.SendMessage(context.Background(), bot.ID, MessageRequest{Text: "long work"})
	send := nextRuntimeSend(t, f)
	send.session.native("turn/started", map[string]any{"threadId": send.session.id, "turn": map[string]any{"id": "running"}})
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	result := waitRuntimeTurn(t, r, bot.ID, id)
	if result.Status != "interrupted" {
		t.Fatalf("shutdown turn %+v", result)
	}
	if send.session.Alive() {
		t.Fatal("owned adapter left alive")
	}
	send.session.mu.Lock()
	calls := strings.Join(send.session.rpcCalls, ",")
	send.session.mu.Unlock()
	if !strings.Contains(calls, "turn/interrupt") {
		t.Fatalf("remote thread not interrupted: %s", calls)
	}
	other := NewRuntime(store, RuntimeConfig{AgentFactory: f.create, AgentOptions: runtimeFakeAgentOptions()})
	defer other.Close()
	replayed, err := other.WaitTurn(context.Background(), bot.ID, id)
	if err != nil || replayed.Status != "interrupted" {
		t.Fatalf("replay %+v %v", replayed, err)
	}
}

func TestRuntimeMaintenanceIdleAccessKeepsNativeGoalAndAdapter(t *testing.T) {
	_, r, f, bot := setupRuntime(t)
	id, _ := r.SendMessage(context.Background(), bot.ID, MessageRequest{Text: "setup"})
	send := nextRuntimeSend(t, f)
	send.session.complete("idle")
	waitRuntimeTurn(t, r, bot.ID, id)
	_, err := r.Goal(context.Background(), bot.ID, "set", map[string]any{"objective": "native goal", "status": "active"})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.WithIdleBotAccess(bot.ID, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	send.session.mu.Lock()
	status := send.session.goal["status"]
	send.session.mu.Unlock()
	if !send.session.Alive() || status != "active" {
		t.Fatalf("maintenance changed adapter=%v goal=%v", send.session.Alive(), status)
	}
}

func TestRuntimeChiefPromotionRotatesNativeDynamicTools(t *testing.T) {
	store, r, f, chief := setupRuntime(t)
	_, err := store.UpdateBot(chief.ID, func(bot *Bot) error { bot.Chief = false; return nil })
	if err != nil {
		t.Fatal(err)
	}
	id, _ := r.SendMessage(context.Background(), chief.ID, MessageRequest{Text: "specialist"})
	first := nextRuntimeSend(t, f)
	first.session.complete("answer")
	waitRuntimeTurn(t, r, chief.ID, id)
	hasCreate := func(tools []map[string]any) bool {
		for _, tool := range tools {
			if tool["name"] == "bots_create" {
				return true
			}
		}
		return false
	}
	if hasCreate(first.session.agent.tools) {
		t.Fatal("specialist has chief create tool")
	}
	if err := r.WithIdleBot(chief.ID, func() error {
		_, err := store.UpdateBot(chief.ID, func(bot *Bot) error { bot.Chief = true; return nil })
		return err
	}); err != nil {
		t.Fatal(err)
	}
	id, _ = r.SendMessage(context.Background(), chief.ID, MessageRequest{Text: "chief"})
	second := nextRuntimeSend(t, f)
	if second.session.agent.resume != "" || !hasCreate(second.session.agent.tools) {
		t.Fatalf("chief tools stayed loaded oldthread resume=%s tools=%v", second.session.agent.resume, second.session.agent.tools)
	}
	second.session.complete("chief ready")
	waitRuntimeTurn(t, r, chief.ID, id)
}

func TestRuntimeSharedIdleGateDoesNotHoldRegistryWhileNativeStartWaits(t *testing.T) {
	store, r, _, bot := setupRuntime(t)
	other, err := store.CreateBot(Bot{Name: "Other", Role: "Other role"})
	if err != nil {
		t.Fatal(err)
	}
	s, _ := r.state(bot.ID)
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		finished <- r.WithIdleBots([]string{other.ID, bot.ID, bot.ID}, func() error { close(entered); <-release; return nil })
	}()
	<-entered
	callbackDone := make(chan struct{})
	go func() { _ = r.beginNativeTurn(s, 0, "provider", false); close(callbackDone) }()
	registryAvailable := make(chan struct{})
	go func() { r.mu.Lock(); r.mu.Unlock(); close(registryAvailable) }()
	select {
	case <-registryAvailable:
	case <-time.After(time.Second):
		t.Fatal("native observer held registry while waiting for an idle gate")
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	select {
	case <-callbackDone:
	case <-time.After(time.Second):
		t.Fatal("native observer and shared mutation deadlocked")
	}
}
