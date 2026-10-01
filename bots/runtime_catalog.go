package bots

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// Capabilities discovers models through the same configured harnesses used by
// bots. Control requests never send a prompt or spend inference tokens.
func (r *Runtime) Capabilities(ctx context.Context) (Capabilities, error) {
	return r.capabilities(ctx, "")
}

// CapabilitiesForBot discovers the selected bot's model-specific settings.
// In particular Pi reports thinking levels for that session's current model.
func (r *Runtime) CapabilitiesForBot(ctx context.Context, botID string) (Capabilities, error) {
	return r.capabilities(ctx, botID)
}

func (r *Runtime) capabilities(ctx context.Context, botID string) (Capabilities, error) {
	ctx, done, err := r.catalogControlContext(ctx)
	if err != nil {
		return Capabilities{}, err
	}
	defer done()
	var selected *Bot
	if botID != "" {
		bot, err := r.store.GetBot(botID)
		if err != nil {
			return Capabilities{}, err
		}
		if bot.Status == "archived" {
			return Capabilities{}, fmt.Errorf("%w: bot is archived", ErrConflict)
		}
		selected = &bot
	}
	result := Capabilities{Models: []Model{}, Voice: r.cfg.VoiceAvailable, Backends: make(map[string]BackendCapabilities)}
	type discovery struct {
		backend string
		models  []Model
		caps    BackendCapabilities
	}
	results := make(chan discovery, 2)
	for _, backend := range []string{"codex", "pi"} {
		go func(backend string) {
			queryCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			item := discovery{backend: backend}
			var lease catalogLease
			var err error
			if selected != nil && selected.Backend == backend {
				lease, err = r.botCatalogSession(queryCtx, *selected)
			} else {
				lease, err = r.catalogSession(queryCtx, backend)
			}
			if err == nil {
				if backend == "codex" {
					item.models, err = codexCatalog(queryCtx, lease.rpc)
					if err == nil {
						item.caps.Subagents = true
						if threadID := lease.session.CurrentSessionID(); threadID != "" {
							var ignored any
							item.caps.Goals = lease.rpc.RPC(queryCtx, "thread/goal/get", map[string]any{"threadId": threadID}, &ignored) == nil
						}
					}
				} else {
					item.models, err = piCatalog(queryCtx, lease.rpc)
				}
				lease.release()
			}
			item.caps.Available = err == nil
			if err != nil {
				item.caps.Reason = err.Error()
			}
			results <- item
		}(backend)
	}
	// Keep the catalog ordering stable even if a backend responds first.
	discovered := make(map[string]discovery, 2)
	for range 2 {
		item := <-results
		discovered[item.backend] = item
	}
	if err := ctx.Err(); err != nil {
		return Capabilities{}, err
	}
	for _, backend := range []string{"codex", "pi"} {
		item := discovered[backend]
		result.Backends[backend] = item.caps
		result.Models = append(result.Models, item.models...)
	}
	return result, nil
}

// Goal exposes only the three native goal methods and always supplies the
// current session's owned thread ID, regardless of fields sent by the client.
func (r *Runtime) Goal(ctx context.Context, botID, method string, fields map[string]any) (any, error) {
	if method != "get" && method != "set" && method != "clear" {
		return nil, fmt.Errorf("%w: unsupported goal method", ErrInvalid)
	}
	ctx, done, err := r.catalogControlContext(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	bot, err := r.store.GetBot(botID)
	if err != nil {
		return nil, err
	}
	if bot.Backend != "codex" {
		return nil, fmt.Errorf("%w: native goals require Codex", ErrInvalid)
	}
	if method == "set" && fields["status"] == "active" {
		if err := r.ensureGoalHandoff(ctx, botID); err != nil {
			return nil, err
		}
		bot, err = r.store.GetBot(botID)
		if err != nil {
			return nil, err
		}
	}
	lease, err := r.botCatalogSession(ctx, bot)
	if err != nil {
		return nil, err
	}
	defer lease.release()
	threadID := lease.session.CurrentSessionID()
	if threadID == "" {
		return nil, fmt.Errorf("%w: Codex session has no thread ID", ErrConflict)
	}
	params := map[string]any{"threadId": threadID}
	if method == "set" {
		for _, key := range []string{"objective", "status", "tokenBudget"} {
			if value, ok := fields[key]; ok {
				params[key] = value
			}
		}
	}
	r.flushNative()
	var baseline uint64
	if journal, ok := r.store.(interface{ Cursor() uint64 }); ok {
		baseline = journal.Cursor()
	}
	var result any
	if err := lease.rpc.RPC(ctx, "thread/goal/"+method, params, &result); err != nil {
		return nil, err
	}
	r.flushNative()
	if method == "get" {
		// Updates arriving during or after the RPC must remain replayable.
		// Reading the cursor after the response would silently cover them.
		response, ok := result.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("Codex goal query returned a non-object response")
		}
		response["cursor"] = baseline
		return response, nil
	}
	var controlErr error
	if method == "set" {
		// A native goal materializes the thread even before its first prompt.
		controlErr = r.materializeThread(botID, "codex", threadID)
	}
	controlErr = errors.Join(controlErr, r.cancelPendingGoal(botID, "owner_goal_"+method))
	if controlErr != nil {
		return nil, controlErr
	}
	// Native notifications own goal state. The RPC response is an action
	// receipt and can already be older than a notification received during it.
	payload := map[string]any{"backend": "codex", "method": method, "threadId": threadID, "result": result}
	if _, err := r.store.AppendEvent(botID, "", "goal_action", payload); err != nil {
		return nil, err
	}
	if response, ok := result.(map[string]any); ok {
		response["cursor"] = baseline
	}
	return result, nil
}

