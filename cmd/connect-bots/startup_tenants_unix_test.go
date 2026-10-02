//go:build !windows

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/bots"
)

// A persistent account must resume its background services at host startup,
// even when nobody has opened that account in the browser since the restart.
func TestMultiUserCommandRestoresTenantBackgroundServicesBeforeVisit(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data")
	accounts, err := bots.OpenAccountStore(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = accounts.Close() })
	if _, err := accounts.Register("owner", "test owner password", true); err != nil {
		t.Fatal(err)
	}
	secondary, err := accounts.Register("secondary", "test secondary password", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := accounts.Close(); err != nil {
		t.Fatal(err)
	}
	secondaryRoot := filepath.Join(root, "users", secondary.ID)
	store, err := bots.OpenStore(secondaryRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	bot, err := store.CreateBot(bots.Bot{Name: "Persistent assistant", Telegram: &bots.TelegramBinding{}})
	if err != nil {
		t.Fatal(err)
	}
	// Stale delivery status gives an observable proof that Telegram.Sync ran,
	// without credentials, a network connection or an inference request.
	if _, err := store.UpdateBot(bot.ID, func(bot *bots.Bot) error {
		bot.Telegram.Status = "connected"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(bot.ID, "previous-turn", "assistant", map[string]string{"text": "Persistent history"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	_, _, stop := startMultiUserCommand(t, root)
	// Readiness only requested the public health route. These store locks are
	// held already, rather than being acquired lazily by a tenant API request.
	for _, workspaceRoot := range []string{root, secondaryRoot} {
		if unexpected, err := bots.OpenStore(workspaceRoot); err == nil {
			unexpected.Close()
			t.Fatalf("workspace %s was not restored before account visit", workspaceRoot)
		}
	}
	if unexpected, err := bots.OpenAccountStore(root); err == nil {
		unexpected.Close()
		t.Fatal("host did not retain its account store lock")
	}
	var state struct {
		Bots []bots.Bot `json:"bots"`
	}
	content, err := os.ReadFile(filepath.Join(secondaryRoot, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(content, &state); err != nil {
		t.Fatal(err)
	}
	telegramRestored := false
	for _, restored := range state.Bots {
		if restored.ID == bot.ID && restored.Telegram != nil && restored.Telegram.Status == "disabled" {
			telegramRestored = true
		}
	}
	if !telegramRestored {
		t.Fatal("persistent tenant Telegram status was not synchronized at startup")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var inventory struct {
			Reports []bots.MaintenanceReport `json:"reports"`
		}
		content, readErr := os.ReadFile(filepath.Join(secondaryRoot, "maintenance.json"))
		if readErr == nil {
			if err := json.Unmarshal(content, &inventory); err != nil {
				t.Fatal(err)
			}
			for _, report := range inventory.Reports {
				if report.BotID == bot.ID && report.Status == "completed" {
					goto inventoryCompleted
				}
			}
		} else if !os.IsNotExist(readErr) {
			t.Fatal(readErr)
		}
		if time.Now().After(deadline) {
			t.Fatal("persistent tenant maintenance did not run before account visit")
		}
		time.Sleep(20 * time.Millisecond)
	}

inventoryCompleted:
	stop()
	reopenedAccounts, err := bots.OpenAccountStore(root)
	if err != nil {
		t.Fatalf("host shutdown retained its account store lock: %v", err)
	}
	defer reopenedAccounts.Close()
	for _, workspaceRoot := range []string{root, secondaryRoot} {
		reopened, err := bots.OpenStore(workspaceRoot)
		if err != nil {
			t.Fatalf("shutdown retained restored workspace lock: %v", err)
		}
		if workspaceRoot == secondaryRoot {
			events, err := reopened.Events(bot.ID, 0)
			if err != nil {
				reopened.Close()
				t.Fatal(err)
			}
			historyPreserved := false
			for _, event := range events {
				if event.Type == "assistant" && event.TurnID == "previous-turn" {
					var message struct {
						Text string `json:"text"`
					}
					if err := json.Unmarshal(event.Data, &message); err != nil {
						reopened.Close()
						t.Fatal(err)
					}
					historyPreserved = message.Text == "Persistent history"
				}
			}
			if !historyPreserved {
				reopened.Close()
				t.Fatal("restoring background services changed the account's conversation history")
			}
		}
		if err := reopened.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
