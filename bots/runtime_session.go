package bots

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func (r *Runtime) agentOptions(bot Bot) (map[string]any, string, error) {
	opts := map[string]any{}
	mergeOptions(opts, r.cfg.AgentOptions[bot.Backend])
	if r.cfg.SessionOptions != nil {
		extra, err := r.cfg.SessionOptions(bot.ID)
		if err != nil {
			return nil, "", err
		}
		mergeOptions(opts, extra)
	}
	instructions := "You are the persistent Connect Bots bot " + bot.Name + " (" + bot.ID + ").\nRole: " + bot.Role + "\n"
	instructions += "Keep durable results in your workspace. Use tmp/ for disposable work. Delegate bounded tasks, inspect the actual result, and never recursively delegate to an active caller.\n"
	if r.cfg.Instructions != nil {
		user, err := r.cfg.Instructions(bot.ID)
		if err != nil {
			return nil, "", err
		}
		instructions += "\n" + user
	}
	opts["work_dir"], opts["model"], opts["native_events"] = bot.WorkDir, bot.Model, true
	if bot.Backend == "codex" {
		if err := r.applyCodexConnection(opts); err != nil {
			return nil, "", err
		}
		opts["reasoning_effort"], opts["developer_instructions"] = bot.Effort, instructions
		// Always include the option, including the empty automatic selection, so
		// resuming a thread can clear a previously explicit service tier.
		opts["service_tier"] = bot.ServiceTier
		// Native shared-user and cwd AGENTS remain the harness's responsibility.
		if opts["mode"] == nil {
			opts["mode"] = "yolo"
		}
	} else if bot.Backend == "pi" {
		opts["rpc"], opts["thinking"] = true, bot.Effort
		opts["session_dir"] = filepath.Join(bot.WorkDir, "sessions", "pi")
		if err := os.MkdirAll(opts["session_dir"].(string), 0700); err != nil {
			return nil, "", err
		}
		args, err := runtimeArgs(opts["cli_args"])
		if err != nil {
			return nil, "", err
		}
		args = append(args, "--approve", "--append-system-prompt", instructions)
		if r.cfg.PiExtensionPath != "" {
			args = append(args, "--extension", r.cfg.PiExtensionPath)
		}
		opts["cli_args"] = args
		env := runtimeEnv(opts["env"])
		env["CONNECT_BOTS_API_URL"], env["CONNECT_BOTS_BOT_ID"], env["CONNECT_BOTS_INTERNAL_TOKEN"] = r.cfg.InternalURL, bot.ID, r.cfg.InternalToken
		if env["DEEPSEEK_SEARCH_MODEL"] == "" {
			env["DEEPSEEK_SEARCH_MODEL"] = "deepseek-flash"
		}
		opts["env"] = env
	} else {
		return nil, "", fmt.Errorf("%w: unsupported backend", ErrInvalid)
	}
	encoded, err := json.Marshal(opts)
	if err != nil {
		return nil, "", err
	}
	digest := sha256.Sum256(encoded)
	return opts, hex.EncodeToString(digest[:]), nil
}

func mergeOptions(dst, src map[string]any) {
	for key, value := range src {
		if key == "cli_args" {
			previous, _ := runtimeArgs(dst[key])
			extra, err := runtimeArgs(value)
			if err == nil {
				dst[key] = append(previous, extra...)
				continue
			}
		}
		if key == "app_server_config" {
			config := map[string]any{}
			if previous, ok := dst[key].(map[string]any); ok {
				for k, v := range previous {
					config[k] = v
				}
			}
			if extra, ok := value.(map[string]any); ok {
				for k, v := range extra {
					config[k] = v
				}
				dst[key] = config
				continue
			}
		}
		dst[key] = value
	}
}

func runtimeArgs(value any) ([]string, error) {
	switch args := value.(type) {
	case nil:
		return nil, nil
	case []string:
		return append([]string(nil), args...), nil
	case []any:
		result := make([]string, len(args))
		for i, value := range args {
			var ok bool
			result[i], ok = value.(string)
			if !ok {
				return nil, fmt.Errorf("cli_args must contain strings")
			}
		}
		return result, nil
	default:
		return nil, fmt.Errorf("cli_args must be a list of strings")
	}
}

