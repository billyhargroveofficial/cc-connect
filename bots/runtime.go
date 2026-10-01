package bots

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	_ "github.com/chenhg5/cc-connect/agent/codex"
	_ "github.com/chenhg5/cc-connect/agent/pi"
	"github.com/chenhg5/cc-connect/core"
)

var ErrBusy = fmt.Errorf("%w: bot is already working", ErrConflict)

// RuntimeStore is the durable product boundary. The runtime never owns or
// closes the store; the service closes it after Runtime.Close has drained work.
type RuntimeStore interface {
	ListBots() []Bot
	GetBot(string) (Bot, error)
	CreateBot(Bot) (Bot, error)
	UpdateBot(string, func(*Bot) error) (Bot, error)
	AppendEvent(botID, turnID, kind string, data any) (Event, error)
	Events(botID string, after uint64) ([]Event, error)
	Root() string
	BotDir(string) (string, error)
}

type RuntimeConfig struct {
	PiExtensionPath    string
	InternalURL        string
	InternalToken      string
	Instructions       func(string) (string, error)
	SessionOptions     func(string) (map[string]any, error)
	ResolveAttachments func(string, []Attachment) ([]Attachment, error)
	PublishFiles       func(string, []string) ([]Attachment, error)
	VoiceAvailable     bool
	AgentOptions       map[string]map[string]any
	ToolTimeout        time.Duration
	MaxToolCalls       int
	// AgentFactory and Now allow deterministic tests without real inference.
	AgentFactory func(string, map[string]any) (core.Agent, error)
	Now          func() time.Time
}

type TurnResult struct {
	Text         string  `json:"text"`
	Status       string  `json:"status"`
	Error        string  `json:"error,omitempty"`
	OutputTokens int     `json:"outputTokens,omitempty"`
	GenerationMS int64   `json:"generationMs,omitempty"`
	TokensPerSec float64 `json:"tokensPerSecond,omitempty"`
}

type Runtime struct {
	store         RuntimeStore
	cfg           RuntimeConfig
	ctx           context.Context
	cancel        context.CancelFunc
	mu            sync.Mutex
	states        map[string]*botRuntime
	turns         map[string]*runtimeTurn
	edges         map[string]string
	closed        bool
	wg            sync.WaitGroup
	readerWG      sync.WaitGroup
	closeOnce     sync.Once
	closeErr      error
	nativeMu      sync.Mutex
	nativeQueue   []queuedNative
	nativeWake    chan struct{}
	nativeDone    chan struct{}
	nativeClosing bool
}

type botRuntime struct {
	compacting          bool
	compactionCancel    context.CancelFunc
	compactionTurnID    string
	compactionCompleted bool
	mu                  sync.Mutex
	lifecycle           sync.Mutex
	id                  string
	agent               core.Agent
	session             core.AgentSession
	signature           string
	backend             string
	lastBackend         string
	epoch               uint64
	current             *runtimeTurn
	lastTurn            string
	providerTurns       map[string]string
	pendingPermissions  map[string]bool
	needsHandoff        bool
	bot                 Bot
	threadID            string
	configSignatures    map[string]string
	committedSignatures map[string]string
	needsRefreshHandoff bool
	handoffReason       string
	legacyTurns         []*runtimeTurn
	pendingGoal         map[string]any
	pendingGoalSource   string
	goalRefreshChecked  bool
	goalCarrySuppressed bool
	pendingHandoff      *pendingHandoff
}

type runtimeTurn struct {
	source          string
	mu              sync.Mutex
	id              string
	botID           string
	bot             Bot
	ctx             context.Context
	cancel          context.CancelFunc
	done            chan struct{}
	settled         chan struct{}
	settleOnce      sync.Once
	finishOnce      sync.Once
	result          TurnResult
	text            strings.Builder
	err             string
	stopped         bool
	toolCalls       int
	metrics         generationMetrics
	providerTurn    string
	nativeCompleted bool
	nativeStatus    string
	nativeThreadID  string
	toolItems       map[string]bool
	processIDs      map[string]bool
}

