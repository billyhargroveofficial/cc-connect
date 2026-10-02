package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// This registry contains public model metadata and an environment reference.
// No owner settings, packages, extensions or authentication files are copied.
const tenantPiModels = `{
  "providers": {
    "deepseek": {
      "apiKey": "$DEEPSEEK_API_KEY",
      "api": "anthropic-messages",
      "baseUrl": "https://api.deepseek.com/anthropic",
      "models": [{
        "id": "deepseek-flash",
        "name": "DeepSeek V4.1 Flash",
        "api": "anthropic-messages",
        "baseUrl": "https://api.deepseek.com/anthropic",
        "reasoning": true,
        "input": ["text", "image"],
        "contextWindow": 1000000,
        "maxTokens": 384000,
        "thinkingLevelMap": {"minimal": null, "low": "low", "medium": null, "high": "high", "xhigh": null, "max": "max"},
        "compat": {"forceAdaptiveThinking": true}
      }]
    }
  }
}
`

const tenantPiSettings = `{
  "defaultProvider": "deepseek",
  "defaultModel": "deepseek-flash",
  "enabledModels": ["deepseek/deepseek-flash"]
}
`

func provisionTenantPiConfig(root string) error {
	workspace, err := os.OpenRoot(root)
	if err != nil {
		return fmt.Errorf("open account Pi configuration root: %w", err)
	}
	defer workspace.Close()
	for _, dir := range []string{".pi", filepath.Join(".pi", "agent")} {
		if err := workspace.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("create account Pi configuration: %w", err)
		}
		info, err := workspace.Lstat(dir)
		if err != nil {
			return fmt.Errorf("inspect account Pi configuration: %w", err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("account Pi configuration must not contain symbolic links")
		}
		if err := workspace.Chmod(dir, 0700); err != nil {
			return fmt.Errorf("secure account Pi configuration: %w", err)
		}
	}
	for _, file := range []struct {
		name, content string
	}{{"models.json", tenantPiModels}, {"settings.json", tenantPiSettings}} {
		if err := writeTenantPiInitialFile(workspace, filepath.Join(".pi", "agent", file.name), file.content); err != nil {
			return fmt.Errorf("initialize account Pi %s: %w", file.name, err)
		}
	}
	return nil
}

func writeTenantPiInitialFile(root *os.Root, path, content string) (writeErr error) {
	file, err := root.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if errors.Is(err, os.ErrExist) {
		info, err := root.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("expected a regular account Pi configuration file")
		}
		return root.Chmod(path, 0600)
	}
	if err != nil {
		return err
	}
	defer func() {
		writeErr = errors.Join(writeErr, file.Close())
		if writeErr != nil {
			_ = root.Remove(path)
		}
	}()
	if _, err := file.WriteString(content); err != nil {
		return err
	}
	return file.Sync()
}
