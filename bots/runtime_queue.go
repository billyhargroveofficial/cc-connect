package bots

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

const maxQueuedMessages = 64

type runtimeQueuedMessage struct {
	message QueuedMessage
	turn    *runtimeTurn
}

type uncertainSteerError struct{ cause error }

func (e *uncertainSteerError) Error() string {
	return "Steering delivery could not be confirmed. The message will not be sent again automatically. Check the current reply before resending it. " + e.cause.Error()
}

func (e *uncertainSteerError) Unwrap() []error { return []error{ErrConflict, e.cause} }

func publicMessageAttachments(attachments []Attachment) []Attachment {
	public := append([]Attachment{}, attachments...)
	for i := range public {
		public[i].Path = ""
	}
	return public
}

// The caller holds r.mu while creating a bot's first state. Only queued records
// recover: a started message may have reached its provider before a crash and
// is never silently submitted twice. Recovered work requires owner resumption.
func (r *Runtime) restoreMessageQueue(s *botRuntime, events []Event) {
	pending := map[string]QueuedMessage{}
	steers := map[string]string{}
	order := []string{}
	for _, event := range events {
		if event.Type == "queue_steer" {
			var operation struct{ ID, Status string }
			if json.Unmarshal(event.Data, &operation) == nil {
				steers[operation.ID] = operation.Status
			}
			continue
		}
		if event.Type != "queue" {
			continue
		}
		var message QueuedMessage
		if json.Unmarshal(event.Data, &message) != nil || message.ID == "" {
			continue
		}
		if message.Status != "queued" {
			delete(pending, message.ID)
			continue
		}
		if _, exists := pending[message.ID]; !exists {
			order = append(order, message.ID)
		}
		message.Attachments = publicMessageAttachments(message.Attachments)
		pending[message.ID] = message
	}
	for _, id := range order {
		message, exists := pending[id]
		if !exists || steers[id] == "pending" {
			continue
		}
		delete(pending, id)
		t := r.newQueuedTurn(s.id, id)
		s.queue = append(s.queue, &runtimeQueuedMessage{message: message, turn: t})
		r.turns[id] = t
	}
	s.queuePaused = len(s.queue) > 0
}

func (r *Runtime) newQueuedTurn(botID, turnID string) *runtimeTurn {
	ctx, cancel := context.WithCancel(r.ctx)
	return &runtimeTurn{id: turnID, botID: botID, ctx: ctx, cancel: cancel, done: make(chan struct{}), settled: make(chan struct{})}
}

// SubmitMessage queues owner messages during an active turn. Internal bot
// delegation uses sendMessage instead and keeps its cycle/busy protection.
func (r *Runtime) SubmitMessage(ctx context.Context, botID string, request MessageRequest) (MessageReceipt, error) {
	if request.Mode != "" && request.Mode != "queue" && request.Mode != "steer" {
		return MessageReceipt{}, fmt.Errorf("%w: message mode must be queue or steer", ErrInvalid)
	}
	request, err := r.prepareMessage(ctx, botID, request)
	if err != nil {
		return MessageReceipt{}, err
	}
	s, err := r.state(botID)
	if err != nil {
		return MessageReceipt{}, err
	}
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	bot, err := r.store.GetBot(botID)
	if err != nil {
		return MessageReceipt{}, err
	}
	if bot.Status == "archived" {
		return MessageReceipt{}, fmt.Errorf("%w: bot is archived", ErrConflict)
	}
	if request.Mode == "steer" {
		return r.steerMessageLocked(ctx, s, request, "")
	}
	s.mu.Lock()
	active, compacting, queued := s.current != nil, s.compacting, len(s.queue)
	s.mu.Unlock()
	if compacting {
		return MessageReceipt{}, ErrBusy
	}
	if !active && queued == 0 {
		id, err := r.startMessageLocked(s, r.ctx, request, nil)
		return MessageReceipt{TurnID: id, Status: "running"}, err
	}
	if queued >= maxQueuedMessages {
		return MessageReceipt{}, fmt.Errorf("%w: message queue is full", ErrConflict)
	}
	entry, err := r.enqueueMessageLocked(s, request)
	if err != nil {
		return MessageReceipt{}, err
	}
	// Completion can race the earlier active snapshot. Always recheck under
	// the lifecycle gate after enqueueing so no accepted message gets stranded.
	r.dispatchQueueLocked(s)
	return MessageReceipt{TurnID: entry.message.ID, QueueID: entry.message.ID, Status: "queued"}, nil
}

