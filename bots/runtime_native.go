package bots

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// A native observer only attributes and enqueues. One writer owns persistence
// so high-frequency deltas never block an adapter's JSON-RPC reader on fsync.
type queuedNative struct {
	botID   string
	turnID  string
	event   *core.NativeEvent
	begin   *runtimeTurn
	barrier chan struct{}
}

func (r *Runtime) enqueueNative(item queuedNative) {
	r.nativeMu.Lock()
	if !r.nativeClosing {
		r.nativeQueue = append(r.nativeQueue, item)
	} else if item.barrier != nil {
		close(item.barrier)
	}
	r.nativeMu.Unlock()
	select {
	case r.nativeWake <- struct{}{}:
	default:
	}
}

func (r *Runtime) nativeWriter() {
	defer close(r.nativeDone)
	for {
		<-r.nativeWake
		for {
			r.nativeMu.Lock()
			batch := r.nativeQueue
			r.nativeQueue = nil
			closing := r.nativeClosing
			r.nativeMu.Unlock()
			if len(batch) == 0 {
				if closing {
					return
				}
				break
			}
			for _, item := range batch {
				if item.barrier != nil {
					close(item.barrier)
					continue
				}
				if item.begin != nil {
					r.logJournalError(item.botID, item.turnID, r.materializeThread(item.botID, item.begin.bot.Backend, item.begin.nativeThreadID))
					_, err := r.store.UpdateBot(item.botID, func(bot *Bot) error {
						if bot.Status != "archived" {
							bot.Status = "running"
						}
						return nil
					})
					r.logJournalError(item.botID, item.turnID, err)
					_, err = r.store.AppendEvent(item.botID, item.turnID, "system", map[string]any{"content": "Бот продолжает работу в своей постоянной сессии.", "source": "native"})
					r.logJournalError(item.botID, item.turnID, err)
					_, err = r.store.AppendEvent(item.botID, item.turnID, "turn", turnPayload(item.begin.bot, "running", TurnResult{}))
					r.logJournalError(item.botID, item.turnID, err)
				}
				if item.event != nil {
					_, err := r.store.AppendEvent(item.botID, item.turnID, "native", *item.event)
					if err != nil {
						r.logJournalError(item.botID, item.turnID, err)
						r.failNativeTurn(item.turnID, err)
					}
				}
			}
		}
	}
}

func (r *Runtime) failNativeTurn(id string, err error) {
	r.mu.Lock()
	t := r.turns[id]
	r.mu.Unlock()
	if t != nil {
		t.mu.Lock()
		t.err = err.Error()
		t.mu.Unlock()
		t.cancel()
	}
}

func (r *Runtime) flushNative() {
	barrier := make(chan struct{})
	r.enqueueNative(queuedNative{barrier: barrier})
	<-barrier
}

