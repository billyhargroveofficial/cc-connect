package bots

import (
	"context"
	"strings"
	"testing"
)

func TestTelegramEnvironmentResolverCanDenyHostCredentials(t *testing.T) {
	store, manager, _, bot, platform := telegramTestHarness(t)
	if err := manager.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	manager.SetEnvironmentResolver(nil)
	if err := manager.Sync(context.Background()); err == nil || !strings.Contains(err.Error(), "not set") {
		t.Fatalf("blocked credential should fail resolution: %v", err)
	}
	if platform.stops.Load() != 1 {
		t.Fatal("blocking credential access did not stop its existing connection")
	}
	profile, err := store.GetBot(bot.ID)
	if err != nil || profile.Telegram.Status != "error" {
		t.Fatalf("denied credential did not surface in bot status: %+v, %v", profile.Telegram, err)
	}
}
