package bots

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	ErrNotFound    = errors.New("bot not found")
	ErrConflict    = errors.New("bot state conflicts with this operation")
	ErrInvalid     = errors.New("invalid request")
	botIDPattern   = regexp.MustCompile(`^bot_[a-f0-9]{32}$`)
	envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

type diskState struct {
	Version int   `json:"version"`
	Bots    []Bot `json:"bots"`
}

type storeSubscription struct {
	events chan Event
	after  uint64
}

// Store owns its journal and subscriber channels. Every mutation is serialized;
// live subscribers are registered under the same lock as their initial replay.
type Store struct {
	mu         sync.Mutex
	root       string
	bots       map[string]Bot
	events     []Event
	seq        uint64
	journal    *os.File
	journalErr error
	lock       *storeFileLock
	subs       map[uint64]*storeSubscription
	nextSub    uint64
	closed     bool
}

func OpenStore(root string) (*Store, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("store root: %w", err)
	}
	if err := privateDir(abs); err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("resolve store root: %w", err)
	}
	s := &Store{root: resolved, bots: make(map[string]Bot), subs: make(map[uint64]*storeSubscription)}
	s.lock, err = acquireStoreLock(filepath.Join(s.root, "owner.lock"))
	if err != nil {
		return nil, err
	}
	opened := false
	defer func() {
		if !opened {
			s.lock.Close()
		}
	}()
	for _, dir := range []string{filepath.Join(s.root, "bots"), s.UserDir(), filepath.Join(s.UserDir(), "skills")} {
		if err := privateDir(dir); err != nil {
			return nil, err
		}
	}
	if err := writeInitialFile(filepath.Join(s.UserDir(), "AGENTS.md"), []byte("# Connect Bots\n\nShared owner instructions for all bots.\n")); err != nil {
		return nil, err
	}
	stateBytes, err := os.ReadFile(filepath.Join(s.root, "state.json"))
	fresh := errors.Is(err, os.ErrNotExist)
	if err != nil && !fresh {
		return nil, fmt.Errorf("read bot state: %w", err)
	}
	if !fresh {
		var state diskState
		if err := json.Unmarshal(stateBytes, &state); err != nil {
			return nil, fmt.Errorf("decode bot state: %w", err)
		}
		if state.Version != 1 {
			return nil, fmt.Errorf("unsupported bot state version %d", state.Version)
		}
		for _, bot := range state.Bots {
			if !botIDPattern.MatchString(bot.ID) {
				return nil, fmt.Errorf("invalid bot id in state")
			}
			if _, exists := s.bots[bot.ID]; exists {
				return nil, fmt.Errorf("duplicate bot id in state")
			}
			bot.WorkDir = filepath.Join(s.root, "bots", bot.ID)
			if bot.Threads == nil {
				bot.Threads = make(map[string]string)
			}
			s.bots[bot.ID] = cloneBot(bot)
		}
	}
	journal, err := os.OpenFile(filepath.Join(s.root, "events.jsonl"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open event journal: %w", err)
	}
	s.journal = journal
	if err := journal.Chmod(0600); err != nil {
		journal.Close()
		return nil, err
	}
	if err := s.loadJournal(); err != nil {
		journal.Close()
		return nil, err
	}
	if fresh {
		if _, err := s.CreateBot(Bot{Name: "Руководитель", Role: "Координируй остальных ботов. Делегируй конкретные задачи и сообщай владельцу проверенные результаты.", Chief: true, Avatar: "coordinator"}); err != nil {
			journal.Close()
			return nil, err
		}
	}
	if err := s.recoverInterrupted(); err != nil {
		journal.Close()
		return nil, err
	}
	opened = true
	return s, nil
}

func (s *Store) Root() string    { return s.root }
func (s *Store) UserDir() string { return filepath.Join(s.root, "user") }

func (s *Store) Cursor() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seq
}

func (s *Store) BotDir(id string) (string, error) {
	if _, err := s.GetBot(id); err != nil {
		return "", err
	}
	return filepath.Join(s.root, "bots", id), nil
}

func (s *Store) ListBots() []Bot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listBotsLocked()
}

