package main

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/bots"
)

func testNodeConfig() nodeConfig {
	return nodeConfig{Version: 1, Credential: bots.NodeCredential{
		ServerURL: "https://bots.example.test", NodeID: "node_" + strings.Repeat("a", 32),
		Token: base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("s", 32))),
	}}
}

func TestNodeDataDirUsesNativeMacAndXDGDefaults(t *testing.T) {
	config := func() (string, error) { return "/Users/billy/Library/Application Support", nil }
	home := func() (string, error) { return "/home/billy", nil }
	for _, tt := range []struct {
		name, goos, xdg, want string
	}{
		{"mac", "darwin", "/ignored", "/Users/billy/Library/Application Support/Connect Bots/Node"},
		{"linux-xdg", "linux", "/state", "/state/connect-bots/node"},
		{"linux-home", "linux", "", "/home/billy/.local/share/connect-bots/node"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := nodeDataDir(tt.goos, config, home, func(string) string { return tt.xdg })
			if err != nil || got != filepath.FromSlash(tt.want) {
				t.Fatalf("default = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
	if _, err := nodeDataDir("linux", config, home, func(string) string { return "relative" }); err == nil {
		t.Fatal("relative XDG_DATA_HOME was accepted")
	}
}

func TestNodeConfigAtomicRoundTripAndPrivatePermissions(t *testing.T) {
	root, err := privateDataDir(filepath.Join(t.TempDir(), "node"))
	if err != nil {
		t.Fatal(err)
	}
	want := testNodeConfig()
	want.AllowInsecure = true
	if err := saveNodeConfig(root, want); err != nil {
		t.Fatal(err)
	}
	got, gotRoot, err := loadNodeConfig(root)
	if err != nil || got != want || gotRoot != root {
		t.Fatalf("loaded = %+v, %q, %v", got.Credential.NodeID, gotRoot, err)
	}
	for _, tt := range []struct {
		path string
		mode os.FileMode
	}{{root, 0700}, {filepath.Join(root, "node.json"), 0600}} {
		info, err := os.Stat(tt.path)
		if err != nil || info.Mode().Perm() != tt.mode {
			t.Fatalf("private mode = %v, %v; want %v", info, err, tt.mode)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".node-config-") {
			t.Fatal("atomic write left a temporary credential file")
		}
	}
	want.Credential.NodeID = "node_" + strings.Repeat("b", 32)
	if err := saveNodeConfig(root, want); err != nil {
		t.Fatal(err)
	}
	got, _, err = loadNodeConfig(root)
	if err != nil || got != want {
		t.Fatal("atomic replacement did not preserve the new configuration", err)
	}
}

func TestNodeConfigRejectsSymlinksAndPublicPermissions(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("Unix filesystem permissions")
	}
	base := t.TempDir()
	root, err := privateDataDir(filepath.Join(base, "node"))
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(base, "outside.json")
	if err := os.WriteFile(outside, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "node.json")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadNodeConfig(root); err == nil {
		t.Fatal("symlink configuration was read")
	}
	if err := saveNodeConfig(root, testNodeConfig()); err == nil {
		t.Fatal("symlink configuration was overwritten")
	}
	if content, _ := os.ReadFile(outside); string(content) != "unchanged" {
		t.Fatal("configuration write followed the symlink")
	}
	if err := os.Remove(filepath.Join(root, "node.json")); err != nil {
		t.Fatal(err)
	}
	if err := saveNodeConfig(root, testNodeConfig()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "node.json"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadNodeConfig(root); err == nil {
		t.Fatal("public credential file was read")
	}
	if err := os.Chmod(filepath.Join(root, "node.json"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadNodeConfig(root); err == nil {
		t.Fatal("public node directory was accepted")
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "linked-node")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if _, err := privateDataDir(link); err == nil {
		t.Fatal("symlink node directory was accepted")
	}
}

func TestNodeConfigRejectsMalformedCredentialsWithoutLeakingValues(t *testing.T) {
	root, err := privateDataDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	secret := "PRIVATE_CONFIG_VALUE"
	for _, raw := range []string{
		`{"version":1,"credential":{"serverUrl":"https://bots.example.test","nodeId":"` + secret + `","token":"` + secret + `"}}`,
		`{"version":1,"credential":{"serverUrl":` + secret + `}}`,
		`{"version":1,"unexpected":"` + secret + `"}`,
		strings.Repeat(secret, maxNodeConfigBytes),
	} {
		if err := os.WriteFile(filepath.Join(root, "node.json"), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		_, _, err := loadNodeConfig(root)
		if err == nil || strings.Contains(err.Error(), secret) {
			t.Fatalf("unsafe config diagnostic: %v", err)
		}
	}
	config := testNodeConfig()
	config.Credential.ServerURL = "https://" + secret + "@bots.example.test"
	if err := validateNodeConfig(config); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("unsafe URL diagnostic: %v", err)
	}
}

func TestNodeConfigMissingDoesNotCreateAWorkspace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing")
	if _, _, err := loadNodeConfig(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing config: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a status read created a workspace")
	}
}