func (r *Runtime) observeNative(s *botRuntime, epoch uint64, event core.NativeEvent) {
	var params map[string]any
	_ = json.Unmarshal(event.Params, &params)
	threadID := nativeThreadID(params)
	providerTurn := nativeTurnID(params)
	s.mu.Lock()
	if s.epoch != epoch {
		s.mu.Unlock()
		return
	}
	if s.threadID == "" && event.RootThreadID != "" {
		s.threadID = event.RootThreadID
	}
	root := threadID == "" || s.threadID == "" || threadID == s.threadID
	if s.compacting && threadID != "" && threadID == s.threadID && providerTurn != "" {
		item, _ := params["item"].(map[string]any)
		if item["type"] == "contextCompaction" && s.compactionTurnID != providerTurn {
			// The first start notification is provisional. The compaction
			// item identifies its actual owner, even if another turn started
			// first; child items never replace the owned root identity.
			s.compactionTurnID = providerTurn
			s.compactionCompleted = false
		} else if event.Method == "turn/started" && s.compactionTurnID == "" {
			s.compactionTurnID = providerTurn
		}
		if event.Method == "turn/completed" && providerTurn == s.compactionTurnID {
			turn, _ := params["turn"].(map[string]any)
			switch turn["status"] {
			case "completed", "failed", "interrupted":
				s.compactionCompleted = true
			}
		}
	}
	compacting := compactionSuppressesTurn(s, providerTurn)
	active := s.current
	s.mu.Unlock()
	if compacting {
		// A manual native compaction owns a provider turn, but is not a new
		// conversational/goal turn. Preserve its full journal without assigning
		// it to an old answer or minting a visible acknowledgment message.
		if event.Timestamp.IsZero() {
			event.Timestamp = r.cfg.Now().UTC()
		}
		copyEvent := event
		copyEvent.Params = append(json.RawMessage(nil), event.Params...)
		copyEvent.RequestID = append(json.RawMessage(nil), event.RequestID...)
		r.enqueueNative(queuedNative{botID: s.id, event: &copyEvent})
		return
	}
	if root && (event.Method == "turn/started" || event.Method == "agent_start") {
		rotate := false
		if active != nil && providerTurn != "" {
			active.mu.Lock()
			rotate = active.nativeCompleted && active.providerTurn != "" && active.providerTurn != providerTurn
			active.mu.Unlock()
		}
		if active == nil || rotate {
			active = r.beginNativeTurn(s, epoch, providerTurn, rotate)
		}
	}
	s.mu.Lock()
	if s.epoch != epoch || compactionSuppressesTurn(s, providerTurn) {
		s.mu.Unlock()
		return
	}
	turnID := ""
	if providerTurn != "" {
		turnID = s.providerTurns[providerTurn]
	}
	if turnID == "" && s.current != nil {
		turnID = s.current.id
	}
	if turnID == "" && providerTurn != "" {
		turnID = s.lastTurn
	}
	if providerTurn != "" && turnID != "" {
		s.providerTurns[providerTurn] = turnID
	}
	if len(s.providerTurns) > 512 {
		for key, value := range s.providerTurns {
			if value != turnID {
				delete(s.providerTurns, key)
			}
		}
	}
	active = s.current
	s.mu.Unlock()
	if turnID != "" {
		r.mu.Lock()
		active = r.turns[turnID]
		r.mu.Unlock()
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = r.cfg.Now().UTC()
	}
	if active != nil && active.id == turnID && root {
		active.mu.Lock()
		if event.Method == "turn/started" && providerTurn != "" {
			active.providerTurn = providerTurn
		}
		active.metrics.nativeEvent(event.Method, params, event.Timestamp)
		if event.Method == "turn/completed" {
			active.nativeCompleted = true
			turn, _ := params["turn"].(map[string]any)
			active.nativeStatus, _ = turn["status"].(string)
			if failure, ok := turn["error"].(map[string]any); ok {
				if message, ok := failure["message"].(string); ok {
					active.err = message
				}
			}
		}
		if item, ok := params["item"].(map[string]any); ok && item["type"] == "commandExecution" {
			if active.toolItems == nil {
				active.toolItems = map[string]bool{}
			}
			if id, ok := item["id"].(string); ok {
				active.toolItems[id] = true
			}
			if process, ok := item["processId"].(string); ok && process != "" {
				if active.processIDs == nil {
					active.processIDs = map[string]bool{}
				}
				active.processIDs[process] = true
			}
		}
		active.mu.Unlock()
	}
	copyEvent := event
	copyEvent.Params = append(json.RawMessage(nil), event.Params...)
	copyEvent.RequestID = append(json.RawMessage(nil), event.RequestID...)
	r.enqueueNative(queuedNative{botID: s.id, turnID: turnID, event: &copyEvent})
	// Legacy result remains the canonical transcript finalizer, because it is
	// emitted after the same native turn/completed has normalized final text.
	// Native lifecycle starts unsolicited goal continuations even while idle.
}

func (r *Runtime) beginAutonomous(s *botRuntime, epoch uint64) *runtimeTurn {
	return r.beginNativeTurn(s, epoch, "", false)
}

// The caller holds s.mu. The asynchronous compact worker can retain its busy
// gate until it reads the terminal journal record. A distinct native goal turn
// may already have started by then; suppress only compaction's own turn after
// its exact terminal lifecycle has arrived. Unscoped events remain ambiguous.
func compactionSuppressesTurn(s *botRuntime, providerTurn string) bool {
	return s.compacting && (!s.compactionCompleted || providerTurn == "" || providerTurn == s.compactionTurnID)
}

