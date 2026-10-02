package bots

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestRuntimeStopTerminatesOnlyCurrentOwnedTurnProcessesAndPreservesOlderServer(t *testing.T) {
	_, r, factory, bot := setupRuntime(t)
	turnID, _ := r.SendMessage(context.Background(), bot.ID, MessageRequest{Text: "run command"})
	sent := nextRuntimeSend(t, factory)
	sent.session.native("turn/started", map[string]any{"threadId": sent.session.id, "turn": map[string]any{"id": "running"}})
	sent.session.native("item/started", map[string]any{"threadId": sent.session.id, "turnId": "running", "item": map[string]any{"id": "own-item", "type": "commandExecution", "processId": "own-handle"}})
	sent.session.native("item/started", map[string]any{"threadId": "child-thread", "turnId": "child-turn", "item": map[string]any{"id": "child-item", "type": "commandExecution", "processId": "child-handle"}})
	sent.session.mu.Lock()
	sent.session.terminals = []ownedBackgroundProcess{{ItemID: "server-item", ProcessID: "older-server"}, {ItemID: "own-item", ProcessID: "own-handle"}, {ItemID: "child-item", ProcessID: "child-handle"}}
	sent.session.mu.Unlock()
	if err := r.Stop(context.Background(), bot.ID); err != nil {
		t.Fatal(err)
	}
	if result := waitRuntimeTurn(t, r, bot.ID, turnID); result.Status != "stopped" {
		t.Fatalf("stop result: %+v", result)
	}
	sent.session.mu.Lock()
	defer sent.session.mu.Unlock()
	if len(sent.session.terminals) != 2 || sent.session.terminals[0].ProcessID != "older-server" || sent.session.terminals[1].ProcessID != "child-handle" {
		t.Fatalf("Stop touched older or child processes: %+v", sent.session.terminals)
	}
	terminated := 0
	for i, method := range sent.session.rpcCalls {
		if method != "thread/backgroundTerminals/list" && method != "thread/backgroundTerminals/terminate" {
			continue
		}
		if sent.session.rpcParams[i]["threadId"] != sent.session.id {
			t.Fatalf("unscoped process RPC: %+v", sent.session.rpcParams[i])
		}
		if method == "thread/backgroundTerminals/terminate" {
			terminated++
			if sent.session.rpcParams[i]["processId"] != "own-handle" {
				t.Fatalf("wrong native process handle: %+v", sent.session.rpcParams[i])
			}
		}
	}
	if terminated != 1 {
		t.Fatalf("current process was not confirmed terminated: calls=%v", sent.session.rpcCalls)
	}
}

func TestRuntimeStopNegativeTerminationReplyRequiresDisappearance(t *testing.T) {
	for _, stillAlive := range []bool{false, true} {
		name := "already_disappeared"
		if stillAlive {
			name = "termination_failed"
		}
		t.Run(name, func(t *testing.T) {
			_, r, factory, bot := setupRuntime(t)
			turnID, _ := r.SendMessage(context.Background(), bot.ID, MessageRequest{Text: "work"})
			sent := nextRuntimeSend(t, factory)
			sent.session.native("turn/started", map[string]any{"threadId": sent.session.id, "turn": map[string]any{"id": "running"}})
			sent.session.native("item/started", map[string]any{"threadId": sent.session.id, "turnId": "running", "item": map[string]any{"id": "command", "type": "commandExecution", "processId": "native-process"}})
			sent.session.mu.Lock()
			sent.session.terminals = []ownedBackgroundProcess{{ItemID: "command", ProcessID: "native-process"}}
			sent.session.terminateFalse, sent.session.falseKeepsAlive = true, stillAlive
			sent.session.mu.Unlock()
			if err := r.Stop(context.Background(), bot.ID); err != nil {
				t.Fatal(err)
			}
			result := waitRuntimeTurn(t, r, bot.ID, turnID)
			if stillAlive {
				if result.Status != "error" || !strings.Contains(result.Error, "did not stop") {
					t.Fatalf("false stop claimed success: %+v", result)
				}
			} else if result.Status != "stopped" {
				t.Fatalf("idempotent disappearance failed: %+v", result)
			}
		})
	}
}

