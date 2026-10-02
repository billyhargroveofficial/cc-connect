package main

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runHostFlags(t *testing.T, args ...string) error {
	t.Helper()
	previousArgs, previousFlags := os.Args, flag.CommandLine
	t.Cleanup(func() { os.Args, flag.CommandLine = previousArgs, previousFlags })
	os.Args = append([]string{"connect-bots"}, args...)
	flag.CommandLine = flag.NewFlagSet("connect-bots", flag.ContinueOnError)
	flag.CommandLine.SetOutput(io.Discard)
	return run()
}

func TestHostNodeFlagsRejectUnsafeConfigurationBeforeStartingCodex(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{"plain remote origin", []string{"--public-url", "http://192.168.1.15:5173"}, "requires HTTPS"},
		{"embedded password", []string{"--public-url", "https://user:password@example.com"}, "without credentials"},
		{"origin path", []string{"--public-url", "https://example.com/nodes"}, "without credentials"},
		{"explicit LAN opt in", []string{"--public-url", "http://192.168.1.15:5173", "--allow-insecure-nodes", "--addr", "invalid"}, "addr must contain"},
		{"unknown command", []string{"node"}, "connect-bots-node executable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := filepath.Join(t.TempDir(), "untouched")
			args := append([]string{"--data", data}, test.args...)
			if err := runHostFlags(t, args...); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("run flags = %v; want %q", err, test.want)
			}
			if _, err := os.Stat(data); !os.IsNotExist(err) {
				t.Fatalf("invalid configuration started workspace initialization: %v", err)
			}
		})
	}
}

func TestHostVersionAcceptsNodeFlagsWithoutStartingServices(t *testing.T) {
	data := filepath.Join(t.TempDir(), "untouched")
	if err := runHostFlags(t, "--data", data, "--node-binaries", t.TempDir(), "--version"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(data); !os.IsNotExist(err) {
		t.Fatalf("version command initialized services: %v", err)
	}
}
