package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/bots"
)

type fakeCodexProcess struct {
	endpoint string
	done     chan struct{}
	exitErr  error
	close    func() error
}

func (process *fakeCodexProcess) URL() string           { return process.endpoint }
func (process *fakeCodexProcess) Done() <-chan struct{} { return process.done }
func (process *fakeCodexProcess) Err() error            { return process.exitErr }
func (process *fakeCodexProcess) Close() error {
	if process.close != nil {
		return process.close()
	}
	return nil
}

func pairedNodeRoot(t *testing.T) string {
	t.Helper()
	root, err := privateDataDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := saveNodeConfig(root, testNodeConfig()); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestNodeRunUsesOwnWorkspaceNativeSkillsAndLoopbackToolListener(t *testing.T) {
	root := pairedNodeRoot(t)
	nativeHome := t.TempDir()
	codexHome := filepath.Join(nativeHome, "native-codex")
	t.Setenv("HOME", nativeHome)
	for _, skillDir := range []string{filepath.Join(nativeHome, ".agents", "skills", "shared-skill"), filepath.Join(codexHome, "skills", "codex-skill")} {
		if err := os.MkdirAll(skillDir, 0700); err != nil {
			t.Fatal(err)
		}
		content := "---\nname: " + filepath.Base(skillDir) + "\ndescription: Native node skill\n---\nRun on this machine.\n"
		if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	deps := testDependencies(root)
	var runtime *bots.Runtime
	var runtimeConfig bots.RuntimeConfig
	var store *bots.Store
	var startupContext context.Context
	codexClosed := false
	deps.startCodex = func(ctx context.Context, config bots.CodexAppServerConfig) (codexProcess, error) {
		startupContext = ctx
		if config.DataDir != root || config.CodexHome != codexHome || config.Command != "/local/bin/native codex" {
			t.Fatal("Codex was not started with node-local overrides")
		}
		return &fakeCodexProcess{endpoint: "unix:///private/node-codex.sock", done: make(chan struct{}), close: func() error {
			if runtime == nil {
				t.Fatal("workspace runtime was never created")
			}
			if _, err := runtime.SendMessage(context.Background(), store.ListBots()[0].ID, bots.MessageRequest{Text: "probe closed runtime"}); !errors.Is(err, os.ErrClosed) {
				t.Fatalf("Codex stopped before Runtime.Close: %v", err)
			}
			if startupContext.Err() != nil {
				t.Fatal("Codex lifetime was canceled before adapters closed")
			}
			codexClosed = true
			return nil
		}}, nil
	}
	deps.newRuntime = func(s *bots.Store, cfg bots.RuntimeConfig) *bots.Runtime {
		store, runtimeConfig = s, cfg
		runtime = bots.NewRuntime(s, cfg)
		return runtime
	}
	var botID string
	var internalURL string
	deps.runClient = func(ctx context.Context, config bots.NodeClientConfig, handler http.Handler) error {
		if config.Credential != testNodeConfig().Credential || config.Metadata.Hostname != "MacBook" {
			t.Fatal("tunnel used a different node identity")
		}
		list := httptest.NewRecorder()
		handler.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/studio/bots", nil))
		if list.Code != http.StatusOK {
			t.Fatal("node did not expose its workspace API", list.Code)
		}
		var response struct {
			Bots []bots.Bot `json:"bots"`
		}
		if err := json.Unmarshal(list.Body.Bytes(), &response); err != nil || len(response.Bots) != 1 {
			t.Fatal("node did not have its own fresh Coordinator", err)
		}
		botID = response.Bots[0].ID
		if !strings.HasPrefix(response.Bots[0].WorkDir, root+string(filepath.Separator)) {
			t.Fatal("bot files were not local to this node")
		}
		profile := httptest.NewRecorder()
		handler.ServeHTTP(profile, httptest.NewRequest(http.MethodPatch, "/api/studio/bots/"+botID, strings.NewReader(`{"name":"Mac Coordinator"}`)))
		if profile.Code != http.StatusOK {
			t.Fatal("node profile edit failed", profile.Code)
		}
		skills := httptest.NewRecorder()
		handler.ServeHTTP(skills, httptest.NewRequest(http.MethodGet, "/api/studio/user/skills", nil))
		if skills.Code != http.StatusOK || !strings.Contains(skills.Body.String(), "shared-skill") || !strings.Contains(skills.Body.String(), "codex-skill") {
			t.Fatal("node lost its native local skills", skills.Code, skills.Body.String())
		}
		for _, path := range []string{"/api/studio/login", "/api/studio/register", "/api/studio/internal/tools"} {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, nil))
			if recorder.Code != http.StatusNotFound {
				t.Fatal("tunnel handler exposed non-workspace routes", path, recorder.Code)
			}
		}
		internalURL = runtimeConfig.InternalURL
		parsed, err := url.Parse(internalURL)
		if err != nil {
			t.Fatal(err)
		}
		ip := net.ParseIP(parsed.Hostname())
		if ip == nil || !ip.IsLoopback() || parsed.Port() == "" || runtimeConfig.InternalToken == "" {
			t.Fatal("local tool listener is not loopback and separately credentialed")
		}
		client := &http.Client{Timeout: time.Second}
		for _, tt := range []struct {
			path, method string
			status       int
		}{{"/api/studio/bots", http.MethodGet, http.StatusNotFound}, {"/api/studio/internal/tools", http.MethodPost, http.StatusUnauthorized}} {
			request, err := http.NewRequest(tt.method, internalURL+tt.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != tt.status {
				t.Fatal("loopback listener exposed the wrong surface", tt.path, response.StatusCode)
			}
		}
		return nil
	}
	options := nodeRunOptions{Root: root, CodexCommand: "/local/bin/native codex", CodexHome: codexHome}
	if err := runNode(context.Background(), testNodeConfig(), options, deps); err != nil {
		t.Fatal(err)
	}
	if !codexClosed || startupContext.Err() == nil {
		t.Fatal("dedicated Codex was not closed and its context released")
	}
	wantOptions := map[string]any{"app_server_url": "unix:///private/node-codex.sock", "codex_home": codexHome, "cmd": []string{options.CodexCommand}}
	if !reflect.DeepEqual(runtimeConfig.AgentOptions["codex"], wantOptions) || runtimeConfig.AgentOptions["pi"] != nil || runtimeConfig.VoiceAvailable {
		t.Fatal("node adapters lost native environment or advertised unavailable voice")
	}
	client := &http.Client{Timeout: time.Second}
	if response, err := client.Get(internalURL + "/api/studio/bots"); err == nil {
		response.Body.Close()
		t.Fatal("local tool listener remained after shutdown")
	}
	reopened, err := bots.OpenStore(root)
	if err != nil {
		t.Fatal("workspace lock was not released", err)
	}
	defer reopened.Close()
	bot, err := reopened.GetBot(botID)
	if err != nil || bot.Name != "Mac Coordinator" {
		t.Fatal("node workspace did not persist edits", err)
	}
}

func TestNodeRunStopsClientAndCodexWhenCanceled(t *testing.T) {
	root := pairedNodeRoot(t)
	deps := testDependencies(root)
	started := make(chan struct{})
	clientStopped := make(chan struct{})
	codexClosed := false
	deps.startCodex = func(context.Context, bots.CodexAppServerConfig) (codexProcess, error) {
		return &fakeCodexProcess{endpoint: "unix:///private/node-codex.sock", done: make(chan struct{}), close: func() error { codexClosed = true; return nil }}, nil
	}
	deps.runClient = func(ctx context.Context, _ bots.NodeClientConfig, _ http.Handler) error {
		close(started)
		<-ctx.Done()
		close(clientStopped)
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- runNode(ctx, testNodeConfig(), nodeRunOptions{Root: root, CodexCommand: "codex"}, deps)
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("node client did not start")
	}
	cancel()
	select {
	case err := <-result:
		if err != nil || !codexClosed {
			t.Fatal("graceful shutdown failed", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("node did not stop on cancellation")
	}
	select {
	case <-clientStopped:
	default:
		t.Fatal("node left its tunnel goroutine running")
	}
}

func TestNodeRunStopsOnDedicatedCodexExitAndReleasesLock(t *testing.T) {
	root := pairedNodeRoot(t)
	deps := testDependencies(root)
	done := make(chan struct{})
	deps.startCodex = func(context.Context, bots.CodexAppServerConfig) (codexProcess, error) {
		return &fakeCodexProcess{endpoint: "unix:///private/node-codex.sock", done: done, exitErr: errors.New("owned Codex crashed")}, nil
	}
	deps.runClient = func(ctx context.Context, _ bots.NodeClientConfig, _ http.Handler) error {
		close(done)
		<-ctx.Done()
		return ctx.Err()
	}
	if err := runNode(context.Background(), testNodeConfig(), nodeRunOptions{Root: root, CodexCommand: "codex"}, deps); err == nil || !strings.Contains(err.Error(), "owned Codex crashed") {
		t.Fatal("unexpected Codex exit was not reported", err)
	}
	lock, err := acquireNodeLock(root)
	if err != nil {
		t.Fatal("workspace lock remained after Codex failure", err)
	}
	lock.Close()
}

func TestNodeRunClosesWorkspaceIfCodexStartupFails(t *testing.T) {
	root := pairedNodeRoot(t)
	deps := testDependencies(root)
	var childContext context.Context
	deps.startCodex = func(ctx context.Context, cfg bots.CodexAppServerConfig) (codexProcess, error) {
		childContext = ctx
		return nil, errors.New("fake startup error")
	}
	if err := runNode(context.Background(), testNodeConfig(), nodeRunOptions{Root: root, CodexCommand: "codex"}, deps); err == nil || !strings.Contains(err.Error(), "fake startup error") {
		t.Fatal("Codex startup error was not returned", err)
	}
	if childContext == nil || childContext.Err() == nil {
		t.Fatal("failed child context was not released")
	}
	store, err := bots.OpenStore(root)
	if err != nil {
		t.Fatal("failed startup left the store open", err)
	}
	store.Close()
}

func TestNodeRunCancellationInterruptsWarmingCodexAndReleasesStore(t *testing.T) {
	root := pairedNodeRoot(t)
	deps := testDependencies(root)
	warming := make(chan struct{})
	deps.startCodex = func(ctx context.Context, cfg bots.CodexAppServerConfig) (codexProcess, error) {
		close(warming)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- runNode(ctx, testNodeConfig(), nodeRunOptions{Root: root, CodexCommand: "codex"}, deps)
	}()
	select {
	case <-warming:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("fake Codex did not begin warming")
	}
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal("canceled startup should stop quietly", err)
		}
	case <-time.After(time.Second):
		t.Fatal("warming Codex ignored cancellation")
	}
	store, err := bots.OpenStore(root)
	if err != nil {
		t.Fatal("canceled startup left the store open", err)
	}
	store.Close()
}

func TestNodeCLIAllowInsecureOverrideAndRevocationPreserveSavedPairing(t *testing.T) {
	root := pairedNodeRoot(t)
	config := testNodeConfig()
	config.AllowInsecure = true
	if err := saveNodeConfig(root, config); err != nil {
		t.Fatal(err)
	}
	deps := testDependencies(root)
	deps.startCodex = func(context.Context, bots.CodexAppServerConfig) (codexProcess, error) {
		return &fakeCodexProcess{endpoint: "unix:///private/node-codex.sock", done: make(chan struct{})}, nil
	}
	for _, args := range [][]string{{"run"}, {"run", "--allow-insecure=false"}} {
		deps.runClient = func(ctx context.Context, got bots.NodeClientConfig, handler http.Handler) error {
			want := len(args) == 1
			if got.AllowInsecure != want {
				t.Fatal("run did not use saved/explicit insecure choice")
			}
			return bots.ErrNodeRevoked
		}
		err := execute(context.Background(), args, strings.NewReader(""), io.Discard, io.Discard, deps)
		if !errors.Is(err, bots.ErrNodeRevoked) || strings.Contains(err.Error(), config.Credential.Token) {
			t.Fatal("revocation error lost identity or leaked the credential", err)
		}
		got, _, loadErr := loadNodeConfig(root)
		if loadErr != nil || got != config {
			t.Fatal("revocation modified pairing without explicit user intent", loadErr)
		}
	}
}
