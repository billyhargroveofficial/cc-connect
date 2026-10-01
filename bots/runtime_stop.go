package bots

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func (r *Runtime) stopCompaction(ctx context.Context, s *botRuntime) error {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	return r.stopCompactionLocked(ctx, s)
}

// The caller owns lifecycle; Close uses this before detaching its adapter and
// cancelling the runtime context, so accepted remote compaction is interrupted.
func (r *Runtime) stopCompactionLocked(ctx context.Context, s *botRuntime) error {
	s.mu.Lock()
	session, backend, cancel := s.session, s.backend, s.compactionCancel
	active := s.compacting
	s.mu.Unlock()
	if !active {
		return nil
	}
	if session == nil || !session.Alive() {
		if cancel != nil {
			cancel()
		}
		return nil
	}
	if err := r.interruptSession(s, session); err != nil {
		return err
	}
	if backend == "codex" {
		rpc, ok := session.(core.AgentRPCSession)
		if !ok {
			return fmt.Errorf("native compaction cancellation is unavailable")
		}
		waitCtx, stop := context.WithTimeout(ctx, 10*time.Second)
		defer stop()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			s.mu.Lock()
			turnID, running := s.compactionTurnID, s.compacting
			s.mu.Unlock()
			if !running {
				return nil
			}
			if turnID != "" {
				if err := rpc.RPC(waitCtx, "turn/interrupt", map[string]any{"threadId": session.CurrentSessionID(), "turnId": turnID}, nil); err != nil {
					return err
				}
				break
			}
			select {
			case <-waitCtx.Done():
				return fmt.Errorf("wait for owned compaction turn before Stop: %w", waitCtx.Err())
			case <-ticker.C:
			}
		}
	}
	// Only release the worker after a successful scoped native cancellation.
	// A failed interrupt must keep the bot busy while its provider still works.
	if cancel != nil {
		cancel()
	}
	return nil
}

// An explicit idle Stop also works after a service restart or a failed prior
// detach. This is a control-only attachment to the stored owned native thread;
// it never starts a new conversation or sends a prompt.
func (r *Runtime) stopIdleSession(s *botRuntime) error {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	s.mu.Lock()
	current, session := s.current, s.session
	s.mu.Unlock()
	if current != nil {
		current.mu.Lock()
		current.stopped = true
		current.mu.Unlock()
		current.cancel()
		return nil
	}
	if session != nil && session.Alive() {
		return errors.Join(r.interruptSession(s, session), r.stopBackgroundProcesses(s, session, nil, true))
	}
	bot, err := r.store.GetBot(s.id)
	if err != nil {
		return err
	}
	if bot.Backend != "codex" || bot.Threads["codex"] == "" {
		return nil
	}
	opts, _, err := r.agentOptions(bot)
	if err != nil {
		return err
	}
	agent, err := r.cfg.AgentFactory("codex", opts)
	if err != nil {
		return err
	}
	session, err = agent.StartSession(r.ctx, bot.Threads["codex"])
	if err != nil {
		return errors.Join(err, agent.Stop())
	}
	// Resuming a still-running owned thread may emit legacy notifications.
	// Drain them so a full adapter channel cannot block the control RPC reader.
	drained := make(chan struct{})
	go func() {
		for range session.Events() {
		}
		close(drained)
	}()
	defer func() { <-drained }()
	control := &botRuntime{id: bot.ID, backend: "codex"}
	// Recover only the authoritative running turn of this exact thread. No
	// journal replay or guessed turn IDs can cancel a different conversation.
	if rpc, ok := session.(core.AgentRPCSession); ok {
		ctx, cancel := context.WithTimeout(r.ctx, 10*time.Second)
		var snapshot struct {
			Thread struct {
				Turns []struct{ ID, Status string } `json:"turns"`
			} `json:"thread"`
		}
		err = rpc.RPC(ctx, "thread/read", map[string]any{"threadId": session.CurrentSessionID(), "includeTurns": true}, &snapshot)
		cancel()
		if err != nil {
			return errors.Join(fmt.Errorf("read owned turn before Stop: %w", err), closeAdapter(session, agent))
		}
		for i := len(snapshot.Thread.Turns) - 1; i >= 0; i-- {
			turn := snapshot.Thread.Turns[i]
			if turn.Status == "inProgress" {
				control.current = &runtimeTurn{providerTurn: turn.ID}
				break
			}
		}
	}
	return errors.Join(r.interruptSession(control, session), r.stopBackgroundProcesses(control, session, nil, true), closeAdapter(session, agent))
}

