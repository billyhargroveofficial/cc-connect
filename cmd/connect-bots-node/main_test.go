package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/bots"
)

func testDependencies(data string) nodeDependencies {
	return nodeDependencies{
		defaultDir: func() (string, error) { return data, nil },
		metadata: func() bots.NodeMetadata {
			return bots.NodeMetadata{Hostname: "MacBook", OS: "darwin", Arch: "arm64", Version: "test"}
		},
		pair: func(context.Context, string, string, bots.NodeMetadata, bool) (bots.NodeCredential, error) {
			return testNodeConfig().Credential, nil
		},
		startCodex: func(context.Context, bots.CodexAppServerConfig) (codexProcess, error) {
			return nil, errors.New("unexpected Codex start")
		},
		runClient: func(context.Context, bots.NodeClientConfig, http.Handler) error {
			return errors.New("unexpected tunnel start")
		},
		newRuntime: func(store *bots.Store, cfg bots.RuntimeConfig) *bots.Runtime {
			return bots.NewRuntime(store, cfg)
		},
	}
}

func TestNodeCLIHelpAndVersionNeverStartServices(t *testing.T) {
	deps := testDependencies(filepath.Join(t.TempDir(), "unused"))
	deps.defaultDir = func() (string, error) { t.Fatal("help attempted to open user state"); return "", nil }
	for _, args := range [][]string{nil, {"help"}, {"--help"}, {"--version"}, {"version"}} {
		var out bytes.Buffer
		if err := execute(context.Background(), args, strings.NewReader(""), &out, io.Discard, deps); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if !strings.Contains(out.String(), "Connect Bots Node") {
			t.Fatalf("%v: missing help/version output", args)
		}
	}
}

func TestNodeCLIPairReadsCodeFromStdinAndStatusOmitsSecrets(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	deps := testDependencies(root)
	code := "one-time-enrollment-secret"
	pairCalls := 0
	deps.pair = func(ctx context.Context, server, received string, metadata bots.NodeMetadata, insecure bool) (bots.NodeCredential, error) {
		pairCalls++
		if server != "https://bots.example.test" || received != code || insecure || metadata.OS != "darwin" || metadata.Arch != "arm64" {
			t.Fatal("incorrect pairing request")
		}
		return testNodeConfig().Credential, nil
	}
	var output, prompts bytes.Buffer
	if err := execute(context.Background(), []string{"pair", "--server", "https://bots.example.test"}, strings.NewReader(code+"\n"), &output, &prompts, deps); err != nil {
		t.Fatal(err)
	}
	if pairCalls != 1 || !strings.Contains(prompts.String(), "Pairing code:") {
		t.Fatal("pair did not prompt and exchange once")
	}
	if err := execute(context.Background(), []string{"status"}, strings.NewReader(""), &output, &prompts, deps); err != nil {
		t.Fatal(err)
	}
	combined := output.String() + prompts.String()
	if strings.Contains(combined, code) || strings.Contains(combined, testNodeConfig().Credential.Token) {
		t.Fatal("pair/status revealed a credential")
	}
	if !strings.Contains(output.String(), "https://bots.example.test") || !strings.Contains(output.String(), "Connection status: see Hosts") {
		t.Fatal("status should identify saved pairing without claiming online state")
	}
}

func TestNodeCLIPairDoesNotOverwriteByDefaultAndWarnsOnReplace(t *testing.T) {
	root, err := privateDataDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	initial := testNodeConfig()
	if err := saveNodeConfig(root, initial); err != nil {
		t.Fatal(err)
	}
	deps := testDependencies(root)
	pairCalls := 0
	var warning bytes.Buffer
	deps.pair = func(context.Context, string, string, bots.NodeMetadata, bool) (bots.NodeCredential, error) {
		pairCalls++
		if !strings.Contains(warning.String(), "become visible to the account") {
			t.Fatal("replacement warning must precede pairing")
		}
		credential := initial.Credential
		credential.NodeID = "node_" + strings.Repeat("b", 32)
		return credential, nil
	}
	args := []string{"pair", "--server", "https://bots.example.test", "--code", "new-enrollment"}
	if err := execute(context.Background(), args, strings.NewReader(""), io.Discard, &warning, deps); err == nil || pairCalls != 0 {
		t.Fatal("pair overwrote saved credential without --replace")
	}
	config, _, err := loadNodeConfig(root)
	if err != nil || config != initial {
		t.Fatal("pair rejected but changed the original credential", err)
	}
	if err := execute(context.Background(), append(args, "--replace"), strings.NewReader(""), io.Discard, &warning, deps); err != nil || pairCalls != 1 {
		t.Fatal("explicit replacement failed", err)
	}
}

func TestNodeCLIRejectsArgumentsWithoutLoggingSecrets(t *testing.T) {
	secret := "SECRET_FLAG_VALUE"
	deps := testDependencies(t.TempDir())
	for _, args := range [][]string{
		{"unknown-command"}, {"run", secret}, {"pair", "--allow-insecure=" + secret}, {"pair", "--unknown=" + secret}, {"status", secret}, {"version", secret}, {"help", secret},
	} {
		var out, diagnostics bytes.Buffer
		err := execute(context.Background(), args, strings.NewReader(""), &out, &diagnostics, deps)
		if err == nil || strings.Contains(err.Error()+out.String()+diagnostics.String(), secret) {
			t.Fatalf("unsafe option diagnostic: %v", err)
		}
	}
	deps.pair = func(context.Context, string, string, bots.NodeMetadata, bool) (bots.NodeCredential, error) {
		return bots.NodeCredential{}, fmt.Errorf("remote included %s", secret)
	}
	err := execute(context.Background(), []string{"pair", "--server", "https://bots.example.test", "--code", secret}, strings.NewReader(""), io.Discard, io.Discard, deps)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatal("pair transport error exposed the code")
	}
}

func TestNodeCLIPairCancellationInterruptsStdinRead(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	deps := testDependencies(root)
	reader, writer := io.Pipe()
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- execute(ctx, []string{"pair", "--server", "https://bots.example.test"}, reader, io.Discard, io.Discard, deps)
	}()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("pair did not report cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("pair remained blocked on stdin after cancellation")
	}
	if _, err := os.Stat(filepath.Join(root, "node.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("canceled pair saved a credential")
	}
}

func TestNodeCLIStatusAndRunOfUnpairedNodeDoNotStartCodex(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing")
	deps := testDependencies(root)
	var out bytes.Buffer
	if err := execute(context.Background(), []string{"status"}, strings.NewReader(""), &out, io.Discard, deps); err != nil || !strings.Contains(out.String(), "Not paired") {
		t.Fatal("missing node status", err)
	}
	if err := execute(context.Background(), []string{"run"}, strings.NewReader(""), io.Discard, io.Discard, deps); err == nil {
		t.Fatal("unpaired run was accepted")
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unpaired read created a workspace")
	}
}

func TestNodeCLICommandHelpDoesNotPairOrOpenCodex(t *testing.T) {
	deps := testDependencies(filepath.Join(t.TempDir(), "unused"))
	deps.pair = func(context.Context, string, string, bots.NodeMetadata, bool) (bots.NodeCredential, error) {
		t.Fatal("command help attempted pairing")
		return bots.NodeCredential{}, nil
	}
	for _, command := range []string{"pair", "run", "status"} {
		var out bytes.Buffer
		if err := execute(context.Background(), []string{command, "--help"}, strings.NewReader(""), io.Discard, &out, deps); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "Usage: connect-bots-node "+command) {
			t.Fatal("command help was not printed")
		}
	}
}