func runtimeEnv(value any) map[string]string {
	env := make(map[string]string)
	switch previous := value.(type) {
	case map[string]string:
		for key, value := range previous {
			env[key] = value
		}
	case map[string]any:
		for key, value := range previous {
			if text, ok := value.(string); ok {
				env[key] = text
			}
		}
	}
	return env
}

func (r *Runtime) ensureSession(ctx context.Context, s *botRuntime, bot Bot) (core.AgentSession, error) {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	opts, signature, err := r.agentOptions(bot)
	if err != nil {
		return nil, fmt.Errorf("bot options: %w", err)
	}
	configSignature := sessionConfigSignature(bot, opts)
	s.mu.Lock()
	if s.session != nil && s.session.Alive() && s.signature == signature && s.configSignatures[bot.Backend] == configSignature {
		session := s.session
		s.mu.Unlock()
		return session, nil
	}
	if s.session != nil && s.session.Alive() && s.backend == "codex" && bot.Backend == "codex" &&
		s.configSignatures[bot.Backend] == configSignature && s.bot.Model == bot.Model && s.bot.Effort == bot.Effort && s.bot.ServiceTier != bot.ServiceTier {
		// A tier-only change is a native setting update on this loaded thread.
		// Preserve its context, goal and adapter instead of reconnecting it.
		session := s.session
		s.mu.Unlock()
		rpc, ok := session.(core.AgentRPCSession)
		if !ok {
			return nil, fmt.Errorf("Codex service tier control is unavailable")
		}
		var tier any
		if bot.ServiceTier != "" {
			tier = bot.ServiceTier
		}
		if err := rpc.RPC(ctx, "thread/settings/update", map[string]any{"threadId": session.CurrentSessionID(), "serviceTier": tier}, nil); err != nil {
			return nil, fmt.Errorf("update Codex service tier: %w", err)
		}
		s.mu.Lock()
		s.bot, s.signature = bot, signature
		s.mu.Unlock()
		return session, nil
	}
	previousSession, previousAgent := s.session, s.agent
	s.session, s.agent = nil, nil
	s.epoch++
	epoch := s.epoch
	s.needsHandoff = s.lastBackend != "" && s.lastBackend != bot.Backend
	refresh := bot.Backend == "codex" && bot.Threads[bot.Backend] != "" && s.configSignatures[bot.Backend] != "" && s.configSignatures[bot.Backend] != configSignature
	s.needsRefreshHandoff = refresh
	s.handoffReason = ""
	if refresh {
		s.handoffReason = "configuration_changed"
	}
	goalChecked, pendingGoal := s.goalRefreshChecked, s.pendingGoal
	s.goalRefreshChecked = false
	s.mu.Unlock()
	if err := r.interruptSession(s, previousSession); err != nil {
		return nil, err
	}
	if err := closeAdapter(previousSession, previousAgent); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.bot, s.backend = bot, bot.Backend
	s.mu.Unlock()
	agent, err := r.cfg.AgentFactory(bot.Backend, opts)
	if err != nil {
		return nil, err
	}
	// After a process restart or a backend round-trip, the old loaded Codex
	// adapter may no longer be attached. Read its own paused goal before the
	// fresh configuration thread replaces that backend ID.
	if refresh && !goalChecked && pendingGoal == nil {
		old, err := agent.StartSession(r.ctx, bot.Threads["codex"])
		if err != nil {
			_ = agent.Stop()
			return nil, fmt.Errorf("read previous Codex goal: %w", err)
		}
		captureErr := r.captureGoal(ctx, s, old)
		interruptErr := r.interruptSession(s, old)
		closeErr := old.Close()
		if captureErr != nil || interruptErr != nil || closeErr != nil {
			_ = agent.Stop()
			return nil, fmt.Errorf("close previous Codex goal session: %w", errors.Join(captureErr, interruptErr, closeErr))
		}
	}
	if observer, ok := agent.(core.NativeEventObserver); ok {
		observer.SetNativeEventHandler(func(event core.NativeEvent) { r.observeNative(s, epoch, event) })
	}
	if dynamic, ok := agent.(core.DynamicToolAgent); ok {
		dynamic.SetDynamicTools(botToolDefinitions(bot.Chief), func(ctx context.Context, call core.DynamicToolCall) (core.DynamicToolResult, error) {
			return r.DynamicTool(ctx, bot.ID, call.Tool, call.Arguments, call.CallID)
		})
	}
	// Session lifetime belongs to the service, not a browser or an individual
	// turn. Explicit cancel interrupts a turn and then detaches this adapter.
	resumeID := bot.Threads[bot.Backend]
	lostPiSession := false
	if bot.Backend == "pi" && resumeID != "" {
		known, err := agent.ListSessions(ctx)
		if err != nil {
			_ = agent.Stop()
			return nil, fmt.Errorf("check own Pi session: %w", err)
		}
		found := false
		for _, info := range known {
			if info.ID == resumeID {
				found = true
				break
			}
		}
		if !found {
			resumeID, lostPiSession = "", true
			s.mu.Lock()
			s.needsRefreshHandoff, s.handoffReason = true, "backend_session_missing"
			s.mu.Unlock()
		}
	}
	if refresh {
		resumeID = ""
	}
	session, err := agent.StartSession(r.ctx, resumeID)
	if err != nil {
		_ = agent.Stop()
		return nil, err
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		_ = closeAdapter(session, agent)
		return nil, os.ErrClosed
	}
	r.readerWG.Add(1)
	r.mu.Unlock()
	s.mu.Lock()
	s.session, s.agent, s.signature, s.threadID = session, agent, signature, session.CurrentSessionID()
	s.configSignatures[bot.Backend] = configSignature
	s.mu.Unlock()
	if err := r.prepareHandoff(s, bot, session); err != nil {
		r.readerWG.Done()
		_ = closeAdapter(session, agent)
		return nil, err
	}
	// Catalog-only and freshly rotated threads can still be unmaterialized.
	// Commit the durable thread pointer AND its config digest together only
	// after actual work or a successful native goal mutation has persisted it.
	content := "Session connected."
	if refresh {
		content = "Instructions or skills were updated. A new Codex session was created with the conversation history carried over."
	}
	if lostPiSession {
		content = "The Pi session has not been saved yet, or its file is missing. A new session was created while preserving the bot's visible history."
	}
	if _, err := r.store.AppendEvent(bot.ID, "", "system", map[string]any{"content": content, "backend": bot.Backend, "pendingConfigSignature": configSignature, "threadId": session.CurrentSessionID(), "previousThreadId": bot.Threads[bot.Backend]}); err != nil {
		r.readerWG.Done()
		_ = closeAdapter(session, agent)
		return nil, err
	}
	go func() { defer r.readerWG.Done(); r.readSession(s, epoch, session) }()
	if bot.Backend == "codex" {
		// Force a thread-scoped rescan after managed skill links have changed.
		if rpc, ok := session.(core.AgentRPCSession); ok {
			var ignored any
			scanCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			err := rpc.RPC(scanCtx, "skills/list", map[string]any{"cwds": []string{bot.WorkDir}, "forceReload": true}, &ignored)
			cancel()
			if err != nil {
				r.logJournalError(bot.ID, "", fmt.Errorf("refresh skills: %w", err))
			}
		}
		if err := r.restoreGoalCarry(ctx, s, session); err != nil {
			r.logJournalError(bot.ID, "", fmt.Errorf("defer paused goal carry until materialized turn: %w", err))
			_, journalErr := r.store.AppendEvent(bot.ID, "", "system", map[string]any{"content": "The saved goal remains paused and will be carried over after the new session's first response."})
			r.logJournalError(bot.ID, "", journalErr)
		}
	}
	return session, nil
}