func (s *Store) listBotsLocked() []Bot {
	bots := make([]Bot, 0, len(s.bots))
	for _, bot := range s.bots {
		bots = append(bots, cloneBot(bot))
	}
	sort.Slice(bots, func(i, j int) bool {
		if bots[i].Chief != bots[j].Chief {
			return bots[i].Chief
		}
		if !bots[i].CreatedAt.Equal(bots[j].CreatedAt) {
			return bots[i].CreatedAt.Before(bots[j].CreatedAt)
		}
		return bots[i].ID < bots[j].ID
	})
	return bots
}

func (s *Store) GetBot(id string) (Bot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	bot, ok := s.bots[id]
	if !ok {
		return Bot{}, ErrNotFound
	}
	return cloneBot(bot), nil
}

func (s *Store) CreateBot(input Bot) (Bot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Bot{}, os.ErrClosed
	}
	if s.journalErr != nil {
		return Bot{}, s.journalErr
	}
	id, err := randomID("bot_")
	if err != nil {
		return Bot{}, err
	}
	now := time.Now().UTC()
	input.ID, input.Status = id, "idle"
	input.CreatedAt, input.UpdatedAt = now, now
	input.WorkDir = filepath.Join(s.root, "bots", id)
	input.Threads = make(map[string]string)
	applyBotDefaults(&input)
	if input.Telegram != nil {
		if input.Telegram.Status != "" || input.Telegram.Error != "" {
			return Bot{}, fmt.Errorf("%w: Telegram connection status is managed by the server", ErrInvalid)
		}
		if input.Telegram.Enabled {
			input.Telegram.Status = "connecting"
		} else {
			input.Telegram.Status = "disabled"
		}
	}
	if err := s.validateBotLocked(input); err != nil {
		return Bot{}, err
	}
	for _, dir := range []string{input.WorkDir, filepath.Join(input.WorkDir, ".agents", "skills"), filepath.Join(input.WorkDir, "tmp"), filepath.Join(input.WorkDir, "uploads")} {
		if err := privateDir(dir); err != nil {
			return Bot{}, err
		}
	}
	content := "# " + input.Name + "\n\n" + input.Role + "\n\nUse tmp/ for disposable working files. Durable results belong outside tmp/.\n"
	if err := writeInitialFile(filepath.Join(input.WorkDir, "AGENTS.md"), []byte(content)); err != nil {
		return Bot{}, err
	}
	s.bots[id] = cloneBot(input)
	if err := s.saveStateLocked(); err != nil {
		delete(s.bots, id)
		return Bot{}, err
	}
	if _, err := s.appendEventLocked(id, "", "bot", input); err != nil {
		return cloneBot(input), err
	}
	return cloneBot(input), nil
}

// SaveBot persists runtime fields as well as profile fields. HTTP callers use
// PatchBot, which forbids client-controlled paths, status, and backend thread IDs.
func (s *Store) SaveBot(bot Bot) (Bot, error) {
	return s.UpdateBot(bot.ID, func(current *Bot) error {
		*current = cloneBot(bot)
		return nil
	})
}

func (s *Store) UpdateBot(id string, mutate func(*Bot) error) (Bot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Bot{}, os.ErrClosed
	}
	if s.journalErr != nil {
		return Bot{}, s.journalErr
	}
	before, ok := s.bots[id]
	if !ok {
		return Bot{}, ErrNotFound
	}
	bot := cloneBot(before)
	if err := mutate(&bot); err != nil {
		return Bot{}, err
	}
	bot.ID, bot.WorkDir, bot.CreatedAt = before.ID, before.WorkDir, before.CreatedAt
	bot.UpdatedAt = time.Now().UTC()
	if err := s.validateBotLocked(bot); err != nil {
		return Bot{}, err
	}
	s.bots[id] = cloneBot(bot)
	if err := s.saveStateLocked(); err != nil {
		s.bots[id] = before
		return Bot{}, err
	}
	if _, err := s.appendEventLocked(id, "", "bot", bot); err != nil {
		return cloneBot(bot), err
	}
	return cloneBot(bot), nil
}