// turn/interrupt stops model work, but Codex intentionally retains unified_exec
// background terminals. Explicit Stop terminates this owned thread's terminals
// and waits for acknowledgement. A shutdown/delegation timeout terminates only
// terminals attributed to its active turn, preserving older deliberate servers.
func (r *Runtime) stopBackgroundProcesses(s *botRuntime, session core.AgentSession, t *runtimeTurn, all bool) error {
	if session == nil || !session.Alive() {
		return nil
	}
	s.mu.Lock()
	backend := s.backend
	s.mu.Unlock()
	if backend != "codex" {
		return nil
	}
	rpc, ok := session.(core.AgentRPCSession)
	if !ok {
		return nil
	}
	items, processes := map[string]bool{}, map[string]bool{}
	if t != nil {
		t.mu.Lock()
		for id := range t.toolItems {
			items[id] = true
		}
		for id := range t.processIDs {
			processes[id] = true
		}
		t.mu.Unlock()
	}
	// If this active turn exposes its actual item/process handles, stop those
	// exact processes even for an owner Stop. Older intentional servers stay.
	if all && len(items)+len(processes) > 0 {
		all = false
	}
	if !all && len(items)+len(processes) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	threadID := session.CurrentSessionID()
	listed, err := ownedBackgroundProcesses(ctx, rpc, threadID)
	if err != nil {
		return err
	}
	for _, process := range listed {
		if !all && !items[process.ItemID] && !processes[process.ProcessID] {
			continue
		}
		if process.ProcessID == "" {
			continue
		}
		var response struct {
			Terminated bool `json:"terminated"`
		}
		if err := rpc.RPC(ctx, "thread/backgroundTerminals/terminate", map[string]any{"threadId": threadID, "processId": process.ProcessID}, &response); err != nil {
			return fmt.Errorf("terminate owned background process: %w", err)
		}
		if !response.Terminated {
			remaining, err := ownedBackgroundProcesses(ctx, rpc, threadID)
			if err != nil {
				return err
			}
			for _, live := range remaining {
				if live.ProcessID == process.ProcessID {
					return fmt.Errorf("owned background process %s did not stop", process.ProcessID)
				}
			}
		}
		turnID := ""
		if t != nil {
			turnID = t.id
		}
		if _, err := r.store.AppendEvent(s.id, turnID, "system", map[string]any{"content": "Фоновая команда остановлена.", "threadId": threadID, "processId": process.ProcessID, "terminated": response.Terminated}); err != nil {
			return err
		}
	}
	return nil
}

type ownedBackgroundProcess struct {
	ItemID    string `json:"itemId"`
	ProcessID string `json:"processId"`
}

func ownedBackgroundProcesses(ctx context.Context, rpc core.AgentRPCSession, threadID string) ([]ownedBackgroundProcess, error) {
	result := []ownedBackgroundProcess{}
	cursor := ""
	for range 100 {
		params := map[string]any{"threadId": threadID, "limit": 100}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var page struct {
			Data       []ownedBackgroundProcess `json:"data"`
			NextCursor string                   `json:"nextCursor"`
		}
		if err := rpc.RPC(ctx, "thread/backgroundTerminals/list", params, &page); err != nil {
			return nil, fmt.Errorf("list owned background processes: %w", err)
		}
		result = append(result, page.Data...)
		if page.NextCursor == "" {
			return result, nil
		}
		if page.NextCursor == cursor {
			return nil, fmt.Errorf("background process list repeated a cursor")
		}
		cursor = page.NextCursor
	}
	return nil, fmt.Errorf("background process list exceeds 100 pages")
}
