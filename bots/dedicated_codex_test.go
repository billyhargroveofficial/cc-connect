//go:build !windows

package bots

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type dedicatedCodexCapture struct {
	PID       int      `json:"pid"`
	ChildPID  int      `json:"childPid,omitempty"`
	Args      []string `json:"args"`
	CodexHome string   `json:"codexHome"`
	Methods   []string `json:"methods"`
}

func TestDedicatedCodexHelper(t *testing.T) {
	mode := os.Getenv("CONNECT_BOTS_TEST_CODEX_HELPER")
	if mode == "" {
		return
	}
	capturePath := os.Getenv("CONNECT_BOTS_TEST_CODEX_CAPTURE")
	capture := dedicatedCodexCapture{PID: os.Getpid(), Args: os.Args, CodexHome: os.Getenv("CODEX_HOME")}
	var captureMu sync.Mutex
	writeCapture := func() {
		data, _ := json.Marshal(capture)
		if err := os.WriteFile(capturePath+".tmp", data, 0600); err == nil {
			_ = os.Rename(capturePath+".tmp", capturePath)
		}
	}
	writeCapture()
	if mode == "exit" {
		os.Exit(17)
	}
	if mode == "hang" {
		for {
			time.Sleep(time.Hour)
		}
	}
	if mode == "ignore-term" {
		signal.Ignore(syscall.SIGTERM)
		child := exec.Command(os.Args[0], "-test.run=^TestDedicatedCodexHelperChild$")
		if err := child.Start(); err != nil {
			os.Exit(18)
		}
		capture.ChildPID = child.Process.Pid
		writeCapture()
		go func() { _ = child.Wait() }()
	}
	endpoint := ""
	for i, arg := range os.Args {
		if arg == "--listen" && i+1 < len(os.Args) {
			endpoint = os.Args[i+1]
		}
	}
	listener, err := net.Listen("unix", strings.TrimPrefix(endpoint, "unix://"))
	if err != nil {
		os.Exit(19)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			var request struct {
				ID     any    `json:"id"`
				Method string `json:"method"`
			}
			if err := conn.ReadJSON(&request); err != nil {
				return
			}
			captureMu.Lock()
			capture.Methods = append(capture.Methods, request.Method)
			writeCapture()
			captureMu.Unlock()
			if request.Method == "initialize" {
				if mode == "delayed-initialize" {
					time.Sleep(750 * time.Millisecond)
				}
				if err := conn.WriteJSON(map[string]any{"id": request.ID, "result": map[string]any{"userAgent": "fake-codex"}}); err != nil {
					return
				}
			}
		}
	})}
	_ = server.Serve(listener)
	os.Exit(0)
}

func TestDedicatedCodexHelperChild(t *testing.T) {
	if os.Getenv("CONNECT_BOTS_TEST_CODEX_HELPER") == "" {
		return
	}
	signal.Ignore(syscall.SIGTERM)
	for {
		time.Sleep(time.Hour)
	}
}

func dedicatedCodexFakeCommand(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	command := filepath.Join(root, "codex")
	// Shell quoting is explicit: the generated command only execs this test
	// binary, with all CLI arguments passed through as separate argv entries.
	quotedBinary := "'" + strings.ReplaceAll(os.Args[0], "'", "'\\''") + "'"
	data := "#!/bin/sh\nexec " + quotedBinary + " -test.run='^TestDedicatedCodexHelper$' -- \"$@\"\n"
	if err := os.WriteFile(command, []byte(data), 0700); err != nil {
		t.Fatal(err)
	}
	return command
}