func (s *Store) PatchBot(id string, fields map[string]json.RawMessage) (Bot, error) {
	return s.UpdateBot(id, func(bot *Bot) error {
		if bot.Status == "archived" {
			return fmt.Errorf("%w: archived bot is read-only", ErrConflict)
		}
		if isActiveStatus(bot.Status) {
			return fmt.Errorf("%w: stop the bot before changing its settings", ErrConflict)
		}
		previousBackend := bot.Backend
		previousTelegram := bot.Telegram
		for key, value := range fields {
			var target any
			switch key {
			case "name":
				target = &bot.Name
			case "role":
				target = &bot.Role
			case "avatar":
				target = &bot.Avatar
			case "chief":
				target = &bot.Chief
			case "backend":
				target = &bot.Backend
			case "model":
				target = &bot.Model
			case "effort":
				target = &bot.Effort
			case "disabledSkills":
				target = &bot.DisabledSkills
			case "telegram":
				var binding *TelegramBinding
				if err := json.Unmarshal(value, &binding); err != nil {
					return fmt.Errorf("%w: invalid telegram", ErrInvalid)
				}
				bot.Telegram = binding
				continue
			default:
				return fmt.Errorf("%w: field %q is not editable", ErrInvalid, key)
			}
			if err := json.Unmarshal(value, target); err != nil {
				return fmt.Errorf("%w: invalid %s", ErrInvalid, key)
			}
		}
		if _, changed := fields["telegram"]; changed && bot.Telegram != nil {
			if bot.Telegram.Status != "" || bot.Telegram.Error != "" {
				return fmt.Errorf("%w: Telegram connection status is managed by the server", ErrInvalid)
			}
			if sameTelegramConfig(previousTelegram, bot.Telegram) {
				bot.Telegram.Status, bot.Telegram.Error = previousTelegram.Status, previousTelegram.Error
			} else if bot.Telegram.Enabled {
				bot.Telegram.Status = "connecting"
			} else {
				bot.Telegram.Status = "disabled"
			}
		}
		if bot.Backend != previousBackend {
			if _, explicit := fields["model"]; !explicit {
				bot.Model = ""
			}
			if _, explicit := fields["effort"]; !explicit {
				bot.Effort = ""
			}
			applyBotDefaults(bot)
		}
		return nil
	})
}

func (s *Store) ArchiveBot(id string) (Bot, error) {
	return s.UpdateBot(id, func(bot *Bot) error {
		if isActiveStatus(bot.Status) {
			return fmt.Errorf("%w: stop the bot before archiving", ErrConflict)
		}
		bot.Status = "archived"
		return nil
	})
}

func (s *Store) AppendEvent(botID, turnID, kind string, data any) (Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Event{}, os.ErrClosed
	}
	return s.appendEventLocked(botID, turnID, kind, data)
}

func (s *Store) appendEventLocked(botID, turnID, kind string, data any) (Event, error) {
	if s.journalErr != nil {
		return Event{}, s.journalErr
	}
	if botID != "" {
		if _, ok := s.bots[botID]; !ok {
			return Event{}, ErrNotFound
		}
	}
	if kind == "" {
		return Event{}, fmt.Errorf("%w: event type is empty", ErrInvalid)
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return Event{}, fmt.Errorf("encode event data: %w", err)
	}
	event := Event{Seq: s.seq + 1, BotID: botID, TurnID: turnID, Type: kind, Time: time.Now().UTC(), Data: encoded}
	line, err := json.Marshal(event)
	if err != nil {
		return Event{}, fmt.Errorf("encode event: %w", err)
	}
	line = append(line, '\n')
	if _, err := s.journal.Write(line); err != nil {
		s.journalErr = fmt.Errorf("append event journal: %w", err)
		return Event{}, s.journalErr
	}
	if err := s.journal.Sync(); err != nil {
		// A failed sync leaves persistence uncertain. Stop further mutations so
		// the next append cannot reuse a sequence possibly already on disk.
		s.journalErr = fmt.Errorf("sync event journal: %w", err)
		return Event{}, s.journalErr
	}
	s.seq = event.Seq
	s.events = append(s.events, event)
	for id, subscription := range s.subs {
		if event.Seq <= subscription.after {
			continue
		}
		select {
		case subscription.events <- cloneEvent(event):
		default:
			// A slow browser reconnects with its last delivered sequence cursor.
			close(subscription.events)
			delete(s.subs, id)
		}
	}
	return cloneEvent(event), nil
}

func (s *Store) Events(botID string, after uint64) ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if botID != "" {
		if _, ok := s.bots[botID]; !ok {
			return nil, ErrNotFound
		}
	}
	return s.eventsLocked(botID, after), nil
}