func (r *Runtime) rememberThread(botID, backend, threadID string) error {
	if threadID == "" {
		return nil
	}
	bot, err := r.store.GetBot(botID)
	if err != nil {
		return err
	}
	if bot.Threads[backend] == threadID {
		return nil
	}
	_, err = r.store.UpdateBot(botID, func(bot *Bot) error {
		if bot.Threads == nil {
			bot.Threads = make(map[string]string)
		}
		bot.Threads[backend] = threadID
		return nil
	})
	return err
}

// A config digest must never point at a catalog-only thread. Pointer first,
// then committed digest, so a restart between writes safely rotates again.
func (r *Runtime) materializeThread(botID, backend, threadID string) error {
	if err := r.rememberThread(botID, backend, threadID); err != nil {
		return err
	}
	r.mu.Lock()
	s := r.states[botID]
	r.mu.Unlock()
	if s == nil {
		return nil
	}
	s.mu.Lock()
	digest := s.configSignatures[backend]
	committed := s.committedSignatures[backend]
	s.mu.Unlock()
	if digest == "" || digest == committed {
		return nil
	}
	if _, err := r.store.AppendEvent(botID, "", "system", map[string]any{"content": "Session history saved.", "backend": backend, "threadId": threadID, "configSignature": digest}); err != nil {
		return err
	}
	s.mu.Lock()
	s.committedSignatures[backend] = digest
	s.mu.Unlock()
	return nil
}

