package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTenantPiConfigUsesEnvironmentReferenceAndPrivateAdaptiveFlashRegistry(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "test-host-secret-must-not-be-persisted")
	root := t.TempDir()
	if err := provisionTenantPiConfig(root); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, ".pi", "agent")
	content, err := os.ReadFile(filepath.Join(configPath, "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), os.Getenv("DEEPSEEK_API_KEY")) {
		t.Fatal("provider registry persisted the host secret")
	}
	var registry struct {
		Providers map[string]struct {
			APIKey  string `json:"apiKey"`
			API     string `json:"api"`
			BaseURL string `json:"baseUrl"`
			Models  []struct {
				ID               string             `json:"id"`
				API              string             `json:"api"`
				BaseURL          string             `json:"baseUrl"`
				ContextWindow    int                `json:"contextWindow"`
				MaxTokens        int                `json:"maxTokens"`
				ThinkingLevelMap map[string]*string `json:"thinkingLevelMap"`
				Compat           map[string]bool    `json:"compat"`
			} `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(content, &registry); err != nil {
		t.Fatal(err)
	}
	provider := registry.Providers["deepseek"]
	if len(registry.Providers) != 1 || provider.APIKey != "$DEEPSEEK_API_KEY" || provider.API != "anthropic-messages" || provider.BaseURL != "https://api.deepseek.com/anthropic" || len(provider.Models) != 1 {
		t.Fatal("private DeepSeek provider registry is incorrect")
	}
	model := provider.Models[0]
	if model.ID != "deepseek-flash" || model.API != provider.API || model.BaseURL != provider.BaseURL || model.ContextWindow != 1000000 || model.MaxTokens != 384000 || !model.Compat["forceAdaptiveThinking"] {
		t.Fatal("private DeepSeek model metadata is incorrect")
	}
	for _, level := range []string{"low", "high", "max"} {
		value := model.ThinkingLevelMap[level]
		if value == nil || *value != level {
			t.Fatalf("DeepSeek thinking level %s was not provisioned", level)
		}
	}
	for _, level := range []string{"minimal", "medium", "xhigh"} {
		if value, exists := model.ThinkingLevelMap[level]; !exists || value != nil {
			t.Fatalf("unsupported thinking level %s was enabled", level)
		}
	}
	settings, err := os.ReadFile(filepath.Join(configPath, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(settings, &values); err != nil {
		t.Fatal(err)
	}
	if len(values) != 3 || string(values["defaultProvider"]) != `"deepseek"` || string(values["defaultModel"]) != `"deepseek-flash"` {
		t.Fatal("account settings inherited unintended host options")
	}
	for _, path := range []string{filepath.Join(root, ".pi"), configPath, filepath.Join(configPath, "models.json"), filepath.Join(configPath, "settings.json")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0600)
		if info.IsDir() {
			want = 0700
		}
		if info.Mode().Perm() != want {
			t.Fatalf("account Pi path has mode %o; want %o", info.Mode().Perm(), want)
		}
	}
	if _, err := os.Stat(filepath.Join(configPath, "auth.json")); !os.IsNotExist(err) {
		t.Fatal("account provisioning created an authentication file")
	}
}

func TestTenantPiConfigPreservesExistingFilesAndRejectsSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := provisionTenantPiConfig(root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".pi", "agent", "models.json")
	custom := []byte(`{"providers":{"account-local":{"models":[]}}}`)
	if err := os.WriteFile(path, custom, 0600); err != nil {
		t.Fatal(err)
	}
	if err := provisionTenantPiConfig(root); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != string(custom) {
		t.Fatal("account provider customization was overwritten")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, ".pi", "agent", "settings.json"), path); err != nil {
		t.Fatal(err)
	}
	if err := provisionTenantPiConfig(root); err == nil {
		t.Fatal("symbolic Pi configuration file was accepted")
	}
	other := t.TempDir()
	if err := os.Symlink(root, filepath.Join(other, ".pi")); err != nil {
		t.Fatal(err)
	}
	if err := provisionTenantPiConfig(other); err == nil {
		t.Fatal("symbolic Pi configuration directory was accepted")
	}
}
