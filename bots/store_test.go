package bots

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return store
}

func TestStore_PersistsBotsThreadsAndEvents(t *testing.T) {
	root := t.TempDir()
	store, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if bots := store.ListBots(); len(bots) != 1 || !bots[0].Chief {
		t.Fatalf("fresh bots: %+v", bots)
	}
	bot, err := store.CreateBot(Bot{Name: "Research", Backend: "pi"})
	if err != nil {
		t.Fatal(err)
	}
	if bot.Model != "deepseek/deepseek-flash" {
		t.Fatalf("model = %s", bot.Model)
	}
	bot, err = store.UpdateBot(bot.ID, func(bot *Bot) error { bot.Threads["pi"] = "persistent-pi-thread"; return nil })
	if err != nil {
		t.Fatal(err)
	}
	last, err := store.AppendEvent(bot.ID, "turn-one", "message", map[string]any{"role": "user", "content": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(root, "state.json"), filepath.Join(root, "events.jsonl"), filepath.Join(bot.WorkDir, "AGENTS.md")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("%s permissions = %o", path, info.Mode().Perm())
		}
	}
	for _, path := range []string{filepath.Join(root, "user", "skills"), filepath.Join(bot.WorkDir, ".agents", "skills"), filepath.Join(bot.WorkDir, "tmp"), filepath.Join(bot.WorkDir, "uploads")} {
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			t.Fatalf("missing directory %s: %v", path, err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.GetBot(bot.ID)
	if err != nil || got.Threads["pi"] != "persistent-pi-thread" {
		t.Fatalf("persisted bot: %+v, %v", got, err)
	}
	next, err := reopened.AppendEvent(bot.ID, "", "system", map[string]string{"content": "next"})
	if err != nil || next.Seq != last.Seq+1 {
		t.Fatalf("next event: %+v, %v", next, err)
	}
}

func TestStore_ExclusiveOwnerLockReleasedOnClose(t *testing.T) {
	root := t.TempDir()
	first, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if other, err := OpenStore(root); err == nil {
		other.Close()
		t.Fatal("two stores acquired the same directory")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := OpenStore(root)
	if err != nil {
		t.Fatalf("lock was not released: %v", err)
	}
	second.Close()
}

func TestStore_RecoveryMarksRunningTurnInterrupted(t *testing.T) {
	root := t.TempDir()
	store, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	bot := store.ListBots()[0]
	if _, err := store.UpdateBot(bot.ID, func(bot *Bot) error { bot.Status = "running"; bot.Threads["codex"] = "thread-one"; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(bot.ID, "turn-running", "turn", map[string]string{"status": "running"}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	reopened, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	bot, err = reopened.GetBot(bot.ID)
	if err != nil || bot.Status != "interrupted" || bot.Threads["codex"] != "thread-one" {
		t.Fatalf("recovery bot: %+v, %v", bot, err)
	}
	events, err := reopened.Events(bot.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	var data map[string]any
	if err := json.Unmarshal(last.Data, &data); err != nil {
		t.Fatal(err)
	}
	if last.TurnID != "turn-running" || data["status"] != "interrupted" {
		t.Fatalf("recovery event: %+v, %+v", last, data)
	}
}

func TestStore_RepairsOnlyIncompleteFinalJournalRecord(t *testing.T) {
	root := t.TempDir()
	store, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	last, _ := store.AppendEvent("", "", "system", map[string]string{"content": "durable"})
	store.Close()
	file, err := os.OpenFile(filepath.Join(root, "events.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	file.WriteString(`{"seq":999,"botId":`)
	file.Close()
	reopened, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	next, err := reopened.AppendEvent("", "", "system", map[string]string{"content": "after crash"})
	if err != nil || next.Seq != last.Seq+1 {
		t.Fatalf("repaired sequence: %+v %v", next, err)
	}
}

func TestStore_SubscribeConcurrentReplayHasNoGapsOrDuplicates(t *testing.T) {
	store := testStore(t)
	for i := 0; i < 30; i++ {
		if _, err := store.AppendEvent("", "", "system", i); err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	var group sync.WaitGroup
	group.Add(1)
	go func() {
		defer group.Done()
		<-start
		for i := 0; i < 150; i++ {
			if _, err := store.AppendEvent("", "", "system", i); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	close(start)
	events, cancel, err := store.Subscribe(10)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	want := uint64(11)
	deadline := time.After(10 * time.Second)
	for want <= 181 {
		select {
		case event, open := <-events:
			if !open {
				t.Fatalf("stream closed before sequence %d", want)
			}
			if event.Seq != want {
				t.Fatalf("sequence = %d, want %d", event.Seq, want)
			}
			want++
		case <-deadline:
			t.Fatalf("waiting for sequence %d", want)
		}
	}
	group.Wait()
}

func TestStore_ArchiveRetainsFilesAndJournal(t *testing.T) {
	store := testStore(t)
	bot, _ := store.CreateBot(Bot{Name: "Archive"})
	file := filepath.Join(bot.WorkDir, "artifact.txt")
	if err := os.WriteFile(file, []byte("result"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ArchiveBot(bot.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(file); err != nil {
		t.Fatal(err)
	}
	if events, err := store.Events(bot.ID, 0); err != nil || len(events) < 2 {
		t.Fatalf("history lost: %+v %v", events, err)
	}
	if _, err := store.PatchBot(bot.ID, map[string]json.RawMessage{"name": json.RawMessage(`"changed"`)}); !errors.Is(err, ErrConflict) {
		t.Fatalf("archived mutation: %v", err)
	}
}

func TestStore_PublicMutationCannotChangePathsOrThreads(t *testing.T) {
	store := testStore(t)
	bot := store.ListBots()[0]
	for _, field := range []string{"workDir", "threads", "id", "status"} {
		_, err := store.PatchBot(bot.ID, map[string]json.RawMessage{field: json.RawMessage(`"/tmp/arbitrary"`)})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("field %s was accepted: %v", field, err)
		}
	}
	if _, err := store.BotDir("../../outside"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unsafe id: %v", err)
	}
	if _, err := store.CreateBot(Bot{Name: "Second chief", Chief: true}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate coordinator: %v", err)
	}
	if _, err := store.CreateBot(Bot{Name: "Unsafe Telegram", Telegram: &TelegramBinding{Enabled: true, TokenEnv: "OWNER_BOT"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unrestricted Telegram: %v", err)
	}
}

func TestStore_ReturnedValuesCannotMutateStoredState(t *testing.T) {
	store := testStore(t)
	bot := store.ListBots()[0]
	bot.Threads["codex"] = "injected"
	got, _ := store.GetBot(bot.ID)
	if got.Threads["codex"] != "" {
		t.Fatal("returned bot shared its map")
	}
	event, _ := store.AppendEvent(bot.ID, "", "system", map[string]string{"content": "original"})
	event.Data[0] = '!'
	stored, _ := store.Events(bot.ID, event.Seq-1)
	if !json.Valid(stored[0].Data) {
		t.Fatal("returned event shared its data")
	}
}

func TestStore_SubscriptionNeverEmitsAtOrBeforeCursor(t *testing.T) {
	store := testStore(t)
	stream, cancel, err := store.Subscribe(3)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	for i := 0; i < 3; i++ {
		if _, err := store.AppendEvent("", "", "system", i); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case event := <-stream:
		if event.Seq != 4 {
			t.Fatalf("event before cursor: %+v", event)
		}
	default:
		t.Fatal("missing event after cursor")
	}
}

func TestStore_TelegramConnectionStatusCannotBeSpoofed(t *testing.T) {
	store := testStore(t)
	bot, err := store.CreateBot(Bot{Name: "Telegram", Telegram: &TelegramBinding{Enabled: true, TokenEnv: "MY_BOT_TOKEN", AllowedUserIDs: []string{"123"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateBot(bot.ID, func(bot *Bot) error { bot.Telegram.Status = "connected"; return nil }); err != nil {
		t.Fatal(err)
	}
	bot, err = store.PatchBot(bot.ID, map[string]json.RawMessage{"telegram": json.RawMessage(`{"enabled":true,"tokenEnv":"MY_BOT_TOKEN","allowedUserIds":["123"]}`)})
	if err != nil || bot.Telegram.Status != "connected" {
		t.Fatalf("unchanged binding lost actualstatus: %+v, %v", bot.Telegram, err)
	}
	if _, err := store.PatchBot(bot.ID, map[string]json.RawMessage{"telegram": json.RawMessage(`{"enabled":true,"tokenEnv":"OTHER_BOT_TOKEN","allowedUserIds":["123"],"status":"connected"}`)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("spoofed status accepted: %v", err)
	}
	bot, err = store.PatchBot(bot.ID, map[string]json.RawMessage{"telegram": json.RawMessage(`{"enabled":true,"tokenEnv":"OTHER_BOT_TOKEN","allowedUserIds":["123"]}`)})
	if err != nil || bot.Telegram.Status != "connecting" {
		t.Fatalf("changedbinding not pending: %+v %v", bot.Telegram, err)
	}
}

func TestStore_RecoveryPreservesCommittedCompletion(t *testing.T) {
	root := t.TempDir()
	store, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	bot := store.ListBots()[0]
	if _, err := store.UpdateBot(bot.ID, func(bot *Bot) error { bot.Status = "running"; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(bot.ID, "already-completed", "turn", map[string]string{"status": "completed"}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	reopened, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	bot, err = reopened.GetBot(bot.ID)
	if err != nil || bot.Status != "idle" {
		t.Fatalf("completed turn recoveredasinterrupted: %+v %v", bot, err)
	}
	events, _ := reopened.Events(bot.ID, 0)
	for _, event := range events {
		if event.Type == "turn" {
			var data map[string]any
			json.Unmarshal(event.Data, &data)
			if data["status"] == "interrupted" {
				t.Fatal("recovery contradicted committedcompletion")
			}
		}
	}
}

func TestStore_FailedSyncStopsFurtherMutations(t *testing.T) {
	store := testStore(t)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if err := store.journal.Close(); err != nil {
		t.Fatal(err)
	}
	store.journal = writer // A pipe permits writes but cannot durably sync them.
	cursor := store.Cursor()
	if _, err := store.AppendEvent("", "", "system", "uncertain"); err == nil {
		t.Fatal("failed journal sync acknowledged success")
	}
	if _, err := store.AppendEvent("", "", "system", "next"); err == nil {
		t.Fatal("append reused sequence after failed sync")
	}
	if store.Cursor() != cursor {
		t.Fatal("failed commit advanced visiblecursor")
	}
	if _, err := store.CreateBot(Bot{Name: "after failure"}); err == nil {
		t.Fatal("profile mutation continued with a broken journal")
	}
}