func (r *Runtime) enqueueMessageLocked(s *botRuntime, request MessageRequest) (*runtimeQueuedMessage, error) {
	id, err := randomID("turn_")
	if err != nil {
		return nil, err
	}
	entry := &runtimeQueuedMessage{message: QueuedMessage{ID: id, Text: request.Text, Attachments: publicMessageAttachments(request.Attachments), Source: request.Source, CreatedAt: r.cfg.Now().UTC(), Status: "queued"}, turn: r.newQueuedTurn(s.id, id)}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		entry.turn.cancel()
		return nil, os.ErrClosed
	}
	r.turns[id] = entry.turn
	r.mu.Unlock()
	s.mu.Lock()
	count := len(s.queue) + 1
	s.mu.Unlock()
	if err := r.appendQueueEvent(s.id, entry.message, count, false, ""); err != nil {
		r.mu.Lock()
		delete(r.turns, id)
		r.mu.Unlock()
		entry.turn.cancel()
		return nil, err
	}
	s.mu.Lock()
	s.queue = append(s.queue, entry)
	s.queuePaused = false
	s.mu.Unlock()
	return entry, nil
}

func (r *Runtime) appendQueueEvent(botID string, message QueuedMessage, count int, paused bool, failure string) error {
	data := struct {
		QueuedMessage
		Count  int    `json:"count"`
		Paused bool   `json:"paused"`
		Error  string `json:"error,omitempty"`
	}{message, count, paused, failure}
	_, err := r.store.AppendEvent(botID, "", "queue", data)
	return err
}

