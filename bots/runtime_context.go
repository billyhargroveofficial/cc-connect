package bots

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// Context reports observed usage; missing provider data stays unknown.
func (r *Runtime) Context(ctx context.Context, botID string) (map[string]any, error) {
	ctx, done, err := r.catalogControlContext(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	bot, err := r.store.GetBot(botID)
	if err != nil {
		return nil, err
	}
	lease, err := r.botCatalogSession(ctx, bot)
	if err != nil {
		return nil, err
	}
	defer lease.release()
	s, err := r.state(botID)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	compacting := s.compacting
	s.mu.Unlock()
	r.flushNative()
	events, err := r.store.Events(botID, 0)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"compacting": compacting}
	if bot.Backend == "pi" {
		var state struct {
			IsCompacting bool `json:"isCompacting"`
			Model        *struct {
				ContextWindow int `json:"contextWindow"`
			} `json:"model"`
		}
		if err := lease.rpc.RPC(ctx, "get_state", nil, &state); err != nil {
			return nil, err
		}
		result["compacting"] = compacting || state.IsCompacting
		if state.Model != nil && state.Model.ContextWindow > 0 {
			result["contextWindow"] = state.Model.ContextWindow
		}
		var stats struct {
			ContextUsage *struct {
				Tokens        *float64 `json:"tokens"`
				ContextWindow int      `json:"contextWindow"`
				Percent       *float64 `json:"percent"`
			} `json:"contextUsage"`
		}
		if err := lease.rpc.RPC(ctx, "get_session_stats", nil, &stats); err != nil {
			return nil, err
		}
		if usage := stats.ContextUsage; usage != nil {
			if usage.ContextWindow > 0 {
				result["contextWindow"] = usage.ContextWindow
			}
			if usage.Tokens != nil && *usage.Tokens >= 0 && !math.IsNaN(*usage.Tokens) && !math.IsInf(*usage.Tokens, 0) {
				result["usedTokens"] = int(*usage.Tokens)
				result["estimated"] = true
			}
			if usage.Percent != nil && *usage.Percent >= 0 && !math.IsNaN(*usage.Percent) && !math.IsInf(*usage.Percent, 0) {
				result["percent"] = *usage.Percent
			}
		}
	} else {
		observed, nativeCompacting, journalKnowsUsage := codexContextFromJournal(events, lease.session.CurrentSessionID())
		result["compacting"] = compacting || nativeCompacting
		for key, value := range observed {
			result[key] = value
		}
		if !journalKnowsUsage {
			if reporter, ok := lease.session.(core.ContextUsageReporter); ok {
				if usage := reporter.GetContextUsage(); usage != nil {
					result["usedTokens"] = usage.UsedTokens
					if usage.ContextWindow > 0 {
						result["contextWindow"] = usage.ContextWindow
					}
				}
			}
		}
	}
	if used, ok := result["usedTokens"].(int); ok {
		if window, ok := result["contextWindow"].(int); ok && window > 0 {
			remaining := window - used
			if remaining < 0 {
				remaining = 0
			}
			result["remainingTokens"] = remaining
			if _, reported := result["percent"]; !reported {
				result["percent"] = float64(used) / float64(window) * 100
			}
		}
	}
	return result, nil
}

func codexContextFromJournal(events []Event, threadID string) (map[string]any, bool, bool) {
	usage := make(map[string]any)
	compacting := false
	journalKnowsUsage := false
	for _, event := range events {
		if event.Type != "native" {
			continue
		}
		var native core.NativeEvent
		if json.Unmarshal(event.Data, &native) != nil || native.Backend != "codex" {
			continue
		}
		var params map[string]any
		if json.Unmarshal(native.Params, &params) != nil || nativeThreadID(params) != threadID {
			continue
		}
		item, _ := params["item"].(map[string]any)
		if item["type"] == "contextCompaction" {
			switch native.Method {
			case "item/started":
				compacting = true
				usage = make(map[string]any)
				journalKnowsUsage = true
			case "item/completed":
				compacting = false
			}
		}
		if native.Method == "turn/completed" {
			compacting = false
		}
		if native.Method != "thread/tokenUsage/updated" {
			continue
		}
		value, _ := params["tokenUsage"].(map[string]any)
		last, _ := value["last"].(map[string]any)
		if last == nil {
			continue
		}
		journalKnowsUsage = true
		usage = make(map[string]any)
		if _, present := last["totalTokens"]; present {
			usage["usedTokens"] = integerValue(last["totalTokens"])
		} else if _, present := last["inputTokens"]; present {
			usage["usedTokens"] = integerValue(last["inputTokens"]) + integerValue(last["outputTokens"])
		}
		if window := integerValue(value["modelContextWindow"]); window > 0 {
			usage["contextWindow"] = window
		}
		if compacting {
			// Codex recomputes its new history estimate before completing the
			// compaction item. Keep that measurement after item/completed.
			usage["estimated"] = true
		}
	}
	return usage, compacting, journalKnowsUsage
}

