package bots

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestRuntimeCapabilitiesDoesNotWaitForActiveBotLifecycle(t *testing.T) {
	for _, selected := range []bool{false, true} {
		name := "workspace"
		if selected {
			name = "selected_bot"
		}
		t.Run(name, func(t *testing.T) {
			store, runtime, factory, bot := setupRuntime(t)
			completeRuntimePrompt(t, runtime, factory, bot.ID, "start the conversation", "ready")
			turnID, err := runtime.SendMessage(context.Background(), bot.ID, MessageRequest{Text: "keep working"})
			if err != nil {
				t.Fatal(err)
			}
			sent := nextRuntimeSend(t, factory)
			state, err := runtime.state(bot.ID)
			if err != nil {
				t.Fatal(err)
			}
			sent.session.mu.Lock()
			beforeRPCs := len(sent.session.rpcCalls)
			sent.session.goal = map[string]any{"objective": "Keep the active goal", "status": "active"}
			sent.session.mu.Unlock()
			before, err := store.GetBot(bot.ID)
			if err != nil {
				t.Fatal(err)
			}
			state.lifecycle.Lock()
			type response struct {
				capabilities Capabilities
				err          error
			}
			result := make(chan response, 1)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			go func() {
				var caps Capabilities
				var err error
				if selected {
					caps, err = runtime.CapabilitiesForBot(ctx, bot.ID)
				} else {
					caps, err = runtime.Capabilities(ctx)
				}
				result <- response{caps, err}
			}()
			var received response
			select {
			case received = <-result:
			case <-ctx.Done():
				t.Error("model discovery waited for the active bot's lifecycle lock")
				state.lifecycle.Unlock()
				received = <-result
				state.lifecycle.Lock()
			}
			state.lifecycle.Unlock()
			if received.err != nil || !received.capabilities.Backends["codex"].Available || !received.capabilities.Backends["codex"].Goals {
				t.Errorf("active bot catalog unavailable: %+v, %v", received.capabilities, received.err)
			}
			state.mu.Lock()
			unchanged := state.session == sent.session && state.current != nil && state.current.id == turnID
			state.mu.Unlock()
			if !unchanged || !sent.session.Alive() {
				t.Fatal("catalog discovery detached or replaced the active bot session")
			}
			sent.session.mu.Lock()
			if len(sent.session.rpcCalls) != beforeRPCs || sent.session.goal["status"] != "active" {
				t.Error("catalog discovery issued control RPCs on the active bot thread")
			}
			sent.session.mu.Unlock()
			after, err := store.GetBot(bot.ID)
			if err != nil {
				t.Fatal(err)
			}
			if after.Threads["codex"] != before.Threads["codex"] {
				t.Error("catalog discovery changed the bot's durable thread")
			}
			sent.session.complete("finished normally")
			if result := waitRuntimeTurn(t, runtime, bot.ID, turnID); result.Status != "completed" {
				t.Fatalf("catalog interrupted the active turn: %+v", result)
			}
		})
	}
}