func (r *Runtime) runTurn(s *botRuntime, t *runtimeTurn, request MessageRequest) {
	session, err := r.ensureSession(t.ctx, s, t.bot)
	if err != nil {
		status := "error"
		if t.ctx.Err() != nil {
			status = r.cancellationStatus()
			err = nil
		}
		r.finishTurn(s, t, status, err)
		return
	}
	prompt := request.Text
	s.mu.Lock()
	handoff, refresh, previous, refreshReason := s.needsHandoff, s.needsRefreshHandoff, s.lastBackend, s.handoffReason
	previousThreads := t.bot.Threads
	if s.pendingHandoff != nil {
		previous = s.pendingHandoff.From
		previousThreads = s.pendingHandoff.PreviousThreads
	}
	s.mu.Unlock()
	if handoff || refresh {
		contextText, err := r.recentContext(t.botID, t.id)
		if err != nil {
			r.finishTurn(s, t, "error", err)
			return
		}
		reason := "backend_changed"
		explanation := "This conversation switched from " + previous + " to " + t.bot.Backend + ". Previous backend threads remain separate."
		if refresh {
			reason, explanation = "configuration_changed", "Instructions or skills changed. This is a fresh Codex thread with the same visible bot conversation."
		}
		if refreshReason == "backend_session_missing" {
			reason, explanation = refreshReason, "The previous Pi session was not written yet or its file is missing. The bot's visible conversation is preserved in this explicit handoff."
		}
		prompt = explanation + " The following messages are conversation history; do not repeat old actions unless the current owner message asks for them. Continue from this recent visible context:\n" + contextText + "\nCurrent owner message:\n" + prompt
		updated, _ := r.store.GetBot(t.botID)
		if _, err := r.store.AppendEvent(t.botID, t.id, "handoff", map[string]any{"from": previous, "to": t.bot.Backend, "threads": updated.Threads, "previousThreads": previousThreads, "reason": reason, "content": contextText}); err != nil {
			r.finishTurn(s, t, "error", err)
			return
		}
	}
	images, files, err := runtimeAttachments(request.Attachments)
	if err == nil {
		err = t.ctx.Err()
	}
	if err == nil {
		err = session.Send(prompt, t.id, images, files)
	}
	if err == nil {
		err = r.acceptHandoff(s, t, session)
	}
	if err == nil && t.bot.Backend == "codex" {
		err = r.materializeThread(t.botID, t.bot.Backend, session.CurrentSessionID())
	}
	if err != nil {
		status := "error"
		if t.ctx.Err() != nil {
			status = r.cancellationStatus()
			err = r.detachStopped(s, session, t)
			if err != nil {
				status = "error"
			}
		}
		r.finishTurn(s, t, status, err)
		return
	}
	r.awaitTurn(s, t, session)
}

