package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/chenhg5/cc-connect/bots"
)

const nodeConfigVersion = 1
const maxNodeConfigBytes = 16 << 10

var nodeIDPattern = regexp.MustCompile(`^node_[a-f0-9]{32}$`)

type nodeConfig struct {
	Version       int                 `json:"version"`
	Credential    bots.NodeCredential `json:"credential"`
	AllowInsecure bool                `json:"allowInsecure,omitempty"`
}

func defaultDataDir() (string, error) {
	return nodeDataDir(runtime.GOOS, os.UserConfigDir, os.UserHomeDir, os.Getenv)
}

func nodeDataDir(goos string, configDir, homeDir func() (string, error), getenv func(string) string) (string, error) {
	if goos == "darwin" || goos == "windows" {
		config, err := configDir()
		if err != nil {
			return "", fmt.Errorf("resolve node data directory: %w", err)
		}
		return filepath.Join(config, "Connect Bots", "Node"), nil
	}
	if stateHome := strings.TrimSpace(getenv("XDG_DATA_HOME")); stateHome != "" {
		if !filepath.IsAbs(stateHome) {
			return "", errors.New("XDG_DATA_HOME must be an absolute directory")
		}
		return filepath.Join(stateHome, "connect-bots", "node"), nil
	}
	home, err := homeDir()
	if err != nil {
		return "", fmt.Errorf("resolve node home directory: %w", err)
	}
	return filepath.Join(home, ".local", "share", "connect-bots", "node"), nil
}

// The managed directory itself cannot be a symlink. Canonicalize its parent
// after that check so normal OS aliases such as macOS /var remain usable;
// subsequent config operations use os.Root rather than following path changes.
func privateDataDir(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", errors.New("node data directory is required")
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", fmt.Errorf("resolve node data directory: %w", err)
	}
	info, err := os.Lstat(abs)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect node data directory: %w", err)
	}
	if err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return "", errors.New("node data directory must be a directory without a symbolic link")
	}
	if err := os.MkdirAll(abs, 0700); err != nil {
		return "", fmt.Errorf("create node data directory: %w", err)
	}
	info, err = os.Lstat(abs)
	if err != nil {
		return "", fmt.Errorf("inspect node data directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("node data directory must be a directory without a symbolic link")
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve node data directory: %w", err)
	}
	if err := os.Chmod(resolved, 0700); err != nil {
		return "", fmt.Errorf("secure node data directory: %w", err)
	}
	return resolved, nil
}

func existingDataDir(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", errors.New("node data directory is required")
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", fmt.Errorf("resolve node data directory: %w", err)
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("node data directory must be a directory without a symbolic link")
	}
	if info.Mode().Perm()&0077 != 0 {
		return "", errors.New("node data directory must have private permissions (0700)")
	}
	return filepath.EvalSymlinks(abs)
}

func pairingDestination(root string, replace bool) error {
	capability, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer capability.Close()
	info, err := capability.Lstat("node.json")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect node configuration: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("node configuration must be a regular file without a symbolic link")
	}
	if !replace {
		return errors.New("node is already paired; use --replace to deliberately replace its pairing or a different --data directory")
	}
	return nil
}

func validateNodeConfig(config nodeConfig) error {
	if config.Version != nodeConfigVersion {
		return errors.New("unsupported node configuration version")
	}
	if !nodeIDPattern.MatchString(config.Credential.NodeID) {
		return errors.New("invalid saved node identity")
	}
	token, err := base64.RawURLEncoding.DecodeString(config.Credential.Token)
	if err != nil || len(token) != 32 {
		return errors.New("invalid saved node credential")
	}
	endpoint, err := url.Parse(config.Credential.ServerURL)
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") {
		return errors.New("invalid saved hub URL")
	}
	return nil
}

func loadNodeConfig(data string) (nodeConfig, string, error) {
	root, err := existingDataDir(data)
	if err != nil {
		return nodeConfig{}, "", err
	}
	capability, err := os.OpenRoot(root)
	if err != nil {
		return nodeConfig{}, root, fmt.Errorf("open node data directory: %w", err)
	}
	defer capability.Close()
	before, err := capability.Lstat("node.json")
	if err != nil {
		return nodeConfig{}, root, err
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return nodeConfig{}, root, errors.New("node configuration must be a regular file without a symbolic link")
	}
	if before.Mode().Perm()&0077 != 0 {
		return nodeConfig{}, root, errors.New("node configuration must have private permissions (0600)")
	}
	file, err := capability.Open("node.json")
	if err != nil {
		return nodeConfig{}, root, fmt.Errorf("open node configuration: %w", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nodeConfig{}, root, err
	}
	after, err := capability.Lstat("node.json")
	if err != nil || !os.SameFile(before, opened) || !os.SameFile(opened, after) || !after.Mode().IsRegular() {
		return nodeConfig{}, root, errors.New("node configuration changed while opening")
	}
	content, err := io.ReadAll(io.LimitReader(file, maxNodeConfigBytes+1))
	if err != nil {
		return nodeConfig{}, root, fmt.Errorf("read node configuration: %w", err)
	}
	if len(content) > maxNodeConfigBytes {
		return nodeConfig{}, root, errors.New("node configuration is too large")
	}
	var config nodeConfig
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		// Syntax/type errors may include secret values. Keep the diagnostic
		// generic rather than reporting the credential-bearing document.
		return nodeConfig{}, root, errors.New("could not decode node configuration")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nodeConfig{}, root, errors.New("node configuration contains extra data")
	}
	if err := validateNodeConfig(config); err != nil {
		return nodeConfig{}, root, err
	}
	return config, root, nil
}

func saveNodeConfig(root string, config nodeConfig) error {
	if err := validateNodeConfig(config); err != nil {
		return err
	}
	if err := pairingDestination(root, true); err != nil {
		return err
	}
	content, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return errors.New("could not encode node configuration")
	}
	capability, err := os.OpenRoot(root)
	if err != nil {
		return fmt.Errorf("open node data directory: %w", err)
	}
	defer capability.Close()
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return fmt.Errorf("generate node configuration file name: %w", err)
	}
	tmp := ".node-config-" + hex.EncodeToString(random[:])
	file, err := capability.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("create private node configuration: %w", err)
	}
	defer capability.Remove(tmp)
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(append(content, '\n'))
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return errors.Join(err, closeErr)
	}
	if err := pairingDestination(root, true); err != nil {
		return err
	}
	if err := capability.Rename(tmp, "node.json"); err != nil {
		return fmt.Errorf("save node configuration atomically: %w", err)
	}
	directory, err := capability.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