func TestRuntimeCapabilitiesSelectedCodexKeepsGoalsWhileUnrelatedPiIsWorking(t *testing.T) {
	store, runtime, factory, codex := setupRuntime(t)
	pi, err := store.CreateBot(Bot{Name: "Pi specialist", Role: "Keep working", Backend: "pi", Model: "deepseek/deepseek-flash", Effort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	turnID, err := runtime.SendMessage(context.Background(), pi.ID, MessageRequest{Text: "keep working"})
	if err != nil {
		t.Fatal(err)
	}
	sent := nextRuntimeSend(t, factory)
	state, err := runtime.state(pi.ID)
	if err != nil {
		t.Fatal(err)
	}
	state.lifecycle.Lock()
	type response struct {
		capabilities Capabilities
		err          error
	}
	result := make(chan response, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() {
		caps, err := runtime.CapabilitiesForBot(ctx, codex.ID)
		result <- response{caps, err}
	}()
	var received response
	select {
	case received = <-result:
		state.lifecycle.Unlock()
	case <-ctx.Done():
		state.lifecycle.Unlock()
		received = <-result
		t.Error("selected Codex discovery waited for an unrelated Pi bot")
	}
	if received.err != nil || !received.capabilities.Backends["codex"].Available || !received.capabilities.Backends["codex"].Goals || !received.capabilities.Backends["pi"].Available {
		t.Errorf("unrelated Pi work hid Codex goals or models: %+v, %v", received.capabilities, received.err)
	}
	factory.mu.Lock()
	var discovery *runtimeFakeAgent
	for _, agent := range factory.created {
		if agent.backend == "pi" && agent.opts["work_dir"] == filepath.Join(store.Root(), "catalog", "pi") {
			discovery = agent
		}
	}
	factory.mu.Unlock()
	if discovery == nil || discovery.opts["model"] != pi.Model || discovery.opts["thinking"] != pi.Effort || discovery.resume != "" {
		t.Error("isolated Pi discovery lost the selected model or resumed its conversation")
	}
	runtime.mu.Lock()
	createdBotSession := runtime.states[codex.ID] != nil
	runtime.mu.Unlock()
	if createdBotSession {
		t.Error("model discovery installed a persistent session for the idle Codex bot")
	}
	sent.session.complete("finished normally")
	if result := waitRuntimeTurn(t, runtime, pi.ID, turnID); result.Status != "completed" {
		t.Fatalf("catalog interrupted unrelated Pi work: %+v", result)
	}
}

func TestRuntimeTemporaryCodexCatalogRequiresExplicitProductConnection(t *testing.T) {
	for _, endpoint := range []string{"", "managed://", "unix://", "unix:///dedicated-catalog/app-server.sock", "ws://127.0.0.1:9831"} {
		t.Run(endpoint, func(t *testing.T) {
			store := testStore(t)
			factory := &runtimeFakeFactory{sends: make(chan runtimeFakeSend, 8)}
			runtime := NewRuntime(store, RuntimeConfig{AgentFactory: factory.create, AgentOptions: map[string]map[string]any{"codex": {
				"app_server_url": endpoint, "backend": "exec", "cmd": "product-codex", "codex_home": "/product/native-codex",
			}}})
			t.Cleanup(func() { _ = runtime.Close() })
			lease, err := runtime.temporaryCatalogSession(context.Background(), "codex")
			valid := endpoint == "unix:///dedicated-catalog/app-server.sock" || endpoint == "ws://127.0.0.1:9831"
			if !valid {
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("implicit/shared catalog connection accepted: %v", err)
				}
				factory.mu.Lock()
				created := len(factory.created)
				factory.mu.Unlock()
				if created != 0 {
					t.Fatal("invalid endpoint created an adapter before validation")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			lease.release()
			factory.mu.Lock()
			options := factory.created[0].opts
			factory.mu.Unlock()
			if options["app_server_url"] != endpoint || options["backend"] != "app-server" || options["cmd"] != "product-codex" || options["codex_home"] != "/product/native-codex" || options["work_dir"] != filepath.Join(store.Root(), "catalog", "codex") {
				t.Fatalf("catalog bypassed the product connection: %v", options)
			}
			select {
			case <-factory.sends:
				t.Fatal("temporary catalog sent inference")
			default:
			}
		})
	}
}

func TestRuntimeGoalBeforeFirstPromptPersistsThreadAndConfiguration(t *testing.T) {
	store, runtime, factory, bot := setupRuntime(t)
	if _, err := runtime.Goal(context.Background(), bot.ID, "get", nil); err != nil {
		t.Fatal(err)
	}
	before, err := store.GetBot(bot.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.Threads["codex"] != "" {
		t.Fatal("read-only discovery committed an unmaterialized thread")
	}
	if _, err := runtime.Goal(context.Background(), bot.ID, "set", map[string]any{"objective": "Persist this goal", "status": "paused", "tokenBudget": 100}); err != nil {
		t.Fatal(err)
	}
	after, err := store.GetBot(bot.ID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := runtime.state(bot.ID)
	if err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	threadID, digest, committed := state.session.CurrentSessionID(), state.configSignatures["codex"], state.committedSignatures["codex"]
	state.mu.Unlock()
	if after.Threads["codex"] != threadID || digest == "" || committed != digest {
		t.Fatalf("goal-first thread/config was not committed: bot=%+v digest=%s committed=%s", after, digest, committed)
	}
	events, err := store.Events(bot.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var audit bool
	for _, event := range events {
		if event.Type == "goal_action" {
			var action map[string]any
			if err := json.Unmarshal(event.Data, &action); err != nil {
				t.Fatal(err)
			}
			audit = action["threadId"] == threadID && action["method"] == "set"
		}
	}
	if !audit {
		t.Fatal("successful goal mutation has no owned-thread audit")
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	restarted := NewRuntime(store, RuntimeConfig{AgentFactory: factory.create, AgentOptions: runtimeFakeAgentOptions()})
	t.Cleanup(func() { _ = restarted.Close() })
	if _, err := restarted.Goal(context.Background(), bot.ID, "get", nil); err != nil {
		t.Fatal(err)
	}
	factory.mu.Lock()
	resume := factory.created[len(factory.created)-1].resume
	factory.mu.Unlock()
	if resume != threadID {
		t.Fatalf("goal-first thread lost on service restart: resume=%s want=%s", resume, threadID)
	}
	select {
	case send := <-factory.sends:
		t.Fatalf("paused goal unexpectedly sent inference: %s", send.prompt)
	default:
	}
}

type goalControlFailureAgent struct {
	*runtimeFakeAgent
	session *goalControlFailureSession
}

func (a *goalControlFailureAgent) StartSession(ctx context.Context, resume string) (core.AgentSession, error) {
	if _, err := a.runtimeFakeAgent.StartSession(ctx, resume); err != nil {
		return nil, err
	}
	return a.session, nil
}

type goalControlFailureSession struct {
	*runtimeFakeSession
	fail atomic.Bool
}

func (s *goalControlFailureSession) RPC(ctx context.Context, method string, params any, result any) error {
	if s.fail.Load() && method == "thread/goal/set" {
		return errors.New("native goal mutation rejected")
	}
	return s.runtimeFakeSession.RPC(ctx, method, params, result)
}

func TestRuntimeExplicitGoalCancelsCarryOnlyAfterSuccessfulRPC(t *testing.T) {
	store := testStore(t)
	bot := store.ListBots()[0]
	factory := &runtimeFakeFactory{sends: make(chan runtimeFakeSend, 8)}
	var session *goalControlFailureSession
	runtime := NewRuntime(store, RuntimeConfig{AgentOptions: runtimeFakeAgentOptions(), AgentFactory: func(backend string, opts map[string]any) (core.Agent, error) {
		agent, err := factory.create(backend, opts)
		if err != nil {
			return nil, err
		}
		fake := agent.(*runtimeFakeAgent)
		session = &goalControlFailureSession{runtimeFakeSession: fake.session}
		return &goalControlFailureAgent{runtimeFakeAgent: fake, session: session}, nil
	}})
	t.Cleanup(func() { _ = runtime.Close() })
	if _, err := runtime.Goal(context.Background(), bot.ID, "get", nil); err != nil {
		t.Fatal(err)
	}
	state, err := runtime.state(bot.ID)
	if err != nil {
		t.Fatal(err)
	}
	setPending := func() {
		state.mu.Lock()
		state.pendingGoal = map[string]any{"objective": "Old paused goal", "status": "paused"}
		state.pendingGoalSource = "old-thread"
		state.mu.Unlock()
	}
	setPending()
	session.fail.Store(true)
	if _, err := runtime.Goal(context.Background(), bot.ID, "set", map[string]any{"objective": "New goal", "status": "paused"}); err == nil {
		t.Fatal("rejected native goal mutation unexpectedly succeeded")
	}
	state.mu.Lock()
	preserved := state.pendingGoal != nil && state.pendingGoalSource == "old-thread"
	state.mu.Unlock()
	if !preserved {
		t.Fatal("unsuccessful mutation cancelled recovery of the old goal")
	}
	session.fail.Store(false)
	if _, err := runtime.Goal(context.Background(), bot.ID, "set", map[string]any{"objective": "New goal", "status": "paused"}); err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	cancelled := state.pendingGoal == nil && state.pendingGoalSource == "" && !state.goalCarrySuppressed
	state.mu.Unlock()
	if !cancelled {
		t.Fatal("explicit successful goal set left old pending carry active")
	}
	setPending()
	if _, err := runtime.Goal(context.Background(), bot.ID, "clear", nil); err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	cleared := state.pendingGoal == nil && state.pendingGoalSource == "" && state.goalCarrySuppressed
	state.mu.Unlock()
	if !cleared {
		t.Fatal("explicit clear can be undone by pending goal carry")
	}
}
