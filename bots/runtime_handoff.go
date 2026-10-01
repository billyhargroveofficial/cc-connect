package bots

import (
	"context"
	"fmt"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// Resuming a paused native goal authorizes work. Before that work starts on a
// fresh configuration thread, put its pending conversation context there in
// one bounded acknowledgment turn. Reads and paused goals never call this.
func (r *Runtime) ensureGoalHandoff(ctx context.Context, botID string) error {
	bot, err := r.store.GetBot(botID)
	if err != nil {
		return err
	}
	if bot.Backend != "codex" {
		return nil
	}
	s, err := r.state(botID)
	if err != nil {
		return err
	}
	if _, err := r.ensureSession(ctx, s, bot); err != nil {
		return err
	}
	s.mu.Lock()
	needed, current := s.pendingHandoff != nil, s.current
	s.mu.Unlock()
	turnID := ""
	if current != nil && current.source == "goal_context" {
		turnID = current.id
	} else if !needed {
		return nil
	}
	if turnID == "" {
		workCtx, cancel := context.WithTimeout(r.ctx, 30*time.Second)
		defer cancel()
		turnID, err = r.sendMessage(ctx, workCtx, botID, MessageRequest{Source: "goal_context", Text: "Восстанови предоставленный контекст разговора перед возобновлением цели. Цель остаётся на паузе. Не используй инструменты, не делегируй, не меняй файлы или цель и не выполняй её работу. Ответь только кратким подтверждением, что контекст принят; сервис затем явно возобновит цель."})
		if err != nil {
			return fmt.Errorf("restore context before resuming goal: %w", err)
		}
	}
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result, err := r.WaitTurn(waitCtx, botID, turnID)
	if err != nil {
		return fmt.Errorf("wait for goal context handoff: %w", err)
	}
	if result.Status != "completed" {
		return fmt.Errorf("goal remains paused: context handoff %s: %s", result.Status, result.Error)
	}
	return nil
}

type pendingHandoff struct {
	From            string            `json:"from"`
	To              string            `json:"to"`
	Reason          string            `json:"reason"`
	ThreadID        string            `json:"threadId"`
	PreviousThreads map[string]string `json:"previousThreads"`
}

func (r *Runtime) prepareHandoff(s *botRuntime, bot Bot, session core.AgentSession) error {
	s.mu.Lock()
	pending := s.pendingHandoff
	if pending != nil && pending.To == bot.Backend {
		s.needsRefreshHandoff = true
		s.handoffReason = pending.Reason
	}
	needed := s.needsHandoff || s.needsRefreshHandoff
	reason, previous := s.handoffReason, s.lastBackend
	s.mu.Unlock()
	if !needed {
		return nil
	}
	if reason == "" {
		reason = "backend_changed"
	}
	previousThreads := bot.Threads
	if pending != nil && pending.To == bot.Backend {
		previous, previousThreads = pending.From, pending.PreviousThreads
	}
	next := &pendingHandoff{From: previous, To: bot.Backend, Reason: reason, ThreadID: session.CurrentSessionID(), PreviousThreads: previousThreads}
	if _, err := r.store.AppendEvent(bot.ID, "", "handoff_pending", next); err != nil {
		return err
	}
	s.mu.Lock()
	s.pendingHandoff = next
	s.mu.Unlock()
	return nil
}

func (r *Runtime) acceptHandoff(s *botRuntime, t *runtimeTurn, session core.AgentSession) error {
	s.mu.Lock()
	pending := s.pendingHandoff
	s.mu.Unlock()
	if pending == nil || pending.To != t.bot.Backend {
		return nil
	}
	if _, err := r.store.AppendEvent(t.botID, t.id, "handoff_applied", map[string]any{"backend": t.bot.Backend, "threadId": session.CurrentSessionID(), "from": pending.From, "to": pending.To}); err != nil {
		return err
	}
	s.mu.Lock()
	if s.pendingHandoff == pending {
		s.pendingHandoff = nil
		s.needsHandoff, s.needsRefreshHandoff = false, false
	}
	s.mu.Unlock()
	return nil
}
