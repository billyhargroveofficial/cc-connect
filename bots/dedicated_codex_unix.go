//go:build !windows

package bots

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

func codexAppServerEndpoint(dataDir string) (string, string, error) {
	runDir := filepath.Join(dataDir, "run")
	if err := os.MkdirAll(runDir, 0700); err != nil {
		return "", "", fmt.Errorf("dedicated Codex runtime directory: %w", err)
	}
	// Unix socket limits vary by platform; leave room for the random suffix.
	if len(filepath.Join(runDir, "codex-1234567890", "server.sock")) > 100 {
		runDir = os.TempDir()
	}
	runtimeDir, err := os.MkdirTemp(runDir, "codex-")
	if err != nil {
		return "", "", fmt.Errorf("dedicated Codex socket directory: %w", err)
	}
	return runtimeDir, "unix://" + filepath.Join(runtimeDir, "server.sock"), nil
}

func configureCodexAppServerProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func signalCodexAppServerProcess(cmd *exec.Cmd, force bool) error {
	signal := syscall.SIGTERM
	if force {
		signal = syscall.SIGKILL
	}
	if err := syscall.Kill(-cmd.Process.Pid, signal); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("stop own Codex process group: %w", err)
	}
	return nil
}