func NewRuntime(store RuntimeStore, cfg RuntimeConfig) *Runtime {
	if cfg.AgentFactory == nil {
		cfg.AgentFactory = core.CreateAgent
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.ToolTimeout <= 0 {
		cfg.ToolTimeout = 3 * time.Minute
	}
	if cfg.MaxToolCalls <= 0 {
		cfg.MaxToolCalls = 12
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &Runtime{store: store, cfg: cfg, ctx: ctx, cancel: cancel,
		states: make(map[string]*botRuntime), turns: make(map[string]*runtimeTurn),
		edges: make(map[string]string), nativeWake: make(chan struct{}, 1), nativeDone: make(chan struct{})}
	go r.nativeWriter()
	return r
}

func (r *Runtime) state(id string) (*botRuntime, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, os.ErrClosed
	}
	if state := r.states[id]; state != nil {
		return state, nil
	}
	bot, err := r.store.GetBot(id)
	if err != nil {
		return nil, err
	}
	if bot.Status == "archived" {
		return nil, fmt.Errorf("%w: bot is archived", ErrConflict)
	}
	s := &botRuntime{id: id, providerTurns: make(map[string]string), pendingPermissions: make(map[string]bool), configSignatures: make(map[string]string), committedSignatures: make(map[string]string)}
	// The previous backend is recoverable from the journal, not a UI hint.
	events, err := r.store.Events(id, 0)
	if err != nil {
		return nil, err
	}
	for i := len(events) - 1; i >= 0; i-- {
		var data struct {
			Backend         string `json:"backend"`
			ConfigSignature string `json:"configSignature"`
		}
		if json.Unmarshal(events[i].Data, &data) != nil {
			continue
		}
		if events[i].Type == "turn" && data.Backend != "" && s.lastBackend == "" {
			s.lastBackend = data.Backend
		}
		if events[i].Type == "system" && data.ConfigSignature != "" && s.configSignatures[data.Backend] == "" {
			s.configSignatures[data.Backend] = data.ConfigSignature
			s.committedSignatures[data.Backend] = data.ConfigSignature
		}
	}
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type != "goal_carry" {
			continue
		}
		var carry struct {
			Status         string         `json:"status"`
			Fields         map[string]any `json:"fields"`
			SourceThreadID string         `json:"sourceThreadId"`
		}
		if json.Unmarshal(events[i].Data, &carry) == nil {
			if carry.Status == "pending" {
				s.pendingGoal, s.pendingGoalSource = carry.Fields, carry.SourceThreadID
			}
			var suppression struct {
				SuppressCarry bool `json:"suppressCarry"`
			}
			_ = json.Unmarshal(events[i].Data, &suppression)
			s.goalCarrySuppressed = suppression.SuppressCarry
			if s.goalCarrySuppressed {
				s.goalRefreshChecked = true
			}
		}
		break
	}
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == "handoff_applied" {
			break
		}
		if events[i].Type != "handoff_pending" {
			continue
		}
		var pending pendingHandoff
		if json.Unmarshal(events[i].Data, &pending) == nil && pending.To == bot.Backend {
			s.pendingHandoff = &pending
		}
		break
	}
	r.states[id] = s
	return s, nil
}

func (r *Runtime) Busy(id string) bool {
	r.mu.Lock()
	s := r.states[id]
	r.mu.Unlock()
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current != nil || s.compacting
}

// SendMessage accepts a turn independently of the HTTP request lifetime. A
// disconnected client can replay the journal or WaitTurn without resending it.
func (r *Runtime) SendMessage(ctx context.Context, id string, request MessageRequest) (string, error) {
	return r.sendMessage(ctx, r.ctx, id, request)
}