func (r *Runtime) beginNativeTurn(s *botRuntime, epoch uint64, providerTurn string, rotate bool) *runtimeTurn {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.wg.Add(1)
	r.mu.Unlock()
	reserved := true
	defer func() {
		if reserved {
			r.wg.Done()
		}
	}()
	s.mu.Lock()
	if s.epoch != epoch || compactionSuppressesTurn(s, providerTurn) {
		s.mu.Unlock()
		return nil
	}
	if s.current != nil && !rotate {
		t := s.current
		s.mu.Unlock()
		return t
	}
	id, err := randomID("turn_")
	if err != nil {
		s.mu.Unlock()
		r.logJournalError(s.id, "", err)
		return nil
	}
	ctx, cancel := context.WithCancel(r.ctx)
	t := &runtimeTurn{id: id, botID: s.id, bot: s.bot, ctx: ctx, cancel: cancel, done: make(chan struct{}), settled: make(chan struct{}), providerTurn: providerTurn, nativeThreadID: s.threadID}
	s.current = t
	s.legacyTurns = append(s.legacyTurns, t)
	s.pendingPermissions = make(map[string]bool)
	session := s.session
	s.mu.Unlock()
	r.mu.Lock()
	r.turns[id] = t
	r.mu.Unlock()
	reserved = false
	r.enqueueNative(queuedNative{botID: s.id, turnID: id, begin: t})
	go func() {
		defer r.wg.Done()
		// A resumed goal can start before StartSession returns its session.
		if session == nil {
			s.lifecycle.Lock()
			s.mu.Lock()
			session = s.session
			s.mu.Unlock()
			s.lifecycle.Unlock()
		}
		if session == nil {
			r.finishTurn(s, t, "error", fmt.Errorf("native turn has no attached session"))
			return
		}
		r.awaitTurn(s, t, session)
	}()
	return t
}

func nativeThreadID(params map[string]any) string {
	if id, ok := params["threadId"].(string); ok {
		return id
	}
	if id, ok := params["thread_id"].(string); ok {
		return id
	}
	if thread, ok := params["thread"].(map[string]any); ok {
		id, _ := thread["id"].(string)
		return id
	}
	return ""
}

func nativeTurnID(params map[string]any) string {
	if id, ok := params["turnId"].(string); ok {
		return id
	}
	if id, ok := params["turn_id"].(string); ok {
		return id
	}
	if turn, ok := params["turn"].(map[string]any); ok {
		id, _ := turn["id"].(string)
		return id
	}
	return ""
}

type generationMetrics struct {
	native       bool
	started      time.Time
	elapsed      time.Duration
	outputTokens int
}

func (m *generationMetrics) resume(now time.Time) {
	if m.started.IsZero() {
		m.started = now
	}
}

func (m *generationMetrics) pause(now time.Time) {
	if !m.started.IsZero() {
		if duration := now.Sub(m.started); duration > 0 {
			m.elapsed += duration
		}
		m.started = time.Time{}
	}
}

func (m *generationMetrics) legacy(event core.Event, now time.Time) {
	if m.native {
		return
	}
	switch event.Type {
	case core.EventText, core.EventThinking:
		m.resume(now)
	case core.EventToolUse, core.EventPermissionRequest, core.EventResult, core.EventError:
		m.pause(now)
	}
}

func (m *generationMetrics) nativeEvent(method string, params map[string]any, now time.Time) {
	switch method {
	case "item/started":
		item, _ := params["item"].(map[string]any)
		kind, _ := item["type"].(string)
		if kind == "agentMessage" || kind == "reasoning" {
			m.native = true
			m.resume(now)
		} else if kind != "userMessage" {
			m.native = true
			m.pause(now)
		}
	case "item/agentMessage/delta", "item/reasoning/summaryTextDelta", "item/reasoning/textDelta":
		m.native = true
		m.resume(now)
	case "item/completed", "turn/completed":
		m.pause(now)
	case "message_start":
		message, _ := params["message"].(map[string]any)
		if message["role"] == "assistant" {
			m.native = true
			m.resume(now)
		}
	case "message_update":
		update, _ := params["assistantMessageEvent"].(map[string]any)
		kind, _ := update["type"].(string)
		if strings.Contains(kind, "text_") || strings.Contains(kind, "thinking_") {
			m.native = true
			m.resume(now)
		}
	case "message_end":
		m.pause(now)
		message, _ := params["message"].(map[string]any)
		if message["role"] == "assistant" {
			usage, _ := message["usage"].(map[string]any)
			m.outputTokens += integerValue(usage["output"])
		}
	case "tool_execution_start", "auto_retry_start", "agent_settled":
		m.pause(now)
	case "thread/tokenUsage/updated":
		usage, _ := params["tokenUsage"].(map[string]any)
		last, _ := usage["last"].(map[string]any)
		if tokens := integerValue(last["outputTokens"]); tokens > m.outputTokens {
			m.outputTokens = tokens
		}
	}
}

func integerValue(value any) int {
	switch value := value.(type) {
	case float64:
		if value > 0 {
			return int(value)
		}
	case int:
		if value > 0 {
			return value
		}
	case json.Number:
		n, _ := value.Int64()
		if n > 0 {
			return int(n)
		}
	}
	return 0
}