// Shutdown cancels in-flight discovery and waits until its temporary adapters
// and lifecycle leases have been released before closing the journal.
func (r *Runtime) catalogControlContext(ctx context.Context) (context.Context, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, nil, os.ErrClosed
	}
	r.wg.Add(1)
	r.mu.Unlock()
	queryCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	stop := context.AfterFunc(r.ctx, cancel)
	return queryCtx, func() { stop(); cancel(); r.wg.Done() }, nil
}

type catalogLease struct {
	session core.AgentSession
	rpc     core.AgentRPCSession
	release func()
}

func (r *Runtime) catalogSession(ctx context.Context, backend string) (catalogLease, error) {
	var chosen *Bot
	for _, bot := range r.store.ListBots() {
		if bot.Backend != backend || bot.Status == "archived" {
			continue
		}
		candidate := bot
		if chosen == nil || candidate.Chief {
			chosen = &candidate
		}
		if candidate.Chief {
			break
		}
	}
	if chosen != nil {
		return r.botCatalogSession(ctx, *chosen)
	}
	return r.temporaryCatalogSession(ctx, backend)
}

func (r *Runtime) botCatalogSession(ctx context.Context, bot Bot) (catalogLease, error) {
	if bot.Status == "archived" {
		return catalogLease{}, fmt.Errorf("%w: bot is archived", ErrConflict)
	}
	s, err := r.state(bot.ID)
	if err != nil {
		return catalogLease{}, err
	}
	for range 3 {
		if err := ctx.Err(); err != nil {
			return catalogLease{}, err
		}
		s.lifecycle.Lock()
		currentBot, err := r.store.GetBot(bot.ID)
		if err != nil || currentBot.Status == "archived" || currentBot.Backend != bot.Backend {
			s.lifecycle.Unlock()
			if err != nil {
				return catalogLease{}, err
			}
			return catalogLease{}, fmt.Errorf("%w: bot profile changed during control request", ErrConflict)
		}
		bot = currentBot
		s.mu.Lock()
		session, backend := s.session, s.backend
		busy := s.current != nil
		s.mu.Unlock()
		if session != nil && session.Alive() && backend == bot.Backend {
			rpc, ok := session.(core.AgentRPCSession)
			if !ok {
				s.lifecycle.Unlock()
				return catalogLease{}, fmt.Errorf("%s control RPC is unavailable", bot.Backend)
			}
			return catalogLease{session: session, rpc: rpc, release: s.lifecycle.Unlock}, nil
		}
		s.lifecycle.Unlock()
		if busy && backend != "" && backend != bot.Backend {
			return catalogLease{}, ErrBusy
		}
		// ensureSession owns this same lifecycle mutex. Reacquire it above to
		// verify the installed adapter and hold it throughout the control RPC.
		if _, err := r.ensureSession(ctx, s, bot); err != nil {
			return catalogLease{}, err
		}
	}
	return catalogLease{}, fmt.Errorf("%w: bot session changed during discovery", ErrConflict)
}