func TestRuntimeExplicitIdleStopTerminatesOwnBackgroundJobs(t *testing.T) {
	_, r, factory, bot := setupRuntime(t)
	sent := completeRuntimePrompt(t, r, factory, bot.ID, "start a server", "server started")
	sent.session.mu.Lock()
	sent.session.terminals = []ownedBackgroundProcess{{ItemID: "old-server", ProcessID: "server-handle"}}
	sent.session.mu.Unlock()
	if err := r.Stop(context.Background(), bot.ID); err != nil {
		t.Fatal(err)
	}
	sent.session.mu.Lock()
	defer sent.session.mu.Unlock()
	if len(sent.session.terminals) != 0 {
		t.Fatal("explicit idle Stop left own background job running")
	}
}

func TestRuntimeIdleStopAfterRestartAttachesOnlyStoredOwnedThreadWithoutInference(t *testing.T) {
	store, r, factory, bot := setupRuntime(t)
	factory.strictResume = true
	sent := completeRuntimePrompt(t, r, factory, bot.ID, "start a background server", "server running")
	sent.session.mu.Lock()
	sent.session.terminals = []ownedBackgroundProcess{{ItemID: "server", ProcessID: "stored-server-handle"}}
	sent.session.mu.Unlock()
	r = restartTestRuntime(t, r, store, factory)
	if err := r.Stop(context.Background(), bot.ID); err != nil {
		t.Fatal(err)
	}
	factory.mu.Lock()
	control := factory.created[len(factory.created)-1]
	factory.mu.Unlock()
	if control.resume != sent.session.id || control.session.Alive() {
		t.Fatalf("idle Stop replaced or retained control session: resume=%s alive=%v", control.resume, control.session.Alive())
	}
	control.session.mu.Lock()
	if len(control.session.terminals) != 0 {
		t.Fatal("idle Stop after restart left own stored background job")
	}
	control.session.mu.Unlock()
	select {
	case prompt := <-factory.sends:
		t.Fatalf("idle Stop sent inference: %s", prompt.prompt)
	default:
	}
	after, _ := store.GetBot(bot.ID)
	if after.Threads["codex"] != sent.session.id {
		t.Fatal("control Stop replaced durable conversation")
	}
}

func TestRuntimeCompactionBusyGateBlocksPromptAndWorkspaceMutation(t *testing.T) {
	_, r, _, bot := setupRuntime(t)
	state, _ := r.state(bot.ID)
	state.mu.Lock()
	state.compacting = true
	state.mu.Unlock()
	defer func() { state.mu.Lock(); state.compacting = false; state.mu.Unlock() }()
	if !r.Busy(bot.ID) {
		t.Fatal("native compaction appeared idle")
	}
	if _, err := r.SendMessage(context.Background(), bot.ID, MessageRequest{Text: "overlap"}); !errors.Is(err, ErrBusy) {
		t.Fatalf("accepted prompt during compaction: %v", err)
	}
	if err := r.Invalidate(bot.ID); !errors.Is(err, ErrBusy) {
		t.Fatalf("invalidated compacting adapter: %v", err)
	}
	mutated := false
	if err := r.WithIdleBot(bot.ID, func() error { mutated = true; return nil }); !errors.Is(err, ErrBusy) || mutated {
		t.Fatalf("mutated compacting workspace: mutated=%v err=%v", mutated, err)
	}
}

