package bots

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestServiceTierPersistsAndBackendSwitchResetsIt(t *testing.T) {
	root := t.TempDir()
	store, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	bot, err := store.CreateBot(Bot{Name: "Fast bot", ServiceTier: "priority"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	bot, err = reopened.GetBot(bot.ID)
	if err != nil || bot.ServiceTier != "priority" {
		t.Fatalf("service tier lost on restart: %+v %v", bot, err)
	}
	bot, err = reopened.PatchBot(bot.ID, map[string]json.RawMessage{"backend": json.RawMessage(`"pi"`)})
	if err != nil || bot.ServiceTier != "" {
		t.Fatalf("Pi inherited a Codex tier: %+v %v", bot, err)
	}
	if _, err := reopened.PatchBot(bot.ID, map[string]json.RawMessage{"serviceTier": json.RawMessage(`"priority"`)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Pi accepted an unsupported service tier: %v", err)
	}
	for _, invalid := range []string{"bad tier", "FAST", "../priority"} {
		if _, err := reopened.CreateBot(Bot{Name: "Invalid tier", ServiceTier: invalid}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid tier %q accepted: %v", invalid, err)
		}
	}
}

func TestServiceTierHTTPCreatePatchAndAutomaticSelection(t *testing.T) {
	_, _, host := testServer(t)
	cookie := loginCookie(t, host)
	response := requestHTTP(t, host, http.MethodPost, "/api/studio/bots", `{"name":"Fast bot","serviceTier":"priority"}`, host.URL, cookie)
	var bot Bot
	decodeErr := json.NewDecoder(response.Body).Decode(&bot)
	response.Body.Close()
	if response.StatusCode != http.StatusCreated || decodeErr != nil || bot.ServiceTier != "priority" {
		t.Fatalf("create service tier: status=%d bot=%+v err=%v", response.StatusCode, bot, decodeErr)
	}
	response = requestHTTP(t, host, http.MethodPatch, "/api/studio/bots/"+bot.ID, `{"serviceTier":""}`, host.URL, cookie)
	decodeErr = json.NewDecoder(response.Body).Decode(&bot)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || decodeErr != nil || bot.ServiceTier != "" {
		t.Fatalf("automatic selection: status=%d bot=%+v err=%v", response.StatusCode, bot, decodeErr)
	}
}

func TestServiceTierChangesResumeHistoryAndJournalEachTurnSelection(t *testing.T) {
	store, runtime, factory, bot := setupRuntime(t)
	server, err := NewServer(store, runtime, ServerConfig{Token: testOwnerToken})
	if err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(server.Handler())
	t.Cleanup(func() { host.Close(); _ = server.Close() })
	cookie := loginCookie(t, host)
	threadID := ""
	var previousSession *runtimeFakeSession
	for _, tier := range []string{"", "priority", ""} {
		response := requestHTTP(t, host, http.MethodPatch, "/api/studio/bots/"+bot.ID, string(mustRaw(t, map[string]string{"serviceTier": tier})), host.URL, cookie)
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("tier-only patch failed: %d", response.StatusCode)
		}
		if threadID != "" {
			// Resuming a goal/control request before an owner prompt must also
			// observe the new setting on the same loaded thread.
			if _, err := runtime.Goal(context.Background(), bot.ID, "get", nil); err != nil {
				t.Fatal(err)
			}
		}
		turnID, err := runtime.SendMessage(context.Background(), bot.ID, MessageRequest{Text: "Continue the same conversation"})
		if err != nil {
			t.Fatal(err)
		}
		send := nextRuntimeSend(t, factory)
		if threadID == "" {
			if initial, configured := send.session.agent.opts["service_tier"]; !configured || initial != "" {
				t.Fatalf("initial automatic option lost: %v", send.session.agent.opts)
			}
		} else if send.session.id != threadID || send.session != previousSession {
			t.Fatalf("tier switch discarded the loaded native session: id=%q want=%q", send.session.id, threadID)
		} else {
			send.session.mu.Lock()
			var update map[string]any
			for i, method := range send.session.rpcCalls {
				if method == "thread/settings/update" {
					update = send.session.rpcParams[i]
				}
			}
			send.session.mu.Unlock()
			var expected any
			if tier != "" {
				expected = tier
			}
			if update == nil || update["threadId"] != threadID || update["serviceTier"] != expected {
				t.Fatalf("native tier update missing: tier=%q params=%v", tier, update)
			}
		}
		threadID = send.session.id
		previousSession = send.session
		response = requestHTTP(t, host, http.MethodPatch, "/api/studio/bots/"+bot.ID, `{"serviceTier":"priority"}`, host.URL, cookie)
		response.Body.Close()
		if response.StatusCode != http.StatusConflict {
			t.Fatalf("tier edit raced an active turn: %d", response.StatusCode)
		}
		send.session.complete("Done")
		waitRuntimeTurn(t, runtime, bot.ID, turnID)
		events, err := store.Events(bot.ID, 0)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, event := range events {
			if event.TurnID != turnID || event.Type != "turn" {
				continue
			}
			var payload map[string]any
			if err := json.Unmarshal(event.Data, &payload); err != nil {
				t.Fatal(err)
			}
			if payload["status"] == "completed" {
				found = payload["serviceTier"] == tier
			}
		}
		if !found {
			t.Fatalf("completed turn has no tier metadata: %s %q", turnID, tier)
		}
	}
}

func mustRaw(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

type serviceTierCatalogRPC struct{}

func (serviceTierCatalogRPC) RPC(_ context.Context, method string, _ any, result any) error {
	if method != "model/list" {
		return errors.New("unexpected discovery method")
	}
	return json.Unmarshal([]byte(`{"data":[
		{"id":"sol","model":"gpt-6-sol","serviceTiers":[{"id":"priority","name":"Fast","description":"Faster responses"},{"id":"priority","name":"Duplicate"},{"id":""}],"defaultServiceTier":"priority"},
		{"id":"other","model":"another-model"}
	]}`), result)
}

func TestServiceTierCatalogRetainsOnlyAdvertisedModelSpecificOptions(t *testing.T) {
	models, err := codexCatalog(context.Background(), serviceTierCatalogRPC{})
	if err != nil || len(models) != 2 {
		t.Fatalf("catalog models=%+v err=%v", models, err)
	}
	if len(models[0].ServiceTiers) != 1 || models[0].ServiceTiers[0].ID != "priority" || models[0].ServiceTiers[0].Name != "Fast" || models[0].ServiceTiers[0].Description != "Faster responses" || models[0].DefaultServiceTier != "priority" {
		t.Fatalf("real catalog tier metadata lost: %+v", models[0])
	}
	if models[1].ServiceTiers == nil || len(models[1].ServiceTiers) != 0 || models[1].DefaultServiceTier != "" {
		t.Fatalf("tiers fabricated for another model: %+v", models[1])
	}
}