func (r *Runtime) temporaryCatalogSession(ctx context.Context, backend string) (catalogLease, error) {
	workDir := filepath.Join(r.store.Root(), "catalog", backend)
	if err := os.MkdirAll(workDir, 0700); err != nil {
		return catalogLease{}, err
	}
	opts := make(map[string]any)
	mergeOptions(opts, r.cfg.AgentOptions[backend])
	opts["work_dir"], opts["native_events"] = workDir, true
	if backend == "codex" {
		if err := r.applyCodexConnection(opts); err != nil {
			return catalogLease{}, err
		}
	} else {
		opts["rpc"] = true
		args, err := runtimeArgs(opts["cli_args"])
		if err != nil {
			return catalogLease{}, err
		}
		opts["cli_args"] = append(args, "--no-session", "--offline", "--no-extensions", "--no-skills")
	}
	agent, err := r.cfg.AgentFactory(backend, opts)
	if err != nil {
		return catalogLease{}, err
	}
	session, err := agent.StartSession(ctx, "")
	if err != nil {
		_ = agent.Stop()
		return catalogLease{}, err
	}
	rpc, ok := session.(core.AgentRPCSession)
	if !ok {
		_ = closeAdapter(session, agent)
		return catalogLease{}, fmt.Errorf("%s control RPC is unavailable", backend)
	}
	// A temporary session has no bot journal, but its legacy event channel must
	// still drain so startup and extension events cannot stall the reader.
	go func() {
		for range session.Events() {
		}
	}()
	return catalogLease{session: session, rpc: rpc, release: func() { _ = closeAdapter(session, agent) }}, nil
}

func codexCatalog(ctx context.Context, rpc core.AgentRPCSession) ([]Model, error) {
	models := []Model{}
	cursor := ""
	seen := make(map[string]bool)
	for range 100 {
		params := map[string]any{"limit": 100, "includeHidden": false}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var page struct {
			Data []struct {
				ID                        string `json:"id"`
				Model                     string `json:"model"`
				DisplayName               string `json:"displayName"`
				Name                      string `json:"name"`
				SupportedReasoningEfforts []struct {
					ReasoningEffort string `json:"reasoningEffort"`
				} `json:"supportedReasoningEfforts"`
			} `json:"data"`
			NextCursor string `json:"nextCursor"`
		}
		if err := rpc.RPC(ctx, "model/list", params, &page); err != nil {
			return nil, err
		}
		for _, item := range page.Data {
			id := catalogText(item.Model, item.ID)
			if id == "" || seen["model:"+id] {
				continue
			}
			seen["model:"+id] = true
			model := Model{ID: id, Name: catalogText(item.DisplayName, item.Name, id), Backend: "codex", Efforts: []string{}}
			for _, effort := range item.SupportedReasoningEfforts {
				if effort.ReasoningEffort != "" {
					model.Efforts = append(model.Efforts, effort.ReasoningEffort)
				}
			}
			models = append(models, model)
		}
		if page.NextCursor == "" {
			return models, nil
		}
		if seen["cursor:"+page.NextCursor] {
			return nil, fmt.Errorf("Codex model catalog repeated a pagination cursor")
		}
		seen["cursor:"+page.NextCursor], cursor = true, page.NextCursor
	}
	return nil, fmt.Errorf("Codex model catalog exceeds 100 pages")
}

func piCatalog(ctx context.Context, rpc core.AgentRPCSession) ([]Model, error) {
	type piModel struct {
		ID       string `json:"id"`
		Provider string `json:"provider"`
		Name     string `json:"name"`
	}
	var catalog struct {
		Models []piModel `json:"models"`
	}
	if err := rpc.RPC(ctx, "get_available_models", nil, &catalog); err != nil {
		return nil, err
	}
	var state struct {
		Model *piModel `json:"model"`
	}
	var levels struct {
		Levels []string `json:"levels"`
	}
	// Pi's thinking-level query describes only the selected model. Reporting
	// those levels on the entire provider catalog would invent capabilities.
	stateErr := rpc.RPC(ctx, "get_state", nil, &state)
	levelsErr := rpc.RPC(ctx, "get_available_thinking_levels", nil, &levels)
	models := []Model{}
	seen := make(map[string]bool)
	for _, item := range catalog.Models {
		if item.ID == "" {
			continue
		}
		id := item.ID
		if item.Provider != "" {
			id = item.Provider + "/" + id
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		model := Model{ID: id, Name: catalogText(item.Name, item.ID), Backend: "pi", Efforts: []string{}}
		if stateErr == nil && levelsErr == nil && state.Model != nil && state.Model.Provider == item.Provider && state.Model.ID == item.ID {
			model.Efforts = append(model.Efforts, levels.Levels...)
		}
		models = append(models, model)
	}
	return models, nil
}

func catalogText(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
