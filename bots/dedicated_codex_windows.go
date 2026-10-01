//go:build windows

package bots

import (
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"syscall"
)

func codexAppServerEndpoint(string) (string, string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", "", fmt.Errorf("dedicated Codex loopback endpoint: %w", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		return "", "", err
	}
	return "", "ws://" + address, nil
}

func configureCodexAppServerProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

func signalCodexAppServerProcess(cmd *exec.Cmd, _ bool) error {
	// taskkill's /T is restricted to the child PID we started and its descendants.
	if err := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run(); err != nil {
		return fmt.Errorf("stop own Codex process tree: %w", err)
	}
	return nil
}