func sessionConfigSignature(bot Bot, options map[string]any) string {
	config := map[string]any{}
	for key, value := range options {
		switch key {
		case "model", "reasoning_effort", "service_tier", "thinking", "app_server_url":
			continue
		}
		config[key] = value
	}
	// Native automatic project instructions are not duplicated in developer
	// instructions, but edits must force a fresh loaded Codex thread too.
	projectInstructions, _ := os.ReadFile(filepath.Join(bot.WorkDir, "AGENTS.md"))
	config["projectInstructions"] = string(projectInstructions)
	config["dynamicTools"] = botToolDefinitions(bot.Chief)
	encoded, _ := json.Marshal(config)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func (r *Runtime) awaitTurn(s *botRuntime, t *runtimeTurn, session core.AgentSession) {
	select {
	case <-t.settled:
		if t.ctx.Err() != nil {
			stopErr := r.detachStopped(s, session, t)
			status := r.cancellationStatus()
			if stopErr != nil {
				status = "error"
			}
			r.finishTurn(s, t, status, stopErr)
			return
		}
		if t.bot.Backend == "codex" {
			s.lifecycle.Lock()
			ctx, cancel := context.WithTimeout(r.ctx, 10*time.Second)
			err := r.restoreGoalCarry(ctx, s, session)
			cancel()
			s.lifecycle.Unlock()
			if err != nil {
				r.logJournalError(t.botID, t.id, fmt.Errorf("restore paused goal after turn: %w", err))
			}
		}
		t.mu.Lock()
		message, nativeStatus := t.err, t.nativeStatus
		t.mu.Unlock()
		var err error
		status := "completed"
		if nativeStatus == "interrupted" {
			status = "interrupted"
		}
		if nativeStatus == "failed" && message == "" {
			message = "Agent turn failed."
		}
		if message != "" {
			status, err = "error", fmt.Errorf("%s", message)
		}
		r.finishTurn(s, t, status, err)
	case <-t.ctx.Done():
		stopErr := r.detachStopped(s, session, t)
		status := r.cancellationStatus()
		t.mu.Lock()
		failure := t.err
		t.mu.Unlock()
		var err error = stopErr
		if stopErr != nil {
			status = "error"
		}
		if failure != "" {
			status, err = "error", fmt.Errorf("%s", failure)
		}
		r.finishTurn(s, t, status, err)
	}
}

func (r *Runtime) cancellationStatus() string {
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return "interrupted"
	}
	return "stopped"
}

func (r *Runtime) detachStopped(s *botRuntime, session core.AgentSession, t *runtimeTurn) error {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	s.mu.Lock()
	if s.session != session {
		s.mu.Unlock()
		return nil
	}
	agent := s.agent
	s.session, s.agent, s.signature = nil, nil, ""
	s.epoch++
	s.mu.Unlock()
	interruptErr := r.interruptSession(s, session)
	t.mu.Lock()
	explicit := t.stopped
	t.mu.Unlock()
	processErr := r.stopBackgroundProcesses(s, session, t, explicit)
	closeErr := closeAdapter(session, agent)
	return errors.Join(interruptErr, processErr, closeErr)
}

func (r *Runtime) interruptSession(s *botRuntime, session core.AgentSession) error {
	if session == nil || !session.Alive() {
		return nil
	}
	if canceller, ok := session.(core.AgentSessionCanceller); ok {
		return canceller.CancelTurn()
	}
	rpc, ok := session.(core.AgentRPCSession)
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s.mu.Lock()
	backend := s.backend
	current := s.current
	s.mu.Unlock()
	if backend == "pi" {
		return rpc.RPC(ctx, "abort", nil, nil)
	}
	if backend != "codex" {
		return nil
	}
	threadID := session.CurrentSessionID()
	if threadID == "" {
		return nil
	}
	// An active native goal can start another turn after an interrupt. Pause it
	// first so stopping/detaching never leaves invisible remote work running.
	var goal struct {
		Goal map[string]any `json:"goal"`
	}
	if err := rpc.RPC(ctx, "thread/goal/get", map[string]any{"threadId": threadID}, &goal); err == nil && goal.Goal != nil && goal.Goal["status"] == "active" {
		var paused any
		if err := rpc.RPC(ctx, "thread/goal/set", map[string]any{"threadId": threadID, "status": "paused"}, &paused); err != nil {
			return err
		}
		_, err := r.store.AppendEvent(s.id, "", "goal_action", map[string]any{"backend": "codex", "method": "set", "threadId": threadID, "result": paused})
		r.logJournalError(s.id, "", err)
	}
	s.mu.Lock()
	current = s.current
	s.mu.Unlock()
	if current == nil {
		return nil
	}
	current.mu.Lock()
	turnID, completed := current.providerTurn, current.nativeCompleted
	current.mu.Unlock()
	if turnID == "" || completed {
		return nil
	}
	return rpc.RPC(ctx, "turn/interrupt", map[string]any{"threadId": threadID, "turnId": turnID}, nil)
}