func (s *Store) eventsLocked(botID string, after uint64) []Event {
	index := sort.Search(len(s.events), func(i int) bool { return s.events[i].Seq > after })
	result := make([]Event, 0, len(s.events)-index)
	for _, event := range s.events[index:] {
		if botID == "" || event.BotID == botID {
			result = append(result, cloneEvent(event))
		}
	}
	return result
}

func (s *Store) Subscribe(after uint64) (<-chan Event, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, nil, os.ErrClosed
	}
	replay := s.eventsLocked("", after)
	channel := make(chan Event, len(replay)+128)
	for _, event := range replay {
		channel <- event
	}
	s.nextSub++
	id := s.nextSub
	s.subs[id] = &storeSubscription{events: channel, after: after}
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if current, exists := s.subs[id]; exists {
				close(current.events)
				delete(s.subs, id)
			}
		})
	}
	return channel, cancel, nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	for id, subscription := range s.subs {
		close(subscription.events)
		delete(s.subs, id)
	}
	return errors.Join(s.journal.Close(), s.lock.Close())
}

func (s *Store) loadJournal() error {
	reader := bufio.NewReader(s.journal)
	var validBytes int64
	for {
		line, err := reader.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			if len(line) > 0 {
				// A crash may leave the final append incomplete. Keep prior records.
				if err := s.journal.Truncate(validBytes); err != nil {
					return fmt.Errorf("repair incomplete journal append: %w", err)
				}
			}
			break
		}
		if err != nil {
			return fmt.Errorf("read event journal: %w", err)
		}
		if len(bytes.TrimSpace(line)) == 0 {
			return fmt.Errorf("empty journal record at byte %d", validBytes)
		}
		var event Event
		if err := json.Unmarshal(line, &event); err != nil {
			return fmt.Errorf("decode journal record at byte %d: %w", validBytes, err)
		}
		if event.Seq <= s.seq || event.Type == "" || !json.Valid(event.Data) {
			return fmt.Errorf("invalid journal record at byte %d", validBytes)
		}
		s.seq = event.Seq
		s.events = append(s.events, event)
		validBytes += int64(len(line))
	}
	_, err := s.journal.Seek(0, io.SeekEnd)
	return err
}