// Compact queues one native operation; it never recreates the conversation or
// sends a replacement user prompt. Its HTTP request does not own its lifetime.
func (r *Runtime) Compact(ctx context.Context, botID, instructions string) (any, error) {
	ctx, done, err := r.catalogControlContext(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	if len(instructions) > 16*1024 {
		return nil, fmt.Errorf("%w: compaction instructions exceed 16 KiB", ErrInvalid)
	}
	bot, err := r.store.GetBot(botID)
	if err != nil {
		return nil, err
	}
	if bot.Backend == "codex" && strings.TrimSpace(instructions) != "" {
		return nil, fmt.Errorf("%w: Codex native compaction does not accept instructions", ErrInvalid)
	}
	lease, err := r.botCatalogSession(ctx, bot)
	if err != nil {
		return nil, err
	}
	defer func() {
		if lease.release != nil {
			lease.release()
		}
	}()
	s, err := r.state(botID)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	busy := s.current != nil || s.compacting
	s.mu.Unlock()
	if busy {
		return nil, ErrBusy
	}
	requestID, err := randomID("compact_")
	if err != nil {
		return nil, err
	}
	r.flushNative()
	var baseline uint64
	if journal, ok := r.store.(interface{ Cursor() uint64 }); ok {
		baseline = journal.Cursor()
	}
	operationCtx, cancel := context.WithCancel(r.ctx)
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		cancel()
		return nil, context.Canceled
	}
	r.wg.Add(1)
	r.mu.Unlock()
	s.mu.Lock()
	if s.current != nil || s.compacting {
		s.mu.Unlock()
		r.wg.Done()
		cancel()
		return nil, ErrBusy
	}
	s.compacting, s.compactionCancel, s.compactionTurnID = true, cancel, ""
	s.compactionCompleted = false
	s.mu.Unlock()
	if _, err := r.store.AppendEvent(botID, "", "compact_action", map[string]any{"backend": bot.Backend, "threadId": lease.session.CurrentSessionID(), "requestId": requestID, "status": "started"}); err != nil {
		s.mu.Lock()
		s.compacting, s.compactionCancel, s.compactionTurnID = false, nil, ""
		s.compactionCompleted = false
		s.mu.Unlock()
		r.wg.Done()
		cancel()
		return nil, err
	}
	release := lease.release
	lease.release = nil
	release()
	go r.runCompaction(operationCtx, cancel, s, bot, lease.session, lease.rpc, requestID, baseline, instructions)
	return map[string]any{"requestId": requestID}, nil
}

func (r *Runtime) runCompaction(ctx context.Context, cancel context.CancelFunc, s *botRuntime, bot Bot, session core.AgentSession, rpc core.AgentRPCSession, requestID string, baseline uint64, instructions string) {
	defer r.wg.Done()
	defer cancel()
	var result any
	method := "thread/compact/start"
	params := map[string]any{"threadId": session.CurrentSessionID()}
	if bot.Backend == "pi" {
		method, params = "compact", make(map[string]any)
		if instructions != "" {
			params["customInstructions"] = instructions
		}
	}
	err := rpc.RPC(ctx, method, params, &result)
	if err == nil && bot.Backend == "codex" {
		err = r.waitCodexCompaction(ctx, bot.ID, session, baseline)
	}
	r.flushNative()
	status := "completed"
	data := map[string]any{"backend": bot.Backend, "threadId": session.CurrentSessionID(), "requestId": requestID, "result": result}
	if err != nil {
		status = "failed"
		data["error"] = err.Error()
	}
	data["status"] = status
	_, journalErr := r.store.AppendEvent(bot.ID, "", "compact_action", data)
	r.logJournalError(bot.ID, "", journalErr)
	s.mu.Lock()
	s.compacting, s.compactionCancel, s.compactionTurnID = false, nil, ""
	s.compactionCompleted = false
	s.mu.Unlock()
}

func (r *Runtime) waitCodexCompaction(ctx context.Context, botID string, session core.AgentSession, after uint64) error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	threadID, itemID, turnID := session.CurrentSessionID(), "", ""
	for {
		events, err := r.store.Events(botID, after)
		if err != nil {
			return err
		}
		for _, event := range events {
			after = event.Seq
			if event.Type != "native" {
				continue
			}
			var native core.NativeEvent
			if json.Unmarshal(event.Data, &native) != nil || native.Backend != "codex" {
				continue
			}
			var params map[string]any
			if json.Unmarshal(native.Params, &params) != nil || nativeThreadID(params) != threadID {
				continue
			}
			providerTurn := nativeTurnID(params)
			if native.Method == "turn/started" && turnID == "" {
				turnID = providerTurn
			}
			item, _ := params["item"].(map[string]any)
			if item["type"] == "contextCompaction" {
				id, _ := item["id"].(string)
				if itemID == "" {
					itemID = id
				}
				if id == itemID && providerTurn != "" {
					turnID = providerTurn
				}
			}
			if native.Method == "turn/completed" && turnID != "" && providerTurn == turnID {
				turn, _ := params["turn"].(map[string]any)
				switch turn["status"] {
				case "completed":
					return nil
				case "failed", "interrupted":
					if failure, ok := turn["error"].(map[string]any); ok {
						if message, ok := failure["message"].(string); ok && message != "" {
							return fmt.Errorf("compaction turn %v: %s", turn["status"], message)
						}
					}
					return fmt.Errorf("compaction turn %v", turn["status"])
				}
			}
		}
		if !session.Alive() {
			return fmt.Errorf("compaction session closed before completion")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
