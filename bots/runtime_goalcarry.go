package bots

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// Configuration changes rotate a loaded Codex thread. Keep its unfinished goal
// as an explicitly paused goal with only the remaining original token budget.
// Pending carry is recoverable from the ordinary journal if the service exits
// between pausing the old thread and materializing the replacement.
func (r *Runtime) captureGoalIfChanged(s *botRuntime, session core.AgentSession, bot Bot) error {
	if session == nil || bot.Backend != "codex" {
		return nil
	}
	opts, _, err := r.agentOptions(bot)
	if err != nil {
		return err
	}
	digest := sessionConfigSignature(bot, opts)
	s.mu.Lock()
	oldDigest, backend := s.configSignatures["codex"], s.backend
	s.mu.Unlock()
	if backend != "codex" || oldDigest == "" || digest == oldDigest {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = r.captureGoal(ctx, s, session)
	s.mu.Lock()
	s.goalRefreshChecked = true
	s.mu.Unlock()
	return err
}

func (r *Runtime) captureGoal(ctx context.Context, s *botRuntime, session core.AgentSession) error {
	s.mu.Lock()
	suppressed := s.goalCarrySuppressed
	s.mu.Unlock()
	if suppressed {
		return nil
	}
	rpc, ok := session.(core.AgentRPCSession)
	if !ok {
		return nil
	}
	var response struct {
		Goal map[string]any `json:"goal"`
	}
	if err := rpc.RPC(ctx, "thread/goal/get", map[string]any{"threadId": session.CurrentSessionID()}, &response); err != nil {
		// The old provider thread and its goal remain durable even on a backend
		// version without experimental goals. Do not invent a goal from text.
		r.logJournalError(s.id, "", fmt.Errorf("read goal before configuration refresh: %w", err))
		return nil
	}
	goal := response.Goal
	objective, _ := goal["objective"].(string)
	if strings.TrimSpace(objective) == "" || goal["status"] == "complete" {
		return nil
	}
	fields := map[string]any{"objective": objective, "status": "paused", "tokenBudget": nil}
	if budget, ok := goal["tokenBudget"]; ok && budget != nil {
		remaining := integerValue(budget) - integerValue(goal["tokensUsed"])
		if remaining < 0 {
			remaining = 0
		}
		fields["tokenBudget"] = remaining
	}
	if exhaustedGoalBudget(fields) {
		return r.recordExhaustedGoal(s, session.CurrentSessionID(), fields)
	}
	s.mu.Lock()
	s.pendingGoal, s.pendingGoalSource = fields, session.CurrentSessionID()
	s.mu.Unlock()
	_, err := r.store.AppendEvent(s.id, "", "goal_carry", map[string]any{"backend": "codex", "status": "pending", "sourceThreadId": session.CurrentSessionID(), "fields": fields})
	return err
}

func (r *Runtime) restoreGoalCarry(ctx context.Context, s *botRuntime, session core.AgentSession) error {
	s.mu.Lock()
	fields, source := s.pendingGoal, s.pendingGoalSource
	s.mu.Unlock()
	if fields == nil || source == session.CurrentSessionID() {
		return nil
	}
	// Old journals can contain a carry written before zero remaining budgets
	// were handled. A native goal rejects zero; preserve its receipt instead of
	// retrying forever or silently replacing the budget with an unlimited one.
	if exhaustedGoalBudget(fields) {
		return r.recordExhaustedGoal(s, source, fields)
	}
	rpc, ok := session.(core.AgentRPCSession)
	if !ok {
		return nil
	}
	params := map[string]any{"threadId": session.CurrentSessionID()}
	for key, value := range fields {
		params[key] = value
	}
	var response any
	if err := rpc.RPC(ctx, "thread/goal/set", params, &response); err != nil {
		return err
	}
	if err := r.materializeThread(s.id, "codex", session.CurrentSessionID()); err != nil {
		return err
	}
	_, err := r.store.AppendEvent(s.id, "", "goal_action", map[string]any{"backend": "codex", "method": "set", "threadId": session.CurrentSessionID(), "result": response})
	if err != nil {
		return err
	}
	_, err = r.store.AppendEvent(s.id, "", "goal_carry", map[string]any{"backend": "codex", "status": "applied", "sourceThreadId": source, "threadId": session.CurrentSessionID(), "fields": fields})
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.pendingGoal, s.pendingGoalSource = nil, ""
	s.mu.Unlock()
	_, err = r.store.AppendEvent(s.id, "", "system", map[string]any{"content": "The goal was moved to a new session and paused. Review it and resume it explicitly; the remaining budget has been preserved."})
	return err
}

func exhaustedGoalBudget(fields map[string]any) bool {
	budget, ok := fields["tokenBudget"]
	return ok && budget != nil && integerValue(budget) <= 0
}

func (r *Runtime) recordExhaustedGoal(s *botRuntime, source string, fields map[string]any) error {
	if _, err := r.store.AppendEvent(s.id, "", "goal_carry", map[string]any{"backend": "codex", "status": "exhausted", "sourceThreadId": source, "fields": fields, "suppressCarry": true}); err != nil {
		return err
	}
	s.mu.Lock()
	s.pendingGoal, s.pendingGoalSource = nil, ""
	s.goalCarrySuppressed = true
	s.mu.Unlock()
	_, err := r.store.AppendEvent(s.id, "", "system", map[string]any{"content": "The goal's original budget is exhausted. The goal is saved in the journal; set a new budget explicitly to continue."})
	return err
}

func (r *Runtime) cancelPendingGoal(botID, reason string) error {
	r.mu.Lock()
	s := r.states[botID]
	r.mu.Unlock()
	if s == nil {
		return nil
	}
	s.mu.Lock()
	pending := s.pendingGoal
	previousSuppression := s.goalCarrySuppressed
	suppressed := reason == "owner_stop" || reason == "owner_goal_clear"
	s.goalCarrySuppressed = suppressed
	if suppressed {
		s.goalRefreshChecked = true
	}
	s.pendingGoal, s.pendingGoalSource = nil, ""
	s.mu.Unlock()
	if pending == nil && !suppressed && !previousSuppression {
		return nil
	}
	_, err := r.store.AppendEvent(botID, "", "goal_carry", map[string]any{"backend": "codex", "status": "cancelled", "reason": reason, "suppressCarry": suppressed})
	return err
}