func (r *Runtime) sendMessage(ctx, workCtx context.Context, id string, request MessageRequest) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if strings.TrimSpace(request.Text) == "" && len(request.Attachments) == 0 {
		return "", fmt.Errorf("%w: message is empty", ErrInvalid)
	}
	if len(request.Text) > 1<<20 {
		return "", fmt.Errorf("%w: message exceeds 1 MiB", ErrInvalid)
	}
	if len(request.Attachments) > 16 {
		return "", fmt.Errorf("%w: too many attachments", ErrInvalid)
	}
	if len(request.Attachments) > 0 {
		if r.cfg.ResolveAttachments == nil {
			return "", fmt.Errorf("%w: attachment resolver is unavailable", ErrInvalid)
		}
		var err error
		request.Attachments, err = r.cfg.ResolveAttachments(id, request.Attachments)
		if err != nil {
			return "", fmt.Errorf("resolve attachments: %w", err)
		}
	}
	s, err := r.state(id)
	if err != nil {
		return "", err
	}
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	turnID, err := randomID("turn_")
	if err != nil {
		return "", err
	}
	bot, err := r.store.GetBot(id)
	if err != nil {
		return "", err
	}
	if bot.Status == "archived" {
		return "", fmt.Errorf("%w: bot is archived", ErrConflict)
	}
	turnCtx, cancel := context.WithCancel(workCtx)
	t := &runtimeTurn{id: turnID, botID: id, bot: bot, source: request.Source, ctx: turnCtx, cancel: cancel, done: make(chan struct{}), settled: make(chan struct{})}
	s.mu.Lock()
	if s.current != nil || s.compacting {
		s.mu.Unlock()
		cancel()
		return "", ErrBusy
	}
	s.current = t
	s.legacyTurns = append(s.legacyTurns, t)
	s.pendingPermissions = make(map[string]bool)
	s.mu.Unlock()
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		s.mu.Lock()
		s.current = nil
		s.mu.Unlock()
		cancel()
		return "", os.ErrClosed
	}
	r.wg.Add(1)
	r.turns[turnID] = t
	r.mu.Unlock()
	if request.Source == "" {
		request.Source = "web"
	}
	publicAttachments := make([]Attachment, len(request.Attachments))
	copy(publicAttachments, request.Attachments)
	for i := range publicAttachments {
		publicAttachments[i].Path = ""
	}
	if _, err = r.store.AppendEvent(id, turnID, "message", map[string]any{
		"role": "user", "content": request.Text, "attachments": publicAttachments, "source": request.Source,
	}); err == nil {
		_, err = r.store.UpdateBot(id, func(bot *Bot) error { bot.Status = "running"; return nil })
	}
	if err == nil {
		_, err = r.store.AppendEvent(id, turnID, "turn", turnPayload(bot, "running", TurnResult{}))
	}
	if err != nil {
		r.finishTurn(s, t, "error", err)
		r.wg.Done()
		return "", err
	}
	go func() { defer r.wg.Done(); r.runTurn(s, t, request) }()
	return turnID, nil
}

func (r *Runtime) WaitTurn(ctx context.Context, botID, turnID string) (TurnResult, error) {
	r.mu.Lock()
	t := r.turns[turnID]
	r.mu.Unlock()
	if t == nil || t.botID != botID {
		return r.persistedTurn(botID, turnID)
	}
	select {
	case <-t.done:
		t.mu.Lock()
		result := t.result
		t.mu.Unlock()
		return result, nil
	case <-ctx.Done():
		return TurnResult{}, ctx.Err()
	}
}

func (r *Runtime) persistedTurn(botID, turnID string) (TurnResult, error) {
	events, err := r.store.Events(botID, 0)
	if err != nil {
		return TurnResult{}, err
	}
	result := TurnResult{}
	for _, event := range events {
		if event.TurnID != turnID {
			continue
		}
		switch event.Type {
		case "message":
			var message struct{ Role, Content string }
			if json.Unmarshal(event.Data, &message) == nil && message.Role == "assistant" {
				result.Text = message.Content
			}
		case "turn":
			text := result.Text
			if json.Unmarshal(event.Data, &result) == nil {
				result.Text = text
			}
		}
	}
	if result.Status == "" {
		return TurnResult{}, ErrNotFound
	}
	if !terminalStatus(result.Status) {
		return TurnResult{}, fmt.Errorf("%w: turn has not completed", ErrConflict)
	}
	return result, nil
}