func TestRuntimeManualCompactionKeepsNativeJournalWithoutChatAndStopInterruptsExactTurn(t *testing.T) {
	for _, failure := range []bool{false, true} {
		name := "confirmed_interrupt"
		if failure {
			name = "interrupt_failure_keeps_busy"
		}
		t.Run(name, func(t *testing.T) {
			store, r, factory, bot := setupRuntime(t)
			sent := completeRuntimePrompt(t, r, factory, bot.ID, "initial chat", "initial answer")
			state, _ := r.state(bot.ID)
			var cancelled atomic.Bool
			state.mu.Lock()
			state.compacting, state.compactionTurnID = true, ""
			state.compactionCancel = func() { cancelled.Store(true) }
			state.mu.Unlock()
			defer func() { state.mu.Lock(); state.compacting = false; state.compactionCancel = nil; state.mu.Unlock() }()
			sent.session.native("turn/started", map[string]any{"threadId": "child-thread", "turn": map[string]any{"id": "child-turn"}})
			sent.session.native("turn/started", map[string]any{"threadId": sent.session.id, "turn": map[string]any{"id": "compact-native"}})
			sent.session.native("item/started", map[string]any{"threadId": sent.session.id, "turnId": "compact-native", "item": map[string]any{"id": "compact-item", "type": "contextCompaction"}})
			// Its normalized output must not become a new assistant answer, even
			// after the preceding chat turn has already settled.
			sent.session.emit(core.Event{Type: core.EventText, Content: "private summary", Metadata: map[string]any{"turnId": "compact-native"}})
			sent.session.emit(core.Event{Type: core.EventResult, Done: true, Metadata: map[string]any{"turnId": "compact-native"}})
			state.mu.Lock()
			turnID, active := state.compactionTurnID, state.current
			state.mu.Unlock()
			if turnID != "compact-native" || active != nil {
				t.Fatalf("manual compaction minted chat or captured child ID: turn=%s current=%v", turnID, active)
			}
			if failure {
				sent.session.mu.Lock()
				sent.session.interruptErr = errors.New("provider cancellation failed")
				sent.session.mu.Unlock()
			}
			err := r.Stop(context.Background(), bot.ID)
			if failure {
				if err == nil || cancelled.Load() || !r.Busy(bot.ID) {
					t.Fatalf("failed native Stop released compact worker: err=%v cancelled=%v busy=%v", err, cancelled.Load(), r.Busy(bot.ID))
				}
			} else if err != nil || !cancelled.Load() {
				t.Fatalf("confirmed native Stop did not cancel worker: err=%v cancelled=%v", err, cancelled.Load())
			}
			sent.session.mu.Lock()
			var exact bool
			for i, method := range sent.session.rpcCalls {
				if method == "turn/interrupt" && sent.session.rpcParams[i]["threadId"] == sent.session.id && sent.session.rpcParams[i]["turnId"] == "compact-native" {
					exact = true
				}
			}
			sent.session.mu.Unlock()
			if !exact {
				t.Fatal("compaction was cancelled without its owned native turn ID")
			}
			r.flushNative()
			events, _ := store.Events(bot.ID, 0)
			messages, native := 0, false
			for _, event := range events {
				if event.Type == "message" {
					messages++
				}
				if event.Type == "native" && strings.Contains(string(event.Data), "compact-item") {
					native = true
					if event.TurnID != "" {
						t.Fatal("compaction raw event attributed to old answer")
					}
				}
			}
			if messages != 2 || !native {
				t.Fatalf("manual compaction visible messages=%d native=%v", messages, native)
			}
		})
	}
}

func TestRuntimeCloseInterruptsAcceptedManualCompactionBeforeDetaching(t *testing.T) {
	_, r, factory, bot := setupRuntime(t)
	if _, err := r.Compact(context.Background(), bot.ID, ""); err != nil {
		t.Fatal(err)
	}
	state, _ := r.state(bot.ID)
	state.mu.Lock()
	session := state.session.(*runtimeFakeSession)
	state.mu.Unlock()
	waitRuntimeCondition(t, func() bool {
		session.mu.Lock()
		defer session.mu.Unlock()
		for _, method := range session.rpcCalls {
			if method == "thread/compact/start" {
				return true
			}
		}
		return false
	})
	// An unrelated shared-daemon connection is not a product-owned bot state.
	unownedAgent, err := factory.create("codex", map[string]any{"work_dir": "unowned-project"})
	if err != nil {
		t.Fatal(err)
	}
	unowned := unownedAgent.(*runtimeFakeAgent).session
	defer unowned.Close()
	session.native("turn/started", map[string]any{"threadId": session.id, "turn": map[string]any{"id": "accepted-compact-turn"}})
	session.native("item/started", map[string]any{"threadId": session.id, "turnId": "accepted-compact-turn", "item": map[string]any{"id": "compact-item", "type": "contextCompaction"}})
	state.mu.Lock()
	current, compacting := state.current, state.compacting
	state.mu.Unlock()
	if current != nil || !compacting {
		t.Fatalf("test compaction did not own separate native operation: current=%v compacting=%v", current, compacting)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	session.mu.Lock()
	var interrupted bool
	for i, method := range session.rpcCalls {
		if method == "turn/interrupt" {
			params := session.rpcParams[i]
			if params["threadId"] != session.id || params["turnId"] != "accepted-compact-turn" {
				t.Fatalf("shutdown cancelled an unowned or guessed turn: %+v", params)
			}
			interrupted = true
		}
	}
	session.mu.Unlock()
	if !interrupted {
		t.Fatal("shutdown detached manual compaction without scoped native interruption")
	}
	if session.Alive() || !unowned.Alive() {
		t.Fatalf("shutdown left own connector or touched another client: own=%v unowned=%v", session.Alive(), unowned.Alive())
	}
	state.mu.Lock()
	stillCompacting := state.compacting
	state.mu.Unlock()
	if stillCompacting {
		t.Fatal("shutdown returned before compact worker settled")
	}
}
