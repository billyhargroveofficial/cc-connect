package bots

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// Keep the compact RPC acknowledgment pending while its native lifecycle is
// delivered. This deterministically models a continuation arriving before the
// asynchronous compact worker has observed its terminal journal event.
type compactContinuationSession struct {
	*runtimeFakeSession
	accepted chan struct{}
	release  chan struct{}
}

func (s *compactContinuationSession) RPC(ctx context.Context, method string, params any, result any) error {
	if err := s.runtimeFakeSession.RPC(ctx, method, params, result); err != nil {
		return err
	}
	if method == "thread/compact/start" {
		close(s.accepted)
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

type compactContinuationAgent struct {
	*runtimeFakeAgent
	session *compactContinuationSession
}

func (a *compactContinuationAgent) StartSession(ctx context.Context, resume string) (core.AgentSession, error) {
	if _, err := a.runtimeFakeAgent.StartSession(ctx, resume); err != nil {
		return nil, err
	}
	return a.session, nil
}

func TestRuntimeCompactImmediateGoalContinuationSurvivesWorkerBusyGate(t *testing.T) {
	store := testStore(t)
	bot, err := store.CreateBot(Bot{Name: "Goal continuation", Backend: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	factory := &runtimeFakeFactory{sends: make(chan runtimeFakeSend, 8)}
	accepted, release := make(chan struct{}), make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	var session *compactContinuationSession
	runtime := NewRuntime(store, RuntimeConfig{AgentOptions: runtimeFakeAgentOptions(), AgentFactory: func(backend string, opts map[string]any) (core.Agent, error) {
		agent, err := factory.create(backend, opts)
		if err != nil {
			return nil, err
		}
		fake := agent.(*runtimeFakeAgent)
		session = &compactContinuationSession{runtimeFakeSession: fake.session, accepted: accepted, release: release}
		return &compactContinuationAgent{runtimeFakeAgent: fake, session: session}, nil
	}})
	t.Cleanup(func() {
		if err := runtime.Close(); err != nil {
			t.Errorf("runtime close: %v", err)
		}
	})
	if _, err := runtime.Compact(context.Background(), bot.ID, ""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-accepted:
	case <-time.After(3 * time.Second):
		t.Fatal("compact RPC was not started")
	}
	state, _ := runtime.state(bot.ID)
	assertNoChat := func() {
		t.Helper()
		state.mu.Lock()
		current := state.current
		state.mu.Unlock()
		if current != nil {
			t.Fatal("chat continuation started before the owned compaction turn completed")
		}
	}
	// A turn/started is only a provisional candidate. The owned compaction
	// item supplies the actual provider turn, which must replace that candidate.
	session.native("turn/started", map[string]any{"threadId": session.id, "turn": map[string]any{"id": "provisional-turn"}})
	item := map[string]any{"id": "compact-item", "type": "contextCompaction"}
	session.native("item/started", map[string]any{"threadId": session.id, "turnId": "compact-turn", "item": item})
	state.mu.Lock()
	compactTurn := state.compactionTurnID
	state.mu.Unlock()
	if compactTurn != "compact-turn" {
		t.Fatalf("owned compaction item did not correct provisional identity: %s", compactTurn)
	}
	session.native("turn/completed", map[string]any{"threadId": session.id, "turn": map[string]any{"id": "provisional-turn", "status": "completed"}})
	session.native("turn/started", map[string]any{"threadId": session.id, "turn": map[string]any{"id": "too-early-goal"}})
	assertNoChat()
	session.native("item/completed", map[string]any{"threadId": session.id, "turnId": "compact-turn", "item": item})
	session.native("turn/completed", map[string]any{"threadId": "child-thread", "turn": map[string]any{"id": "compact-turn", "status": "completed"}})
	session.native("turn/started", map[string]any{"threadId": session.id, "turn": map[string]any{"id": "still-too-early-goal"}})
	assertNoChat()
	session.native("turn/completed", map[string]any{"turn": map[string]any{"id": "compact-turn", "status": "completed"}})
	session.native("turn/started", map[string]any{"threadId": session.id, "turn": map[string]any{"id": "ambiguous-owner"}})
	assertNoChat()
	session.native("turn/completed", map[string]any{"threadId": session.id, "turn": map[string]any{"id": "compact-turn", "status": "inProgress"}})
	session.native("turn/started", map[string]any{"threadId": session.id, "turn": map[string]any{"id": "not-terminal-yet"}})
	assertNoChat()
	session.native("turn/completed", map[string]any{"threadId": session.id, "turn": map[string]any{"id": "compact-turn", "status": "completed"}})
	// Do not wait for the compact worker: the next native goal turn starts in
	// the same observer burst while the compact operation's busy gate is true.
	session.native("turn/started", map[string]any{"threadId": session.id, "turn": map[string]any{"id": "goal-turn"}})
	state.mu.Lock()
	current, workerBusy := state.current, state.compacting
	state.mu.Unlock()
	if current == nil || !workerBusy {
		t.Fatalf("immediate goal continuation was swallowed: current=%v compacting=%v", current, workerBusy)
	}
	// Late normalized compaction output must never become the goal's answer.
	session.emit(core.Event{Type: core.EventText, Content: "private compact summary", Metadata: map[string]any{"turnId": "compact-turn", "threadId": session.id}})
	session.emit(core.Event{Type: core.EventResult, Done: true, Metadata: map[string]any{"turnId": "compact-turn", "threadId": session.id}})
	session.emit(core.Event{Type: core.EventText, Content: "actual goal answer", Metadata: map[string]any{"turnId": "goal-turn", "threadId": session.id}})
	session.native("turn/completed", map[string]any{"threadId": session.id, "turn": map[string]any{"id": "goal-turn", "status": "completed"}})
	session.emit(core.Event{Type: core.EventResult, Done: true, SessionID: session.id, Metadata: map[string]any{"turnId": "goal-turn", "threadId": session.id}})
	result := waitRuntimeTurn(t, runtime, bot.ID, current.id)
	if result.Status != "completed" || result.Text != "actual goal answer" {
		t.Fatalf("goal continuation result: %+v", result)
	}
	runtime.flushNative()
	events, err := store.Events(bot.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	messageCount := 0
	for _, event := range events {
		if event.Type == "message" {
			messageCount++
			var message map[string]any
			if err := json.Unmarshal(event.Data, &message); err != nil {
				t.Fatal(err)
			}
			if message["role"] != "assistant" || message["content"] != "actual goal answer" || event.TurnID != current.id {
				t.Fatalf("compaction leaked into the conversation: %+v %v", event, message)
			}
		}
		if event.Type != "native" {
			continue
		}
		var native core.NativeEvent
		var params map[string]any
		if json.Unmarshal(event.Data, &native) != nil || json.Unmarshal(native.Params, &params) != nil {
			t.Fatalf("invalid raw native event: %+v", event)
		}
		switch nativeTurnID(params) {
		case "goal-turn":
			if event.TurnID != current.id {
				t.Fatalf("goal native activity lost its product turn: %+v", event)
			}
		case "compact-turn":
			if event.TurnID != "" {
				t.Fatalf("compaction attributed to a conversational turn: %+v", event)
			}
		}
	}
	if messageCount != 1 {
		t.Fatalf("compaction produced phantom chat messages: %d", messageCount)
	}
	close(release)
	released = true
	waitRuntimeCondition(t, func() bool { return !runtime.Busy(bot.ID) })
	events, err = store.Events(bot.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var action map[string]any
	for _, event := range events {
		if event.Type == "compact_action" {
			if err := json.Unmarshal(event.Data, &action); err != nil {
				t.Fatal(err)
			}
		}
	}
	if action["status"] != "completed" {
		t.Fatalf("goal continuation changed the compact result: %v", action)
	}
	select {
	case <-factory.sends:
		t.Fatal("native compact or goal continuation resent a user prompt")
	default:
	}
}