func runtimeAttachments(attachments []Attachment) ([]core.ImageAttachment, []core.FileAttachment, error) {
	var images []core.ImageAttachment
	var files []core.FileAttachment
	for _, attachment := range attachments {
		info, err := os.Stat(attachment.Path)
		if err != nil {
			return nil, nil, err
		}
		if !info.Mode().IsRegular() || info.Size() > 25<<20 {
			return nil, nil, fmt.Errorf("invalid attachment file")
		}
		data, err := os.ReadFile(attachment.Path)
		if err != nil {
			return nil, nil, err
		}
		if strings.HasPrefix(attachment.MimeType, "image/") {
			images = append(images, core.ImageAttachment{MimeType: attachment.MimeType, Data: data, FileName: attachment.Name})
		} else {
			files = append(files, core.FileAttachment{MimeType: attachment.MimeType, Data: data, FileName: attachment.Name})
		}
	}
	return images, files, nil
}

func (r *Runtime) recentContext(botID, currentTurn string) (string, error) {
	events, err := r.store.Events(botID, 0)
	if err != nil {
		return "", err
	}
	messages := []string{}
	total := 0
	for i := len(events) - 1; i >= 0 && len(messages) < 12 && total < 24_000; i-- {
		if events[i].Type != "message" || events[i].TurnID == currentTurn {
			continue
		}
		var message struct{ Role, Content string }
		if json.Unmarshal(events[i].Data, &message) != nil || message.Content == "" {
			continue
		}
		text := message.Role + ": " + message.Content
		if len(text) > 8_000 {
			text = text[:8_000] + "\n[older text truncated for handoff]"
		}
		messages = append(messages, text)
		total += len(text)
	}
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}
	return strings.Join(messages, "\n\n"), nil
}

func (r *Runtime) readSession(s *botRuntime, epoch uint64, session core.AgentSession) {
	for event := range session.Events() {
		s.mu.Lock()
		if s.epoch != epoch {
			s.mu.Unlock()
			return
		}
		t := s.current
		if len(s.legacyTurns) > 0 {
			t = s.legacyTurns[0]
		}
		backend := s.backend
		productTurn := ""
		providerScoped := false
		if providerTurn, ok := event.Metadata["turnId"].(string); ok && providerTurn != "" {
			providerScoped = true
			productTurn = s.providerTurns[providerTurn]
			if productTurn == "" {
				t = nil
			}
		}
		if s.compacting {
			t = nil
		}
		s.mu.Unlock()
		if productTurn != "" {
			r.mu.Lock()
			t = r.turns[productTurn]
			r.mu.Unlock()
		}
		if t == nil && !providerScoped && event.Type == core.EventText && !event.FromSubagent {
			t = r.beginAutonomous(s, epoch)
		}
		turnID := ""
		if t != nil {
			turnID = t.id
		}
		if _, err := r.store.AppendEvent(s.id, turnID, "agent", normalizedEvent(event)); err != nil {
			r.logJournalError(s.id, turnID, err)
			if t != nil {
				t.mu.Lock()
				t.err = err.Error()
				t.mu.Unlock()
				t.cancel()
			}
		}
		if event.SessionID != "" {
			r.logJournalError(s.id, turnID, r.materializeThread(s.id, backend, event.SessionID))
		}
		if t == nil {
			continue
		}
		t.mu.Lock()
		if !event.FromSubagent {
			switch event.Type {
			case core.EventText:
				t.text.WriteString(event.Content)
			case core.EventError:
				if event.Error != nil {
					t.err = event.Error.Error()
				} else if event.Content != "" {
					t.err = event.Content
				}
			case core.EventResult:
				if event.Content != "" {
					t.text.Reset()
					t.text.WriteString(event.Content)
				}
			}
			if event.OutputTokens > t.metrics.outputTokens {
				t.metrics.outputTokens = event.OutputTokens
			}
			t.metrics.legacy(event, r.cfg.Now())
		}
		t.mu.Unlock()
		if event.Type == core.EventPermissionRequest {
			s.mu.Lock()
			if s.current == t {
				s.pendingPermissions[event.RequestID] = true
			}
			s.mu.Unlock()
		}
		if event.Type == core.EventResult && !event.FromSubagent {
			s.mu.Lock()
			for i, pending := range s.legacyTurns {
				if pending == t {
					s.legacyTurns = append(s.legacyTurns[:i], s.legacyTurns[i+1:]...)
					break
				}
			}
			s.mu.Unlock()
			t.settleOnce.Do(func() { close(t.settled) })
		}
	}
	s.mu.Lock()
	t := s.current
	valid := s.epoch == epoch
	s.mu.Unlock()
	if t != nil && valid {
		t.mu.Lock()
		t.err = "Agent connection closed before this turn completed."
		t.mu.Unlock()
		t.settleOnce.Do(func() { close(t.settled) })
	}
}

