package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/chenhg5/cc-connect/bots"
)

const testAccountA = "user_11111111111111111111111111111111"
const testAccountB = "user_22222222222222222222222222222222"

func tenantTestManager(t *testing.T, root string) *TenantManager {
	t.Helper()
	manager, err := NewTenantManager(TenantManagerConfig{
		Context: context.Background(), DataRoot: root,
		CodexURL: "unix:///connect-bots-test/app-server.sock", InternalURL: "http://127.0.0.1:9830",
		FlovURL: "http://127.0.0.1:17432/v1/audio/transcriptions",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Error(err)
		}
	})
	return manager
}

func TestTenantManagerPreservesExistingOwnerWorkspace(t *testing.T) {
	root := t.TempDir()
	store, err := bots.OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	bot, err := store.CreateBot(bots.Bot{Name: "Existing assistant", Role: "Preserve this role"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(bot.ID, "turn-1", "assistant", map[string]string{"text": "Existing history"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	manager := tenantTestManager(t, root)
	owner, err := manager.Resolve(bots.Account{ID: testAccountA, Username: "owner", Owner: true})
	if err != nil || owner != manager.OwnerServer() {
		t.Fatalf("owner did not resolve to the legacy workspace: %v", err)
	}
	preserved, err := manager.owner.store.GetBot(bot.ID)
	if err != nil || preserved.Role != bot.Role || preserved.WorkDir != bot.WorkDir {
		t.Fatalf("existing bot was not preserved: %+v, %v", preserved, err)
	}
	events, err := manager.owner.store.Events(bot.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	preservedHistory := false
	for _, event := range events {
		if event.Type == "assistant" && event.TurnID == "turn-1" {
			preservedHistory = true
		}
	}
	if !preservedHistory {
		t.Fatal("existing history was not preserved")
	}
}

func TestTenantManagerAccountsHaveSeparateBotsInstructionsAndSkills(t *testing.T) {
	manager := tenantTestManager(t, t.TempDir())
	first, err := manager.Resolve(bots.Account{ID: testAccountA, Username: "same-display-name"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Resolve(bots.Account{ID: testAccountB, Username: "same-display-name"})
	if err != nil {
		t.Fatal(err)
	}
	if first == second || first == manager.OwnerServer() {
		t.Fatal("accounts shared a workspace server")
	}
	firstStore, secondStore := manager.tenants[testAccountA].store, manager.tenants[testAccountB].store
	if firstStore.Root() != filepath.Join(manager.root, "users", testAccountA) || secondStore.Root() != filepath.Join(manager.root, "users", testAccountB) {
		t.Fatal("account directory was not derived from its opaque identity")
	}
	privateBot, err := firstStore.CreateBot(bots.Bot{Name: "Private assistant"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := secondStore.GetBot(privateBot.ID); !errors.Is(err, bots.ErrNotFound) {
		t.Fatalf("another account could resolve the private bot: %v", err)
	}
	if err := first.Workspace().SetInstructions("", "First account instructions"); err != nil {
		t.Fatal(err)
	}
	content, _, err := second.Workspace().UserInstructions()
	if err != nil || content == "First account instructions" {
		t.Fatalf("account instructions were shared: %q, %v", content, err)
	}
	path := filepath.Join(firstStore.UserDir(), "skills", "private", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("---\nname: private\ndescription: Account-local skill\n---\nUse account-local files.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	firstSkills, err := first.Workspace().UserSkills()
	if err != nil || len(firstSkills) != 1 {
		t.Fatalf("first account skill not found: %+v, %v", firstSkills, err)
	}
	secondSkills, err := second.Workspace().UserSkills()
	if err != nil || len(secondSkills) != 0 {
		t.Fatalf("account-local skill leaked: %+v, %v", secondSkills, err)
	}
	for _, store := range []*bots.Store{firstStore, secondStore} {
		if _, err := os.Stat(filepath.Join(store.Root(), "extensions", "connect-bots.ts")); err != nil {
			t.Fatalf("account Pi bridge missing: %v", err)
		}
		if _, err := os.Stat(filepath.Join(store.Root(), "token")); !os.IsNotExist(err) {
			t.Fatal("account tenant unexpectedly created a legacy login token")
		}
	}
}

func TestTenantManagerNativeSkillsRemainOwnerOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".agents", "skills", "host-only", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("---\nname: host-only\ndescription: Host owner skill\n---\nPrivate host instruction.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	manager := tenantTestManager(t, t.TempDir())
	ownerSkills, err := manager.OwnerServer().Workspace().UserSkills()
	if err != nil || len(ownerSkills) != 1 || ownerSkills[0].Name != "host-only" {
		t.Fatalf("owner lost native skill defaults: %+v, %v", ownerSkills, err)
	}
	account, err := manager.Resolve(bots.Account{ID: testAccountA})
	if err != nil {
		t.Fatal(err)
	}
	skills, err := account.Workspace().UserSkills()
	if err != nil || len(skills) != 0 {
		t.Fatalf("non-owner exposed host skills: %+v, %v", skills, err)
	}
}

func TestTenantManagerConcurrentResolveAndCloseReleaseEveryStore(t *testing.T) {
	manager := tenantTestManager(t, t.TempDir())
	const callers = 12
	results := make(chan *bots.Server, callers)
	failures := make(chan error, callers)
	var callersDone sync.WaitGroup
	for range callers {
		callersDone.Add(1)
		go func() {
			defer callersDone.Done()
			server, err := manager.Resolve(bots.Account{ID: testAccountA})
			results <- server
			failures <- err
		}()
	}
	callersDone.Wait()
	close(results)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first *bots.Server
	for result := range results {
		if first == nil {
			first = result
		}
		if result != first {
			t.Fatal("concurrent requests created multiple tenant services")
		}
	}
	roots := []string{manager.root, manager.tenants[testAccountA].store.Root()}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("second close failed: %v", err)
	}
	if _, err := manager.Resolve(bots.Account{ID: testAccountA}); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("closed manager still accepted a tenant: %v", err)
	}
	for _, root := range roots {
		store, err := bots.OpenStore(root)
		if err != nil {
			t.Fatalf("tenant close did not release store lock: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTenantManagerRejectsUnsafeAccountPathsAndDoesNotCacheFailedOpen(t *testing.T) {
	manager := tenantTestManager(t, t.TempDir())
	for _, id := range []string{"", "owner", "../owner", "user_../owner", "user_ABCDEF12345678901234567890123456", testAccountA + "/../" + testAccountB} {
		if _, err := manager.Resolve(bots.Account{ID: id}); !errors.Is(err, bots.ErrInvalid) {
			t.Fatalf("unsafe account identity %q accepted: %v", id, err)
		}
	}
	users := filepath.Join(manager.root, "users")
	if err := os.Symlink(t.TempDir(), users); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Resolve(bots.Account{ID: testAccountA}); err == nil {
		t.Fatal("symbolic account parent directory accepted")
	}
	if len(manager.tenants) != 0 {
		t.Fatal("failed account initialization was cached")
	}
	if err := os.Remove(users); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Resolve(bots.Account{ID: testAccountA}); err != nil {
		t.Fatalf("repaired account directory could not be opened: %v", err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(users, testAccountB)); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Resolve(bots.Account{ID: testAccountB}); err == nil {
		t.Fatal("symbolic account workspace accepted")
	}
	if manager.tenants[testAccountB] != nil {
		t.Fatal("symbolic account workspace was cached")
	}
}

func TestTenantManagerInternalCredentialsAreScopedAndInvalidAfterClose(t *testing.T) {
	manager := tenantTestManager(t, t.TempDir())
	account, err := manager.Resolve(bots.Account{ID: testAccountA})
	if err != nil {
		t.Fatal(err)
	}
	if manager.owner.internalHash == manager.tenants[testAccountA].internalHash {
		t.Fatal("accounts shared an internal bridge credential")
	}
	manager.owner.internalHash = sha256.Sum256([]byte("test-owner-bridge"))
	manager.tenants[testAccountA].internalHash = sha256.Sum256([]byte("test-account-bridge"))
	for raw, expected := range map[string]*bots.Server{
		"test-owner-bridge": manager.OwnerServer(), "test-account-bridge": account, "": nil, "wrong": nil,
	} {
		server, ok := manager.ResolveInternalToken(raw)
		if server != expected || ok != (expected != nil) {
			t.Fatal("internal credential resolved to the wrong account")
		}
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := manager.ResolveInternalToken("test-account-bridge"); ok {
		t.Fatal("closed manager accepted an internal credential")
	}
}

func TestTenantManagerFailedInitializationReleasesOwnerStore(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "extensions"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := NewTenantManager(TenantManagerConfig{DataRoot: root, CodexURL: "unix:///dedicated.sock"})
	if err == nil || manager != nil {
		t.Fatal("incomplete owner workspace was accepted")
	}
	store, err := bots.OpenStore(root)
	if err != nil {
		t.Fatalf("failed initialization leaked owner store lock: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestTenantRuntimeUsesAccountHomeAndSharedExplicitTransport(t *testing.T) {
	store, err := bots.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	workspace := bots.NewWorkspace(store)
	config := TenantManagerConfig{CodexURL: "unix:///dedicated.sock", InternalURL: "http://127.0.0.1:9830", FlovURL: "http://voice.local/transcribe"}
	account := tenantRuntimeConfig(config, store, workspace, "/tenant/extension.ts", "test-internal", false)
	if account.AgentOptions["codex"]["app_server_url"] != config.CodexURL {
		t.Fatal("account runtime replaced the explicit product transport")
	}
	for _, backend := range []string{"codex", "pi"} {
		env := account.AgentOptions[backend]["env"].(map[string]string)
		if env["HOME"] != store.Root() {
			t.Fatalf("%s runtime inherited the host home", backend)
		}
		if backend == "pi" && env["PI_CODING_AGENT_DIR"] != filepath.Join(store.Root(), ".pi", "agent") {
			t.Fatal("Pi configuration was not account-local")
		}
	}
	owner := tenantRuntimeConfig(config, store, workspace, "/owner/extension.ts", "test-owner", true)
	if owner.AgentOptions["codex"]["env"] != nil || owner.AgentOptions["pi"] != nil {
		t.Fatal("owner lost native harness environment defaults")
	}
	if !account.VoiceAvailable || account.PiExtensionPath != "/tenant/extension.ts" || account.InternalURL != config.InternalURL {
		t.Fatal("runtime lost account voice or bridge configuration")
	}
	if account.ResolveSkills == nil || owner.ResolveSkills == nil {
		t.Fatal("runtime lost the tenant-scoped skill attachment resolver")
	}
}