func readDedicatedCodexCapture(t *testing.T, path string, methods int) dedicatedCodexCapture {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		var capture dedicatedCodexCapture
		if err == nil && json.Unmarshal(data, &capture) == nil && capture.PID > 0 && len(capture.Methods) >= methods {
			return capture
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no helper capture with %d methods at %s", methods, path)
	return dedicatedCodexCapture{}
}

func dedicatedCodexProcessAlive(pid int) bool {
	if pid <= 0 || syscall.Kill(pid, 0) != nil {
		return false
	}
	// An adopted zombie has exited even if the OS has not reaped it yet.
	if stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		if tail := strings.LastIndex(string(stat), ") "); tail >= 0 && strings.HasPrefix(string(stat)[tail+2:], "Z ") {
			return false
		}
	}
	return true
}

func waitDedicatedCodexProcessExited(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if !dedicatedCodexProcessAlive(pid) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("own helper process %d still alive", pid)
}

func TestDedicatedCodexUsesPrivateStateAndOnlyInitializesReadiness(t *testing.T) {
	command := dedicatedCodexFakeCommand(t)
	data, home := t.TempDir(), t.TempDir()
	instructions := filepath.Join(home, "AGENTS.md")
	if err := os.WriteFile(instructions, []byte("Native global instructions."), 0600); err != nil {
		t.Fatal(err)
	}
	capturePath := filepath.Join(t.TempDir(), "capture.json")
	t.Setenv("CONNECT_BOTS_TEST_CODEX_HELPER", "ready")
	t.Setenv("CONNECT_BOTS_TEST_CODEX_CAPTURE", capturePath)
	t.Setenv("CODEX_HOME", home)
	server, err := StartCodexAppServer(context.Background(), CodexAppServerConfig{DataDir: data, Command: command, StartupTimeout: time.Second, ShutdownTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	capture := readDedicatedCodexCapture(t, capturePath, 2)
	if capture.CodexHome != home || strings.Contains(server.URL(), home) {
		t.Fatalf("native auth home or private socket lost: %#v URL=%q", capture, server.URL())
	}
	args := strings.Join(capture.Args, " ")
	if !strings.Contains(args, "app-server --listen "+server.URL()) || !strings.Contains(args, "sqlite_home="+strconvQuote(filepath.Join(data, "codex", "state"))) || strings.Contains(args, " daemon ") {
		t.Fatalf("dedicated CLI arguments incorrect: %v", capture.Args)
	}
	if strings.Join(capture.Methods, ",") != "initialize,initialized" {
		t.Fatalf("readiness created work: %v", capture.Methods)
	}
	global, _ := os.ReadFile(instructions)
	if string(global) != "Native global instructions." {
		t.Fatal("startup rewrote native global instructions")
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	waitDedicatedCodexProcessExited(t, capture.PID)
	if _, err := os.Stat(strings.TrimPrefix(server.URL(), "unix://")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned socket remains after Close: %v", err)
	}
	if err := server.Close(); err != nil {
		t.Fatalf("Close is not idempotent: %v", err)
	}
}

func strconvQuote(value string) string { return fmt.Sprintf("%q", value) }

func TestDedicatedCodexReadinessWaitsForWarmingServer(t *testing.T) {
	// A handshake can finish before a warming app-server is ready to answer
	// initialize. Replacing that connection every 500ms can prevent readiness.
	t.Setenv("CONNECT_BOTS_TEST_CODEX_HELPER", "delayed-initialize")
	capturePath := filepath.Join(t.TempDir(), "capture.json")
	t.Setenv("CONNECT_BOTS_TEST_CODEX_CAPTURE", capturePath)
	server, err := StartCodexAppServer(context.Background(), CodexAppServerConfig{
		DataDir: t.TempDir(), Command: dedicatedCodexFakeCommand(t), StartupTimeout: 2 * time.Second, ShutdownTimeout: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	capture := readDedicatedCodexCapture(t, capturePath, 2)
	if strings.Join(capture.Methods, ",") != "initialize,initialized" {
		t.Fatalf("warm startup replaced its pending connection: %v", capture.Methods)
	}
}

func TestDedicatedCodexCloseDoesNotStopAnotherAppServer(t *testing.T) {
	t.Setenv("CONNECT_BOTS_TEST_CODEX_HELPER", "ready")
	command := dedicatedCodexFakeCommand(t)
	start := func() (*CodexAppServer, dedicatedCodexCapture) {
		capturePath := filepath.Join(t.TempDir(), "capture.json")
		t.Setenv("CONNECT_BOTS_TEST_CODEX_CAPTURE", capturePath)
		server, err := StartCodexAppServer(context.Background(), CodexAppServerConfig{DataDir: t.TempDir(), Command: command, StartupTimeout: time.Second, ShutdownTimeout: 100 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = server.Close() })
		return server, readDedicatedCodexCapture(t, capturePath, 2)
	}
	other, otherCapture := start()
	ours, ourCapture := start()
	if err := ours.Close(); err != nil {
		t.Fatal(err)
	}
	waitDedicatedCodexProcessExited(t, ourCapture.PID)
	if !dedicatedCodexProcessAlive(otherCapture.PID) {
		t.Fatal("Close stopped another app-server process")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := probeCodexAppServer(ctx, other.URL()); err != nil {
		t.Fatalf("another app-server socket was damaged: %v", err)
	}
}

func TestDedicatedCodexStartupFailureNeverFallsBack(t *testing.T) {
	for _, mode := range []string{"exit", "hang"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CONNECT_BOTS_TEST_CODEX_HELPER", mode)
			capturePath := filepath.Join(t.TempDir(), "capture.json")
			t.Setenv("CONNECT_BOTS_TEST_CODEX_CAPTURE", capturePath)
			started := time.Now()
			server, err := StartCodexAppServer(context.Background(), CodexAppServerConfig{
				DataDir: t.TempDir(), Command: dedicatedCodexFakeCommand(t), StartupTimeout: 200 * time.Millisecond, ShutdownTimeout: 100 * time.Millisecond,
			})
			if err == nil || server != nil || time.Since(started) > 2*time.Second {
				t.Fatalf("startup failure was not bounded: server=%v error=%v elapsed=%v", server, err, time.Since(started))
			}
			capture := readDedicatedCodexCapture(t, capturePath, 0)
			waitDedicatedCodexProcessExited(t, capture.PID)
		})
	}
}

func TestDedicatedCodexContextCancellationForcesOnlyOwnedProcessGroup(t *testing.T) {
	t.Setenv("CONNECT_BOTS_TEST_CODEX_HELPER", "ignore-term")
	capturePath := filepath.Join(t.TempDir(), "capture.json")
	t.Setenv("CONNECT_BOTS_TEST_CODEX_CAPTURE", capturePath)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server, err := StartCodexAppServer(ctx, CodexAppServerConfig{DataDir: t.TempDir(), Command: dedicatedCodexFakeCommand(t), StartupTimeout: time.Second, ShutdownTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	capture := readDedicatedCodexCapture(t, capturePath, 2)
	cancel()
	select {
	case <-server.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled helper did not exit after bounded force stop")
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	waitDedicatedCodexProcessExited(t, capture.PID)
	waitDedicatedCodexProcessExited(t, capture.ChildPID)
}

func TestValidateCodexAppServerURLRejectsImplicitSharedEndpoints(t *testing.T) {
	for _, endpoint := range []string{"", "managed", "managed://", "stdio://", "unix://", "unix://relative.sock", "off", "ws://", "https://localhost:9999", "ws://user:password@localhost:9999"} {
		if got, err := ValidateCodexAppServerURL(endpoint); err == nil {
			t.Errorf("unsafe endpoint %q accepted as %q", endpoint, got)
		}
	}
	for _, endpoint := range []string{"unix:///tmp/connect-bots.sock", "ws://127.0.0.1:1234", "wss://codex.example.invalid/"} {
		if got, err := ValidateCodexAppServerURL(endpoint); err != nil || got != endpoint {
			t.Errorf("explicit endpoint %q rejected: %q %v", endpoint, got, err)
		}
	}
}