func (r *Runtime) Stop(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s, err := r.state(id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	t := s.current
	compacting := s.compacting
	s.mu.Unlock()
	if err := r.cancelPendingGoal(id, "owner_stop"); err != nil {
		return err
	}
	if t == nil {
		if compacting {
			return r.stopCompaction(ctx, s)
		}
		return r.stopIdleSession(s)
	}
	t.mu.Lock()
	t.stopped = true
	t.mu.Unlock()
	t.cancel()
	return nil
}

func (r *Runtime) Permission(ctx context.Context, id, requestID string, result core.PermissionResult) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if result.Behavior != "allow" && result.Behavior != "deny" {
		return fmt.Errorf("%w: permission must be allow or deny", ErrInvalid)
	}
	s, err := r.state(id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	pendingID := requestID
	if !s.pendingPermissions[pendingID] {
		encoded, _ := json.Marshal(requestID)
		if s.pendingPermissions[string(encoded)] {
			pendingID = string(encoded)
		}
	}
	if s.current == nil || !s.pendingPermissions[pendingID] || s.session == nil {
		s.mu.Unlock()
		return fmt.Errorf("%w: permission request is no longer pending", ErrConflict)
	}
	session, t := s.session, s.current
	delete(s.pendingPermissions, pendingID)
	s.mu.Unlock()
	if err := session.RespondPermission(pendingID, result); err != nil {
		s.mu.Lock()
		if s.current == t {
			s.pendingPermissions[pendingID] = true
		}
		s.mu.Unlock()
		return err
	}
	_, err = r.store.AppendEvent(id, t.id, "permission", map[string]any{"requestId": plainRequestID(pendingID), "status": "resolved", "behavior": result.Behavior, "message": result.Message, "updatedInput": result.UpdatedInput})
	return err
}

// Invalidate closes only an idle adapter. The next send resumes the same backend
// thread with fresh role, instructions, skills, model, and effort settings.
func (r *Runtime) Invalidate(id string) error {
	r.mu.Lock()
	s := r.states[id]
	r.mu.Unlock()
	if s == nil {
		return nil
	}
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	s.mu.Lock()
	if s.current != nil || s.compacting {
		s.mu.Unlock()
		return ErrBusy
	}
	session, agent := s.session, s.agent
	s.session, s.agent, s.signature = nil, nil, ""
	s.epoch++
	s.mu.Unlock()
	bot, getErr := r.store.GetBot(id)
	var captureErr error
	if getErr == nil {
		captureErr = r.captureGoalIfChanged(s, session, bot)
	}
	return errors.Join(getErr, captureErr, r.interruptSession(s, session), closeAdapter(session, agent))
}

// WithIdleBot makes a profile/workspace change atomic with respect to accepting
// a turn. mutate must change the store/files only, without calling the runtime.
func (r *Runtime) WithIdleBot(id string, mutate func() error) error {
	return r.withIdleBots([]string{id}, true, mutate)
}

// WithIdleBots locks a shared instruction/skill edit in a stable order. It
// collects state pointers before any bot lock, avoiding nested runtime calls.
func (r *Runtime) WithIdleBots(ids []string, mutate func() error) error {
	return r.withIdleBots(ids, true, mutate)
}

// WithIdleBotAccess protects a short deterministic maintenance operation while
// leaving its adapter and native goal untouched.
func (r *Runtime) WithIdleBotAccess(id string, access func() error) error {
	return r.withIdleBots([]string{id}, false, access)
}

func (r *Runtime) withIdleBots(ids []string, invalidate bool, mutate func() error) error {
	ordered := append([]string(nil), ids...)
	sort.Strings(ordered)
	states := make([]*botRuntime, 0, len(ordered))
	for i, id := range ordered {
		if i > 0 && id == ordered[i-1] {
			continue
		}
		s, err := r.state(id)
		if err != nil {
			return err
		}
		states = append(states, s)
	}
	for _, s := range states {
		s.lifecycle.Lock()
	}
	defer func() {
		for i := len(states) - 1; i >= 0; i-- {
			states[i].lifecycle.Unlock()
		}
	}()
	for _, s := range states {
		s.mu.Lock()
	}
	unlockStates := func() {
		for i := len(states) - 1; i >= 0; i-- {
			states[i].mu.Unlock()
		}
	}
	for _, s := range states {
		if s.current != nil || s.compacting {
			unlockStates()
			return ErrBusy
		}
	}
	if err := mutate(); err != nil {
		unlockStates()
		return err
	}
	type adapter struct {
		state   *botRuntime
		session core.AgentSession
		agent   core.Agent
	}
	adapters := []adapter{}
	if invalidate {
		for _, s := range states {
			adapters = append(adapters, adapter{s, s.session, s.agent})
			s.session, s.agent, s.signature = nil, nil, ""
			s.epoch++
		}
	}
	unlockStates()
	var err error
	for _, a := range adapters {
		updated, getErr := r.store.GetBot(a.state.id)
		if getErr == nil {
			err = errors.Join(err, r.captureGoalIfChanged(a.state, a.session, updated))
		} else {
			err = errors.Join(err, getErr)
		}
		err = errors.Join(err, r.interruptSession(a.state, a.session), closeAdapter(a.session, a.agent))
	}
	return err
}

func (r *Runtime) Close() error {
	r.closeOnce.Do(func() {
		r.mu.Lock()
		r.closed = true
		states := make([]*botRuntime, 0, len(r.states))
		for _, s := range r.states {
			states = append(states, s)
		}
		for _, t := range r.turns {
			t.cancel()
		}
		r.mu.Unlock()
		// Interrupt only this product's threads before detaching from the shared
		// app-server. Other clients and the shared daemon keep running.
		for _, s := range states {
			s.lifecycle.Lock()
			s.mu.Lock()
			session, agent, t := s.session, s.agent, s.current
			compacting := s.compacting
			s.mu.Unlock()
			if compacting {
				r.closeErr = errors.Join(r.closeErr, r.stopCompactionLocked(context.Background(), s))
			} else {
				r.closeErr = errors.Join(r.closeErr, r.interruptSession(s, session))
			}
			if t != nil {
				r.closeErr = errors.Join(r.closeErr, r.stopBackgroundProcesses(s, session, t, false))
			}
			s.mu.Lock()
			s.session, s.agent = nil, nil
			s.epoch++
			s.mu.Unlock()
			r.closeErr = errors.Join(r.closeErr, closeAdapter(session, agent))
			s.lifecycle.Unlock()
		}
		r.cancel()
		r.wg.Wait()
		r.readerWG.Wait()
		r.nativeMu.Lock()
		r.nativeClosing = true
		r.nativeMu.Unlock()
		select {
		case r.nativeWake <- struct{}{}:
		default:
		}
		<-r.nativeDone
	})
	return r.closeErr
}

func closeAdapter(session core.AgentSession, agent core.Agent) error {
	var err error
	if session != nil {
		err = session.Close()
	}
	if agent != nil {
		err = errors.Join(err, agent.Stop())
	}
	return err
}

func terminalStatus(status string) bool {
	return status == "completed" || status == "error" || status == "stopped" || status == "interrupted"
}

func turnPayload(bot Bot, status string, result TurnResult) map[string]any {
	return map[string]any{"status": status, "backend": bot.Backend, "model": bot.Model, "effort": bot.Effort,
		"outputTokens": result.OutputTokens, "generationMs": result.GenerationMS,
		"tokensPerSecond": result.TokensPerSec, "throughputEstimated": true, "error": result.Error}
}

func (r *Runtime) logJournalError(botID, turnID string, err error) {
	if err != nil {
		slog.Error("bots: persist runtime event", "bot", botID, "turn", turnID, "error", err)
	}
}
