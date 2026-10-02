package bots

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestMaintenancePiPreservesTenantEnvironmentWithoutBotCredentials(t *testing.T) {
	for _, shape := range []string{"string-map", "any-map"} {
		t.Run(shape, func(t *testing.T) {
			store, _, bot := workspaceFixture(t)
			home := store.Root()
			configuredEnv := map[string]string{
				"HOME": home, "PI_CODING_AGENT_DIR": filepath.Join(home, ".pi", "agent"),
				"DEEPSEEK_API_KEY":            "tenant-provider-fixture",
				"CONNECT_BOTS_INTERNAL_TOKEN": "bot-credential-fixture",
				"CONNECT_BOTS_API_URL":        "http://127.0.0.1/internal", "CONNECT_BOTS_BOT_ID": bot.ID,
			}
			var rawEnv any = configuredEnv
			if shape == "any-map" {
				env := map[string]any{}
				for key, value := range configuredEnv {
					env[key] = value
				}
				rawEnv = env
			}
			factory := &runtimeFakeFactory{sends: make(chan runtimeFakeSend, 1)}
			runtime := NewRuntime(store, RuntimeConfig{
				AgentOptions: map[string]map[string]any{"pi": {
					"cmd": "/tenant/bin/pi", "env": rawEnv,
					"cli_args":    []string{"--extension", "/persistent/coordinator"},
					"session_dir": "/persistent/sessions", "mode": "yolo",
				}},
				AgentFactory: func(backend string, opts map[string]any) (core.Agent, error) {
					agent, err := factory.create(backend, opts)
					if err == nil {
						agent.(*runtimeFakeAgent).session.emit(core.Event{Type: core.EventResult, Content: "Tenant inventory.", Done: true})
					}
					return agent, err
				},
			})
			defer runtime.Close()
			maintenance := NewMaintenance(store, runtime)
			defer maintenance.Close()
			settings := MaintenanceSettings{Backend: "pi", Model: "deepseek/deepseek-flash", Effort: "off", RetentionHours: 24}
			result, err := maintenance.runInventoryAgent(context.Background(), settings, filepath.Join(bot.WorkDir, "tmp"), "draft.txt")
			if err != nil || result != "Tenant inventory." {
				t.Fatalf("inventory result=%q error=%v", result, err)
			}
			if len(factory.created) != 1 {
				t.Fatalf("inventory agents=%d", len(factory.created))
			}
			opts := factory.created[0].opts
			if opts["cmd"] != "/tenant/bin/pi" {
				t.Fatal("inventory lost tenant CLI")
			}
			env := runtimeEnv(opts["env"])
			for _, key := range []string{"HOME", "PI_CODING_AGENT_DIR", "DEEPSEEK_API_KEY"} {
				if env[key] != configuredEnv[key] {
					t.Fatalf("inventory lost tenant environment key %s", key)
				}
			}
			for key := range env {
				if strings.HasPrefix(key, "CONNECT_BOTS_") {
					t.Fatalf("inventory inherited bot credential %s", key)
				}
			}
			if opts["mode"] != "default" || opts["session_dir"] != filepath.Join(bot.WorkDir, "state", "maintenance", "pi") {
				t.Fatal("inventory inherited persistent bot state or permissions")
			}
			args, err := runtimeArgs(opts["cli_args"])
			if err != nil || strings.Contains(strings.Join(args, " "), "/persistent/coordinator") {
				t.Fatal("inventory inherited persistent bot extensions")
			}
			// The inventory's environment is independently owned: filtering or
			// subsequent factory changes cannot alter the tenant runtime config.
			opts["env"].(map[string]string)["HOME"] = "/changed/by/factory"
			originalEnv := runtimeEnv(rawEnv)
			if originalEnv["HOME"] != home || originalEnv["CONNECT_BOTS_INTERNAL_TOKEN"] != "bot-credential-fixture" {
				t.Fatal("inventory mutated tenant runtime environment")
			}
		})
	}
}
