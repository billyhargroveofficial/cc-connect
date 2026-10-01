//go:build !windows

package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/bots"
	"github.com/gorilla/websocket"
)

func TestConnectBotsStartupCodexHelper(t *testing.T) {
	mode := os.Getenv("CONNECT_BOTS_TEST_STARTUP_HELPER")
	if mode == "" {
		return
	}
	if err := os.WriteFile(os.Getenv("CONNECT_BOTS_TEST_STARTUP_CAPTURE"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		os.Exit(17)
	}
	if mode == "warming" {
		for {
			time.Sleep(time.Hour)
		}
	}
	endpoint := ""
	for i, arg := range os.Args {
		if arg == "--listen" && i+1 < len(os.Args) {
			endpoint = os.Args[i+1]
		}
	}
	listener, err := net.Listen("unix", strings.TrimPrefix(endpoint, "unix://"))
	if err != nil {
		os.Exit(18)
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
			if conn.ReadJSON(&request) != nil {
				return
			}
			if request.Method == "initialize" {
				if conn.WriteJSON(map[string]any{"id": request.ID, "result": map[string]any{"userAgent": "startup-test"}}) != nil {
					return
				}
			}
		}
	})}
	_ = server.Serve(listener)
	os.Exit(0)
}

func startupCodexConfig(t *testing.T, mode string) (bots.CodexAppServerConfig, string) {
	t.Helper()
	capture := filepath.Join(t.TempDir(), "pid")
	t.Setenv("CONNECT_BOTS_TEST_STARTUP_HELPER", mode)
	t.Setenv("CONNECT_BOTS_TEST_STARTUP_CAPTURE", capture)
	command := filepath.Join(t.TempDir(), "codex")
	quotedBinary := "'" + strings.ReplaceAll(os.Args[0], "'", "'\\''") + "'"
	script := "#!/bin/sh\nexec " + quotedBinary + " -test.run='^TestConnectBotsStartupCodexHelper$' -- \"$@\"\n"
	if err := os.WriteFile(command, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return bots.CodexAppServerConfig{DataDir: t.TempDir(), Command: command, StartupTimeout: 5 * time.Second, ShutdownTimeout: 100 * time.Millisecond}, capture
}

func TestOwnedCodexStartupCancellationStopsWarmingChild(t *testing.T) {
	config, capture := startupCodexConfig(t, "warming")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		server, stop, err := startOwnedCodexAppServer(ctx, config)
		if server != nil {
			_ = server.Close()
		}
		if stop != nil {
			stop()
		}
		result <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(capture); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("warming child never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("startup cancellation returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("HTTP signal cancellation did not stop the warming child before readiness timeout")
	}
	entries, err := os.ReadDir(filepath.Join(config.DataDir, "run"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("canceled startup retained its private runtime directory: %v %v", entries, err)
	}
}

func TestOwnedCodexReadyChildSurvivesHTTPCancellation(t *testing.T) {
	config, _ := startupCodexConfig(t, "ready")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server, stop, err := startOwnedCodexAppServer(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close(); stop() })
	cancel()
	select {
	case <-server.Done():
		t.Fatal("HTTP cancellation stopped the ready child before native session cleanup")
	case <-time.After(150 * time.Millisecond):
	}
	if _, err := os.Stat(strings.TrimPrefix(server.URL(), "unix://")); err != nil {
		t.Fatalf("ready endpoint vanished before explicit cleanup: %v", err)
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-server.Done():
	case <-time.After(time.Second):
		t.Fatal("explicit owned-child shutdown did not finish")
	}
}