func (r *Runtime) Queue(botID string) (MessageQueue, error) {
	s, err := r.state(botID)
	if err != nil {
		return MessageQueue{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	queue := MessageQueue{Messages: []QueuedMessage{}, Paused: s.queuePaused}
	for _, entry := range s.queue {
		message := entry.message
		message.Attachments = publicMessageAttachments(message.Attachments)
		queue.Messages = append(queue.Messages, message)
	}
	return queue, nil
}

func (r *Runtime) CancelQueuedMessage(ctx context.Context, botID, messageID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s, err := r.state(botID)
	if err != nil {
		return err
	}
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	return r.removeQueuedMessageLocked(s, messageID, "cancelled", "")
}

func (r *Runtime) removeQueuedMessageLocked(s *botRuntime, messageID, status, failure string) error {
	s.mu.Lock()
	index := -1
	var entry *runtimeQueuedMessage
	for i, candidate := range s.queue {
		if candidate.message.ID == messageID {
			index, entry = i, candidate
			break
		}
	}
	count, paused := len(s.queue)-1, s.queuePaused
	s.mu.Unlock()
	if index < 0 {
		return ErrNotFound
	}
	message := entry.message
	message.Status = status
	if err := r.appendQueueEvent(s.id, message, count, paused, failure); err != nil {
		return err
	}
	s.mu.Lock()
	s.queue = append(s.queue[:index], s.queue[index+1:]...)
	s.mu.Unlock()
	r.finishQueuedTurn(entry.turn, status, failure)
	return nil
}

func (r *Runtime) finishQueuedTurn(t *runtimeTurn, status, failure string) {
	t.finishOnce.Do(func() {
		t.mu.Lock()
		t.result = TurnResult{Status: status, Error: failure}
		t.mu.Unlock()
		t.cancel()
		close(t.done)
	})
	r.pruneCompletedTurns()
}

func (r *Runtime) ResumeQueue(ctx context.Context, botID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s, err := r.state(botID)
	if err != nil {
		return err
	}
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	if err := r.setQueuePaused(s, false, "owner_resume", ""); err != nil {
		return err
	}
	r.dispatchQueueLocked(s)
	return nil
}

func (r *Runtime) setQueuePaused(s *botRuntime, paused bool, reason, failure string) error {
	s.mu.Lock()
	if len(s.queue) == 0 {
		s.mu.Unlock()
		return nil
	}
	s.queuePaused = paused
	s.mu.Unlock()
	_, err := r.store.AppendEvent(s.id, "", "queue_control", map[string]any{"paused": paused, "reason": reason, "error": failure})
	return err
}

// finishTurn invokes this after journaling its final answer. A tracked worker
// takes the lifecycle gate, so shutdown and simultaneous HTTP sends cannot
// race a second dispatcher or write after the store has been closed.
func (r *Runtime) dispatchQueue(s *botRuntime) {
	s.mu.Lock()
	ready := s.current == nil && !s.compacting && !s.queuePaused && len(s.queue) > 0
	s.mu.Unlock()
	if !ready {
		return
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.wg.Add(1)
	r.mu.Unlock()
	go func() {
		defer r.wg.Done()
		s.lifecycle.Lock()
		defer s.lifecycle.Unlock()
		r.dispatchQueueLocked(s)
	}()
}

func (r *Runtime) dispatchQueueLocked(s *botRuntime) {
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return
	}
	s.mu.Lock()
	if s.current != nil || s.compacting || s.queuePaused || len(s.queue) == 0 {
		s.mu.Unlock()
		return
	}
	entry := s.queue[0]
	count := len(s.queue) - 1
	s.mu.Unlock()
	request, err := r.prepareMessage(r.ctx, s.id, MessageRequest{Text: entry.message.Text, Attachments: entry.message.Attachments, Source: entry.message.Source})
	if err != nil {
		r.logJournalError(s.id, "", r.setQueuePaused(s, true, "dispatch_failed", err.Error()))
		r.logJournalError(s.id, "", r.removeQueuedMessageLocked(s, entry.message.ID, "failed", err.Error()))
		return
	}
	message := entry.message
	message.Status = "started"
	if err := r.appendQueueEvent(s.id, message, count, false, ""); err != nil {
		r.logJournalError(s.id, "", err)
		r.logJournalError(s.id, "", r.setQueuePaused(s, true, "journal_failed", err.Error()))
		return
	}
	s.mu.Lock()
	s.queue = s.queue[1:]
	s.mu.Unlock()
	if _, err := r.startMessageLocked(s, r.ctx, request, entry.turn); err != nil {
		if errors.Is(err, os.ErrClosed) || errors.Is(err, ErrBusy) {
			r.restoreUndispatchedMessageLocked(s, entry)
			return
		}
		message.Status = "failed"
		r.logJournalError(s.id, "", r.appendQueueEvent(s.id, message, count, true, err.Error()))
		r.logJournalError(s.id, "", r.setQueuePaused(s, true, "dispatch_failed", err.Error()))
		r.finishQueuedTurn(entry.turn, "error", err.Error())
	}
}

// Admission can lose its race with Close or an unsolicited native turn after
// the started marker was committed. No provider Send happened in these cases;
// restore the same pending ID rather than losing it or replaying an active turn.
func (r *Runtime) restoreUndispatchedMessageLocked(s *botRuntime, entry *runtimeQueuedMessage) {
	s.mu.Lock()
	for i, turn := range s.legacyTurns {
		if turn == entry.turn {
			s.legacyTurns = append(s.legacyTurns[:i], s.legacyTurns[i+1:]...)
			break
		}
	}
	s.queue = append([]*runtimeQueuedMessage{entry}, s.queue...)
	s.queuePaused = true
	count := len(s.queue)
	s.mu.Unlock()
	// startMessageLocked cancelled this unadmitted turn. Keep its wait channel
	// and identity, but create a fresh work context before a later owner resume.
	r.mu.Lock()
	entry.turn.ctx, entry.turn.cancel = context.WithCancel(r.ctx)
	r.mu.Unlock()
	r.logJournalError(s.id, "", r.appendQueueEvent(s.id, entry.message, count, true, ""))
}

func (r *Runtime) SteerQueuedMessage(ctx context.Context, botID, messageID string) (MessageReceipt, error) {
	s, err := r.state(botID)
	if err != nil {
		return MessageReceipt{}, err
	}
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	s.mu.Lock()
	var message *QueuedMessage
	for _, entry := range s.queue {
		if entry.message.ID == messageID {
			copyMessage := entry.message
			message = &copyMessage
			break
		}
	}
	s.mu.Unlock()
	if message == nil {
		return MessageReceipt{}, ErrNotFound
	}
	request, err := r.prepareMessage(ctx, botID, MessageRequest{Text: message.Text, Attachments: message.Attachments, Source: message.Source})
	if err != nil {
		return MessageReceipt{}, err
	}
	// Persist intent before the provider boundary. A crash or journal failure
	// after ACK must never leave a message eligible for duplicate resubmission.
	if _, err := r.store.AppendEvent(botID, "", "queue_steer", map[string]any{"id": messageID, "status": "pending"}); err != nil {
		return MessageReceipt{}, err
	}
	receipt, err := r.steerMessageLocked(ctx, s, request, messageID)
	if err != nil {
		var uncertain *uncertainSteerError
		if errors.As(err, &uncertain) {
			r.logJournalError(botID, "", r.setQueuePaused(s, true, "steer_uncertain", err.Error()))
			r.consumeQueuedMessageLocked(s, messageID, "uncertain", err.Error())
			return MessageReceipt{}, err
		}
		_, journalErr := r.store.AppendEvent(botID, "", "queue_steer", map[string]any{"id": messageID, "status": "failed"})
		r.logJournalError(botID, "", journalErr)
		return MessageReceipt{}, err
	}
	r.consumeQueuedMessageLocked(s, messageID, "steered", "")
	receipt.QueueID = messageID
	return receipt, nil
}

func (r *Runtime) consumeQueuedMessageLocked(s *botRuntime, messageID, status, failure string) {
	s.mu.Lock()
	var entry *runtimeQueuedMessage
	for i, candidate := range s.queue {
		if candidate.message.ID == messageID {
			entry = candidate
			s.queue = append(s.queue[:i], s.queue[i+1:]...)
			break
		}
	}
	count, paused := len(s.queue), s.queuePaused
	s.mu.Unlock()
	if entry == nil {
		return
	}
	message := entry.message
	message.Status = status
	r.logJournalError(s.id, "", r.appendQueueEvent(s.id, message, count, paused, failure))
	r.finishQueuedTurn(entry.turn, status, failure)
}

func (r *Runtime) steerMessageLocked(ctx context.Context, s *botRuntime, request MessageRequest, queueID string) (MessageReceipt, error) {
	s.mu.Lock()
	t, session, backend, compacting := s.current, s.session, s.backend, s.compacting
	s.mu.Unlock()
	if t == nil || compacting || session == nil || !session.Alive() {
		return MessageReceipt{}, fmt.Errorf("%w: there is no active turn to steer", ErrConflict)
	}
	select {
	case <-t.settled:
		return MessageReceipt{}, fmt.Errorf("%w: the active turn has already finished", ErrConflict)
	default:
	}
	rpc, ok := session.(core.AgentRPCSession)
	if !ok {
		return MessageReceipt{}, fmt.Errorf("%w: this harness does not support steering; keep the message queued", ErrConflict)
	}
	t.mu.Lock()
	providerTurn, completed := t.providerTurn, t.nativeCompleted
	t.mu.Unlock()
	if completed || t.ctx.Err() != nil {
		return MessageReceipt{}, fmt.Errorf("%w: the active turn has already finished", ErrConflict)
	}
	method, params, err := steerInput(backend, session.CurrentSessionID(), providerTurn, request)
	if err != nil {
		return MessageReceipt{}, err
	}
	steerCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := steerCtx.Err(); err != nil {
		return MessageReceipt{}, err
	}
	if err := rpc.RPC(steerCtx, method, params, nil); err != nil {
		var rejection *core.RPCRejectionError
		if errors.As(err, &rejection) {
			return MessageReceipt{}, fmt.Errorf("%w: steering was rejected: %v", ErrConflict, err)
		}
		return MessageReceipt{}, &uncertainSteerError{cause: err}
	}
	if backend == "pi" && !activeSteerOwner(s, t) {
		// Pi's command accepts steering even while idle. Clear only this
		// dedicated session's pending inputs before another product turn can
		// start; lifecycle serializes both owner sends and steering requests.
		clearCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		clearErr := rpc.RPC(clearCtx, "clear_queue", nil, nil)
		stop()
		return MessageReceipt{}, &uncertainSteerError{cause: errors.Join(errors.New("the active turn finished while steering was in flight"), clearErr)}
	}
	_, err = r.store.AppendEvent(s.id, t.id, "message", map[string]any{"role": "user", "content": request.Text, "attachments": publicMessageAttachments(request.Attachments), "source": request.Source, "mode": "steer", "queueId": queueID})
	// The provider has accepted the input. Returning a retryable failure here
	// would let the UI inject it again when persistence alone failed.
	r.logJournalError(s.id, t.id, err)
	return MessageReceipt{TurnID: t.id, Status: "steered"}, nil
}

func activeSteerOwner(s *botRuntime, t *runtimeTurn) bool {
	s.mu.Lock()
	active := s.current == t
	s.mu.Unlock()
	select {
	case <-t.settled:
		return false
	default:
		return active && t.ctx.Err() == nil
	}
}

func steerInput(backend, threadID, turnID string, request MessageRequest) (string, map[string]any, error) {
	files := []string{}
	for _, attachment := range request.Attachments {
		if !strings.HasPrefix(attachment.MimeType, "image/") {
			files = append(files, attachment.Path)
		}
	}
	prompt := core.AppendFileRefs(request.Text, files)
	switch backend {
	case "codex":
		if threadID == "" || turnID == "" {
			return "", nil, fmt.Errorf("%w: the native turn is still starting; keep the message queued", ErrConflict)
		}
		input := []map[string]any{{"type": "text", "text": prompt, "text_elements": []any{}}}
		for _, attachment := range request.Attachments {
			if strings.HasPrefix(attachment.MimeType, "image/") {
				input = append(input, map[string]any{"type": "localImage", "path": attachment.Path})
			}
		}
		return "turn/steer", map[string]any{"threadId": threadID, "expectedTurnId": turnID, "input": input}, nil
	case "pi":
		images, _, err := runtimeAttachments(request.Attachments)
		if err != nil {
			return "", nil, err
		}
		params := map[string]any{"message": prompt}
		if len(images) > 0 {
			content := make([]map[string]any, 0, len(images))
			for _, image := range images {
				content = append(content, map[string]any{"type": "image", "mimeType": image.MimeType, "data": base64.StdEncoding.EncodeToString(image.Data)})
			}
			params["images"] = content
		}
		return "steer", params, nil
	default:
		return "", nil, fmt.Errorf("%w: this harness does not support steering; keep the message queued", ErrConflict)
	}
}