func (r *Runtime) finishTurn(s *botRuntime, t *runtimeTurn, status string, turnErr error) {
	t.finishOnce.Do(func() {
		r.flushNative()
		t.mu.Lock()
		t.metrics.pause(r.cfg.Now())
		result := TurnResult{Text: t.text.String(), Status: status, OutputTokens: t.metrics.outputTokens, GenerationMS: t.metrics.elapsed.Milliseconds()}
		if turnErr != nil {
			result.Error = turnErr.Error()
		}
		if result.OutputTokens > 0 && result.GenerationMS > 0 {
			result.TokensPerSec = float64(result.OutputTokens) * 1000 / float64(result.GenerationMS)
		}
		t.result = result
		t.mu.Unlock()
		if result.Text != "" {
			_, err := r.store.AppendEvent(t.botID, t.id, "message", map[string]any{"role": "assistant", "content": result.Text, "source": t.bot.Backend})
			r.logJournalError(t.botID, t.id, err)
		}
		_, err := r.store.AppendEvent(t.botID, t.id, "turn", turnPayload(t.bot, status, result))
		r.logJournalError(t.botID, t.id, err)
		s.mu.Lock()
		if s.current == t {
			s.current, s.lastTurn, s.lastBackend = nil, t.id, t.bot.Backend
			s.pendingPermissions = make(map[string]bool)
		}
		stillRunning := s.current != nil
		_, err = r.store.UpdateBot(t.botID, func(bot *Bot) error {
			if bot.Status != "archived" {
				bot.Status = "idle"
				if stillRunning {
					bot.Status = "running"
				}
				if !stillRunning && (status == "error" || status == "interrupted") {
					bot.Status = status
				}
			}
			return nil
		})
		r.logJournalError(t.botID, t.id, err)
		for i, pending := range s.legacyTurns {
			if pending == t {
				s.legacyTurns = append(s.legacyTurns[:i], s.legacyTurns[i+1:]...)
				break
			}
		}
		s.mu.Unlock()
		t.cancel()
		close(t.done)
		if status == "completed" || status == "stopped" {
			// Stop already paused its queue atomically. A later owner send or
			// explicit resume can reopen it while cancellation is settling.
			r.dispatchQueue(s)
		} else {
			r.logJournalError(t.botID, "", r.setQueuePaused(s, true, "turn_"+status, result.Error))
		}
		r.pruneCompletedTurns()
	})
}

func (r *Runtime) pruneCompletedTurns() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.turns) <= 256 {
		return
	}
	for id, t := range r.turns {
		select {
		case <-t.done:
			delete(r.turns, id)
		default:
		}
		if len(r.turns) <= 128 {
			break
		}
	}
}

func normalizedEvent(event core.Event) map[string]any {
	data := map[string]any{"type": event.Type, "content": event.Content, "toolName": event.ToolName, "toolInput": event.ToolInput,
		"toolInputRaw": event.ToolInputRaw, "toolResult": event.ToolResult, "toolStatus": event.ToolStatus,
		"sessionId": event.SessionID, "requestId": plainRequestID(event.RequestID), "questions": event.Questions, "done": event.Done,
		"inputTokens": event.InputTokens, "outputTokens": event.OutputTokens, "cacheCreationInputTokens": event.CacheCreationInputTokens,
		"cacheReadInputTokens": event.CacheReadInputTokens, "metadata": event.Metadata, "synthetic": event.Synthetic, "fromSubagent": event.FromSubagent}
	if event.ToolExitCode != nil {
		data["toolExitCode"] = *event.ToolExitCode
	}
	if event.ToolSuccess != nil {
		data["toolSuccess"] = *event.ToolSuccess
	}
	if event.Error != nil {
		data["error"] = event.Error.Error()
	}
	return data
}

func plainRequestID(id string) string {
	var decoded string
	if json.Unmarshal([]byte(id), &decoded) == nil {
		return decoded
	}
	return id
}
