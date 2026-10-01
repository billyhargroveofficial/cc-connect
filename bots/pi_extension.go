package bots

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed extensions/pi-tools.ts
var piToolsExtension []byte

// InstallPiExtension installs the bundled bridge without changing Pi's global
// settings or extensions. Each product session loads this file explicitly.
func InstallPiExtension(root string) (string, error) {
	dir := filepath.Join(root, "extensions")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create extension directory: %w", err)
	}
	path := filepath.Join(dir, "connect-bots.ts")
	if err := os.WriteFile(path, piToolsExtension, 0600); err != nil {
		return "", fmt.Errorf("install Pi bridge: %w", err)
	}
	return path, nil
}