func (s *Store) recoverInterrupted() error {
	for _, bot := range s.ListBots() {
		if !isActiveStatus(bot.Status) {
			continue
		}
		turnID := ""
		lastStatus := ""
		for i := len(s.events) - 1; i >= 0; i-- {
			event := s.events[i]
			if event.BotID != bot.ID || event.TurnID == "" {
				continue
			}
			if turnID == "" {
				turnID = event.TurnID
			}
			if event.TurnID == turnID && event.Type == "turn" {
				var data struct {
					Status string `json:"status"`
				}
				if err := json.Unmarshal(event.Data, &data); err != nil {
					return fmt.Errorf("decode persisted turn status: %w", err)
				}
				lastStatus = data.Status
				break
			}
		}
		if lastStatus == "completed" || lastStatus == "stopped" || lastStatus == "error" || lastStatus == "interrupted" {
			// The journal commit can precede the final bot-state replacement.
			// Preserve that completed result instead of inventing an interruption.
			if _, err := s.UpdateBot(bot.ID, func(bot *Bot) error {
				bot.Status = "idle"
				if lastStatus == "error" || lastStatus == "interrupted" {
					bot.Status = lastStatus
				}
				return nil
			}); err != nil {
				return err
			}
			continue
		}
		if _, err := s.UpdateBot(bot.ID, func(bot *Bot) error { bot.Status = "interrupted"; return nil }); err != nil {
			return err
		}
		if _, err := s.AppendEvent(bot.ID, turnID, "turn", map[string]any{"status": "interrupted", "backend": bot.Backend, "model": bot.Model, "effort": bot.Effort, "error": "The server restarted. This turn was interrupted and was not resubmitted."}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) saveStateLocked() error {
	encoded, err := json.MarshalIndent(diskState{Version: 1, Bots: s.listBotsLocked()}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode bot state: %w", err)
	}
	return writePrivateAtomic(filepath.Join(s.root, "state.json"), append(encoded, '\n'))
}

func (s *Store) validateBotLocked(bot Bot) error {
	if strings.TrimSpace(bot.Name) == "" || len([]rune(bot.Name)) > 128 || len(bot.Role) > 32768 || len(bot.Avatar) > 512 {
		return fmt.Errorf("%w: invalid bot profile", ErrInvalid)
	}
	if bot.Backend != "codex" && bot.Backend != "pi" {
		return fmt.Errorf("%w: backend must be codex or pi", ErrInvalid)
	}
	if bot.Model == "" || len(bot.Model) > 256 || len(bot.Effort) > 32 {
		return fmt.Errorf("%w: invalid model settings", ErrInvalid)
	}
	if bot.Chief && bot.Status != "archived" {
		for _, existing := range s.bots {
			if existing.ID != bot.ID && existing.Chief && existing.Status != "archived" {
				return fmt.Errorf("%w: a coordinator already exists", ErrConflict)
			}
		}
	}
	if bot.Telegram != nil {
		if bot.Telegram.TokenEnv != "" && !envNamePattern.MatchString(bot.Telegram.TokenEnv) {
			return fmt.Errorf("%w: tokenEnv must name an environment variable", ErrInvalid)
		}
		if bot.Telegram.Enabled && (bot.Telegram.TokenEnv == "" || len(bot.Telegram.AllowedUserIDs) == 0) {
			return fmt.Errorf("%w: Telegram requires a token variable and allowed owner IDs", ErrInvalid)
		}
		for _, id := range bot.Telegram.AllowedUserIDs {
			if id == "" || strings.Trim(id, "0123456789") != "" {
				return fmt.Errorf("%w: invalid Telegram user ID", ErrInvalid)
			}
		}
	}
	return nil
}

func applyBotDefaults(bot *Bot) {
	bot.Name = strings.TrimSpace(bot.Name)
	if bot.Backend == "" {
		bot.Backend = "codex"
	}
	if bot.Model == "" {
		if bot.Backend == "pi" {
			bot.Model = "deepseek/deepseek-flash"
		} else {
			bot.Model = "gpt-6-sol"
		}
	}
	if bot.Effort == "" {
		bot.Effort = "max"
	}
	if bot.Avatar == "" {
		bot.Avatar = "bot"
	}
	if bot.DisabledSkills == nil {
		bot.DisabledSkills = []string{}
	}
}

func isActiveStatus(status string) bool {
	switch status {
	case "running", "working", "thinking", "starting", "stopping", "waiting_permission":
		return true
	default:
		return false
	}
}

func cloneBot(bot Bot) Bot {
	threads := make(map[string]string, len(bot.Threads))
	for backend, id := range bot.Threads {
		threads[backend] = id
	}
	bot.Threads = threads
	bot.DisabledSkills = append([]string{}, bot.DisabledSkills...)
	if bot.Telegram != nil {
		binding := *bot.Telegram
		binding.AllowedUserIDs = append([]string{}, binding.AllowedUserIDs...)
		bot.Telegram = &binding
	}
	return bot
}

func sameTelegramConfig(a, b *TelegramBinding) bool {
	if a == nil || b == nil || a.Enabled != b.Enabled || a.TokenEnv != b.TokenEnv || a.Username != b.Username || len(a.AllowedUserIDs) != len(b.AllowedUserIDs) {
		return false
	}
	for i := range a.AllowedUserIDs {
		if a.AllowedUserIDs[i] != b.AllowedUserIDs[i] {
			return false
		}
	}
	return true
}

func cloneEvent(event Event) Event {
	event.Data = append(json.RawMessage(nil), event.Data...)
	return event
}

func randomID(prefix string) (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate identifier: %w", err)
	}
	return prefix + hex.EncodeToString(value[:]), nil
}

func privateDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return fmt.Errorf("create private directory: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("private path is not a directory")
	}
	return os.Chmod(path, 0700)
}

func writeInitialFile(path string, content []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("create instruction file: %w", err)
	}
	if _, err := file.Write(content); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func writePrivateAtomic(path string, content []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".state-*")
	if err != nil {
		return fmt.Errorf("create atomic state file: %w", err)
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(content); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("replace state file: %w", err)
	}
	return syncStoreDirectory(filepath.Dir(path))
}
