package bots

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestRuntimeCodexConnectionIsAuthoritativeForBotsCatalogAndIdleStop(t *testing.T) {
	store, r, factory, bot := setupRuntime(t)
	r.cfg.AgentOptions["codex"] = map[string]any{"app_server_url": "unix:///product-owned/server.sock", "codex_home": "/native-user-home", "env": map[string]string{"CODEX_HOME": "/native-user-home"}}
	r.cfg.SessionOptions = func(string) (map[string]any, error) {
		return map[string]any{"app_server_url": "managed://", "codex_home": "/wrong-home", "env": map[string]string{"CODEX_HOME": "/wrong-home"}}, nil
	}
	sent := completeRuntimePrompt(t, r, factory, bot.ID, "own transport", "own answer")
	if sent.session.agent.opts["app_server_url"] != "unix:///product-owned/server.sock" || sent.session.agent.opts["codex_home"] != "/native-user-home" {
		t.Fatalf("workspace options redirected product transport: %+v", sent.session.agent.opts)
	}
	r = restartTestRuntime(t, r, store, factory)
	if err := r.Stop(context.Background(), bot.ID); err != nil {
		t.Fatal(err)
	}
	factory.mu.Lock()
	control := factory.created[len(factory.created)-1]
	factory.mu.Unlock()
	if control.resume != sent.session.id || control.opts["app_server_url"] != "unix:///product-owned/server.sock" {
		t.Fatalf("idle Stop connected to a different server: resume=%s opts=%+v", control.resume, control.opts)
	}
	// Remove all Codex bots so discovery exercises the separate temporary path.
	_, err := store.UpdateBot(bot.ID, func(bot *Bot) error { bot.Backend = "pi"; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if caps, err := r.Capabilities(context.Background()); err != nil || !caps.Backends["codex"].Available {
		t.Fatalf("temporary catalog: %+v %v", caps, err)
	}
	factory.mu.Lock()
	defer factory.mu.Unlock()
	foundTemporary := false
	for _, created := range factory.created {
		if created.backend != "codex" {
			continue
		}
		if created.opts["app_server_url"] != "unix:///product-owned/server.sock" {
			t.Fatalf("Codex creation escaped product server: %+v", created.opts)
		}
		if strings.HasSuffix(created.opts["work_dir"].(string), "/catalog/codex") {
			foundTemporary = true
		}
	}
	if !foundTemporary {
		t.Fatal("test did not exercise temporary Codex catalog")
	}
}

func TestRuntimeCodexRejectsMissingAndManagedEndpointsBeforeAgentCreation(t *testing.T) {
	for _, endpoint := range []string{"", "managed://", "auto", "stdio://", "/tmp/implicit.sock"} {
		t.Run(endpoint, func(t *testing.T) {
			_, r, factory, bot := setupRuntime(t)
			r.cfg.AgentOptions = map[string]map[string]any{"codex": {"app_server_url": endpoint}}
			turnID, err := r.SendMessage(context.Background(), bot.ID, MessageRequest{Text: "never use shared server"})
			if err != nil {
				t.Fatal(err)
			}
			result := waitRuntimeTurn(t, r, bot.ID, turnID)
			if result.Status != "error" || !strings.Contains(result.Error, "dedicated Codex app-server") {
				t.Fatalf("unconfigured product connection was accepted: %+v", result)
			}
			factory.mu.Lock()
			created := len(factory.created)
			factory.mu.Unlock()
			if created != 0 {
				t.Fatal("invalid URL reached a factory with implicit fallback")
			}
		})
	}
}

func TestRuntimeDedicatedEndpointRestartResumesSameNativeThreadAndGoal(t *testing.T) {
	store, r, factory, bot := setupRuntime(t)
	factory.strictResume = true
	first := completeRuntimePrompt(t, r, factory, bot.ID, "keep this conversation", "persistent answer")
	if _, err := r.Goal(context.Background(), bot.ID, "set", map[string]any{"objective": "Retain my paused goal", "status": "paused", "tokenBudget": 1000}); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	newEndpoint := "unix:///next-product-run/app-server.sock"
	r = NewRuntime(store, RuntimeConfig{AgentFactory: factory.create, AgentOptions: map[string]map[string]any{"codex": {"app_server_url": newEndpoint}}})
	t.Cleanup(func() { _ = r.Close() })
	snapshot, err := r.Goal(context.Background(), bot.ID, "get", nil)
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Goal struct{ Objective, Status string }
	}
	encoded, _ := json.Marshal(snapshot)
	_ = json.Unmarshal(encoded, &response)
	if response.Goal.Objective != "Retain my paused goal" || response.Goal.Status != "paused" {
		t.Fatalf("new transport lost native goal: %+v", response)
	}
	factory.mu.Lock()
	resumed := factory.created[len(factory.created)-1]
	factory.mu.Unlock()
	if resumed.resume != first.session.id || resumed.opts["app_server_url"] != newEndpoint {
		t.Fatalf("random endpoint rotated durable conversation: resume=%s opts=%+v", resumed.resume, resumed.opts)
	}
	if _, err := r.Goal(context.Background(), bot.ID, "set", map[string]any{"status": "active"}); err != nil {
		t.Fatal(err)
	}
	select {
	case send := <-factory.sends:
		t.Fatalf("endpoint-only restart created a context handoff turn: %s", send.prompt)
	default:
	}
	updated, _ := store.GetBot(bot.ID)
	if updated.Threads["codex"] != first.session.id {
		t.Fatal("endpoint-only restart replaced native thread ID")
	}
}
