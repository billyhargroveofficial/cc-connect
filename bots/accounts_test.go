package bots

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func testAccountStore(t *testing.T) *AccountStore {
	t.Helper()
	store, err := OpenAccountStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return store
}

func accountState(t *testing.T, path string) accountDiskState {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state accountDiskState
	if err := json.Unmarshal(content, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestAccountStore_PrivatePersistenceAndSafeAccount(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	store, err := OpenAccountStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if store.Count() != 0 || store.HasOwner() {
		t.Fatal("fresh account store is not empty")
	}
	password := "  private account password  "
	account, err := store.Register("  BiLLy.name-1_  ", password, true)
	if err != nil {
		t.Fatal(err)
	}
	if !accountIDPattern.MatchString(account.ID) || account.Username != "billy.name-1_" || !account.Owner || account.CreatedAt.IsZero() {
		t.Fatalf("registered account = %+v", account)
	}
	if store.Count() != 1 || !store.HasOwner() {
		t.Fatal("account metadata was not updated")
	}
	if got, err := store.Get(account.ID); err != nil || got != account {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if _, err := store.Get("user_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown account = %v", err)
	}
	token, _, err := store.NewSession(account.ID)
	if err != nil {
		t.Fatal(err)
	}
	public, err := json.Marshal(account)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"password", "$2", password, token} {
		if bytes.Contains(public, []byte(forbidden)) {
			t.Fatalf("public account includes private data: %q", forbidden)
		}
	}
	content, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{password, token} {
		if bytes.Contains(content, []byte(forbidden)) {
			t.Fatal("private state contains a plaintext password or session token")
		}
	}
	state := accountState(t, store.path)
	if len(state.Accounts) != 1 || len(state.Sessions) != 1 {
		t.Fatalf("persisted account/session counts = %d/%d", len(state.Accounts), len(state.Sessions))
	}
	if cost, err := bcrypt.Cost([]byte(state.Accounts[0].PasswordHash)); err != nil || cost != 12 {
		t.Fatalf("password hash cost = %d, %v", cost, err)
	}
	digest := sha256.Sum256([]byte(token))
	if state.Sessions[0].Digest != hex.EncodeToString(digest[:]) || state.Sessions[0].UserID != account.ID {
		t.Fatal("session did not persist only its digest and account reference")
	}
	for path, permissions := range map[string]os.FileMode{
		root: 0700, filepath.Join(root, "auth"): 0700,
		store.path: 0600, filepath.Join(root, "auth", "accounts.lock"): 0600,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != permissions {
			t.Fatalf("%s permissions = %o, want %o", path, info.Mode().Perm(), permissions)
		}
	}
	if other, err := OpenAccountStore(root); err == nil {
		other.Close()
		t.Fatal("two writers acquired the same account store")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenAccountStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got, err := reopened.Authenticate("BILLY.NAME-1_", password); err != nil || got != account {
		t.Fatalf("authentication after restart = %+v, %v", got, err)
	}
	if got, err := reopened.ResolveSession(token); err != nil || got != account {
		t.Fatalf("session after restart = %+v, %v", got, err)
	}
}

func TestAccountStore_ConcurrentNormalizedDuplicateRegistration(t *testing.T) {
	store := testAccountStore(t)
	const requests = 6
	start := make(chan struct{})
	results := make(chan error, requests)
	var group sync.WaitGroup
	for i := 0; i < requests; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			_, err := store.Register("  SAME.User  ", "duplicate-safe password", false)
			results <- err
		}()
	}
	close(start)
	group.Wait()
	close(results)
	succeeded, duplicates := 0, 0
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrUsernameTaken):
			duplicates++
		default:
			t.Fatalf("unexpected registration error: %v", err)
		}
	}
	if succeeded != 1 || duplicates != requests-1 || store.Count() != 1 {
		t.Fatalf("registrations = %d successes, %d duplicates, %d records", succeeded, duplicates, store.Count())
	}
	if _, err := store.Register("same.user", "another password", false); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("normalized duplicate = %v", err)
	}
	owner, err := store.Register("owner", "owner account password", true)
	if err != nil || !owner.Owner {
		t.Fatalf("owner registration = %+v, %v", owner, err)
	}
	if _, err := store.Register("second-owner", "another owner password", true); !errors.Is(err, ErrConflict) {
		t.Fatalf("second owner = %v", err)
	}
}

func TestAccountStore_RejectsInvalidRegistrationInput(t *testing.T) {
	store := testAccountStore(t)
	for _, username := range []string{"", "ab", strings.Repeat("a", 33), ".name", "_name", "-name", "a/b", "a\\b", "user name", "юзер", "Key"} {
		if _, err := store.Register(username, "valid password", false); !errors.Is(err, ErrInvalid) {
			t.Errorf("username %q error = %v", username, err)
		}
	}
	for _, password := range []string{"", "1234567", strings.Repeat("a", 129), strings.Repeat("я", 65)} {
		if _, err := store.Register("valid-user", password, false); !errors.Is(err, ErrInvalid) {
			t.Errorf("password byte length %d error = %v", len(password), err)
		}
	}
	if store.Count() != 0 {
		t.Fatal("invalid input created an account")
	}
}

func TestAccountStore_AuthenticationIsGenericAndPreservesPasswordBytes(t *testing.T) {
	store := testAccountStore(t)
	account, err := store.Register("User", "password with spaces  ", false)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := store.Authenticate(" USER ", "password with spaces  "); err != nil || got != account {
		t.Fatalf("normalized login = %+v, %v", got, err)
	}
	for _, attempt := range [][2]string{
		{"user", "wrong password"}, {"unknown", "wrong password"},
		{"user", "password with spaces"}, {"user", ""}, {"unknown", ""},
		{"user", strings.Repeat("a", 129)},
	} {
		if _, err := store.Authenticate(attempt[0], attempt[1]); err != ErrInvalidCredentials {
			t.Errorf("failed authentication error = %v, want the same generic error", err)
		}
	}
	longPassword := strings.Repeat("z", 127) + "a"
	longAccount, err := store.Register("long-password", longPassword, false)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := store.Authenticate("long-password", longPassword); err != nil || got != longAccount {
		t.Fatalf("128-byte password authentication = %+v, %v", got, err)
	}
	if _, err := store.Authenticate("long-password", strings.Repeat("z", 127)+"b"); err != ErrInvalidCredentials {
		t.Fatal("password bytes beyond bcrypt's raw limit were ignored")
	}
}

func TestAccountStore_SessionTamperExpiryAndServerSideRevocation(t *testing.T) {
	root := t.TempDir()
	store, err := OpenAccountStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	store.now = func() time.Time { return now }
	account, err := store.Register("sessions", "session account password", false)
	if err != nil {
		t.Fatal(err)
	}
	token, expiresAt, err := store.NewSession(account.ID)
	if err != nil || !expiresAt.Equal(now.Add(30*24*time.Hour)) {
		t.Fatalf("new session expiry = %v, %v", expiresAt, err)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(decoded) != 32 {
		t.Fatalf("session entropy bytes = %d, %v", len(decoded), err)
	}
	decoded[0] ^= 1
	tampered := base64.RawURLEncoding.EncodeToString(decoded)
	for _, bad := range []string{"", "invalid", token + "=", token + "x", tampered} {
		if _, err := store.ResolveSession(bad); err != ErrInvalidCredentials {
			t.Errorf("tampered session accepted: %v", err)
		}
	}
	if _, _, err := store.NewSession("user_missing"); err != ErrInvalidCredentials {
		t.Fatalf("session for missing account = %v", err)
	}
	if err := store.RevokeSession(token); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveSession(token); err != ErrInvalidCredentials {
		t.Fatal("logged-out cookie remained valid")
	}
	if err := store.RevokeSession(token); err != nil {
		t.Fatalf("idempotent logout = %v", err)
	}
	if err := store.RevokeSession("malformed"); err != nil {
		t.Fatalf("malformed logout = %v", err)
	}
	live, _, err := store.NewSession(account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenAccountStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.now = func() time.Time { return now }
	if _, err := reopened.ResolveSession(token); err != ErrInvalidCredentials {
		t.Fatal("restart resurrected a revoked cookie")
	}
	if got, err := reopened.ResolveSession(live); err != nil || got != account {
		t.Fatalf("restart lost live session = %+v, %v", got, err)
	}
	now = expiresAt
	if _, err := reopened.ResolveSession(live); err != ErrInvalidCredentials {
		t.Fatal("session remained valid at its absolute expiry")
	}
	if state := accountState(t, reopened.path); len(state.Sessions) != 0 {
		t.Fatal("expired sessions were not removed from private state")
	}
}

func TestAccountStore_SessionLimitsKeepLatestAndCleanExpired(t *testing.T) {
	store := testAccountStore(t)
	now := time.Now().UTC()
	store.now = func() time.Time { return now }
	account, err := store.Register("many-sessions", "many session password", false)
	if err != nil {
		t.Fatal(err)
	}
	tokens := make([]string, 0, accountMaxSessionsPerUser+2)
	for i := 0; i < accountMaxSessionsPerUser+2; i++ {
		now = now.Add(time.Second)
		token, _, err := store.NewSession(account.ID)
		if err != nil {
			t.Fatal(err)
		}
		tokens = append(tokens, token)
	}
	for i, token := range tokens {
		_, err := store.ResolveSession(token)
		if i < 2 && err != ErrInvalidCredentials || i >= 2 && err != nil {
			t.Fatalf("session %d resolution = %v", i, err)
		}
	}
	if state := accountState(t, store.path); len(state.Sessions) != accountMaxSessionsPerUser {
		t.Fatalf("stored sessions = %d", len(state.Sessions))
	}
	now = now.Add(AccountSessionLifetime)
	if _, _, err := store.NewSession(account.ID); err != nil {
		t.Fatal(err)
	}
	if state := accountState(t, store.path); len(state.Sessions) != 1 {
		t.Fatal("new session did not clean old expired records")
	}
}

func TestAccountStore_FailedPersistenceRollsBackMutations(t *testing.T) {
	store := testAccountStore(t)
	account, err := store.Register("rollback", "rollback password", false)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := store.NewSession(account.ID)
	if err != nil {
		t.Fatal(err)
	}
	store.path = filepath.Join(t.TempDir(), "missing-directory", "accounts.json")
	if _, err := store.Register("failed-user", "failed password", false); err == nil || store.Count() != 1 {
		t.Fatal("failed registration remained in memory")
	}
	if _, _, err := store.NewSession(account.ID); err == nil || len(store.sessions) != 1 {
		t.Fatal("failed session creation remained in memory")
	}
	if err := store.RevokeSession(token); err == nil {
		t.Fatal("logout reported success without persisting revocation")
	}
	if got, err := store.ResolveSession(token); err != nil || got != account {
		t.Fatal("failed revocation removed the live in-memory session")
	}
}

func TestAccountStore_RejectsSymlinkedPrivateState(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "auth"), 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "accounts.json")
	content := []byte(`{"version":1,"accounts":[],"sessions":[]}`)
	if err := os.WriteFile(outside, content, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "auth", "accounts.json")); err != nil {
		t.Fatal(err)
	}
	if store, err := OpenAccountStore(root); err == nil {
		store.Close()
		t.Fatal("symlinked private state was accepted")
	}
	if info, err := os.Stat(outside); err != nil || info.Mode().Perm() != 0644 {
		t.Fatal("opening symlinked state changed the target")
	}
}

func TestAccountStore_PostCommitSyncFailureKeepsMemoryConsistent(t *testing.T) {
	root := t.TempDir()
	store, err := OpenAccountStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	account, err := store.Register("before-sync", "before sync password", false)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := store.NewSession(account.ID)
	if err != nil {
		t.Fatal(err)
	}
	syncFailure := errors.New("injected directory sync failure")
	store.write = func(path string, content []byte) error {
		if err := writePrivateAtomic(path, content); err != nil {
			return err
		}
		return syncFailure
	}
	if _, err := store.Register("after-sync", "after sync password", false); !errors.Is(err, syncFailure) {
		t.Fatalf("post-commit registration error = %v", err)
	}
	if store.Count() != 2 || len(accountState(t, store.path).Accounts) != 2 {
		t.Fatal("post-commit registration diverged between memory and disk")
	}
	if _, _, err := store.NewSession(account.ID); !errors.Is(err, syncFailure) {
		t.Fatalf("post-commit session error = %v", err)
	}
	if len(store.sessions) != 2 || len(accountState(t, store.path).Sessions) != 2 {
		t.Fatal("post-commit session creation diverged between memory and disk")
	}
	if err := store.RevokeSession(token); !errors.Is(err, syncFailure) {
		t.Fatalf("post-commit logout error = %v", err)
	}
	if _, err := store.ResolveSession(token); err != ErrInvalidCredentials {
		t.Fatal("post-commit revocation left the cookie valid in memory")
	}
	if len(store.sessions) != 1 || len(accountState(t, store.path).Sessions) != 1 {
		t.Fatal("post-commit revocation diverged between memory and disk")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenAccountStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.Authenticate("after-sync", "after sync password"); err != nil {
		t.Fatalf("committed registration after restart = %v", err)
	}
	if _, err := reopened.ResolveSession(token); err != ErrInvalidCredentials {
		t.Fatal("restart resurrected post-commit revoked session")
	}
}

func TestAccountStore_ListIsStableAndReturnsSafeCopies(t *testing.T) {
	store := testAccountStore(t)
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	first, err := store.Register("first", "first password", false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Register("second", "second password", false)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(-time.Hour)
	earlier, err := store.Register("earlier", "earlier password", false)
	if err != nil {
		t.Fatal(err)
	}
	tiedIDs := []string{first.ID, second.ID}
	sort.Strings(tiedIDs)
	listed := store.List()
	if len(listed) != 3 || listed[0] != earlier || listed[1].ID != tiedIDs[0] || listed[2].ID != tiedIDs[1] {
		t.Fatalf("account order = %+v", listed)
	}
	listed[0].Username = "changed"
	if got, err := store.Get(earlier.ID); err != nil || got != earlier {
		t.Fatal("mutating a listed account changed private state")
	}
	content, err := json.Marshal(store.List())
	if err != nil || bytes.Contains(content, []byte("password")) || bytes.Contains(content, []byte("$2")) {
		t.Fatal("account list exposes password data")
	}
}

func TestAccountStore_RejectsSymlinkedLockWithoutChangingTarget(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "auth"), 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside-lock")
	if err := os.WriteFile(outside, []byte("untouched"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "auth", "accounts.lock")); err != nil {
		t.Fatal(err)
	}
	if store, err := OpenAccountStore(root); err == nil {
		store.Close()
		t.Fatal("symlinked account lock was accepted")
	}
	if info, err := os.Stat(outside); err != nil || info.Mode().Perm() != 0644 {
		t.Fatal("opening a symlinked lock changed the target")
	}
}

func TestAccountStore_RejectsCorruptPersistedAccounts(t *testing.T) {
	seed := testAccountStore(t)
	account, err := seed.Register("seed", "seed password", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := seed.NewSession(account.ID); err != nil {
		t.Fatal(err)
	}
	state := accountState(t, seed.path)
	content, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*accountDiskState)
	}{
		{"version", func(state *accountDiskState) { state.Version = 99 }},
		{"id", func(state *accountDiskState) { state.Accounts[0].ID = "../escape" }},
		{"name", func(state *accountDiskState) { state.Accounts[0].Username = "UpperCase" }},
		{"timestamp", func(state *accountDiskState) { state.Accounts[0].CreatedAt = time.Time{} }},
		{"duplicate id", func(state *accountDiskState) { state.Accounts = append(state.Accounts, state.Accounts[0]) }},
		{"duplicate username", func(state *accountDiskState) {
			duplicate := state.Accounts[0]
			duplicate.ID = "user_" + strings.Repeat("a", 32)
			state.Accounts = append(state.Accounts, duplicate)
		}},
		{"multiple owners", func(state *accountDiskState) {
			duplicate := state.Accounts[0]
			duplicate.ID = "user_" + strings.Repeat("a", 32)
			duplicate.Username = "another-owner"
			state.Accounts = append(state.Accounts, duplicate)
		}},
		{"bad hash alphabet", func(state *accountDiskState) {
			state.Accounts[0].PasswordHash = state.Accounts[0].PasswordHash[:59] + "!"
		}},
		{"hash trailing junk", func(state *accountDiskState) { state.Accounts[0].PasswordHash += "junk" }},
		{"hash wrong cost", func(state *accountDiskState) {
			state.Accounts[0].PasswordHash = strings.Replace(state.Accounts[0].PasswordHash, "$12$", "$10$", 1)
		}},
		{"session digest", func(state *accountDiskState) { state.Sessions[0].Digest = "raw-token" }},
		{"session missing user", func(state *accountDiskState) { state.Sessions[0].UserID = "user_" + strings.Repeat("f", 32) }},
		{"duplicate session", func(state *accountDiskState) { state.Sessions = append(state.Sessions, state.Sessions[0]) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			var corrupted accountDiskState
			if err := json.Unmarshal(content, &corrupted); err != nil {
				t.Fatal(err)
			}
			test.mutate(&corrupted)
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "auth"), 0700); err != nil {
				t.Fatal(err)
			}
			serialized, err := json.Marshal(corrupted)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "auth", "accounts.json")
			if err := os.WriteFile(path, serialized, 0600); err != nil {
				t.Fatal(err)
			}
			if store, err := OpenAccountStore(root); err == nil {
				store.Close()
				t.Fatal("corrupt account state was accepted")
			}
			if err := os.WriteFile(path, content, 0600); err != nil {
				t.Fatal(err)
			}
			store, err := OpenAccountStore(root)
			if err != nil {
				t.Fatalf("failed open did not release its lock: %v", err)
			}
			store.Close()
		})
	}
}

func TestAccountStore_ConcurrentSessionsAreResolvedAndRevoked(t *testing.T) {
	store := testAccountStore(t)
	account, err := store.Register("concurrent-sessions", "concurrent password", false)
	if err != nil {
		t.Fatal(err)
	}
	const workers = 4
	start := make(chan struct{})
	failures := make(chan error, workers)
	var group sync.WaitGroup
	for i := 0; i < workers; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			for j := 0; j < 3; j++ {
				token, _, err := store.NewSession(account.ID)
				if err != nil {
					failures <- err
					return
				}
				if got, err := store.ResolveSession(token); err != nil || got != account {
					failures <- errors.New("concurrent session was not resolved to its account")
					return
				}
				if err := store.RevokeSession(token); err != nil {
					failures <- err
					return
				}
				if _, err := store.ResolveSession(token); err != ErrInvalidCredentials {
					failures <- errors.New("concurrent revoked session remained valid")
					return
				}
				store.List()
				store.Count()
				store.HasOwner()
			}
		}()
	}
	close(start)
	group.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	if state := accountState(t, store.path); len(state.Sessions) != 0 {
		t.Fatal("concurrent logout left session records on disk")
	}
}

func TestAccountStore_RestartPrunesExpiredSessions(t *testing.T) {
	root := t.TempDir()
	store, err := OpenAccountStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC().Add(-AccountSessionLifetime - time.Hour)
	store.now = func() time.Time { return now }
	account, err := store.Register("expired-session", "expired password", false)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := store.NewSession(account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenAccountStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.ResolveSession(token); err != ErrInvalidCredentials {
		t.Fatal("restart retained an expired session")
	}
	if state := accountState(t, reopened.path); len(state.Sessions) != 0 {
		t.Fatal("restart did not persist expiry cleanup")
	}
}

func TestAccountStore_RestartBoundsTotalStoredSessions(t *testing.T) {
	root := t.TempDir()
	store, err := OpenAccountStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Register("session-seed", "session seed password", false); err != nil {
		t.Fatal(err)
	}
	state := accountState(t, store.path)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	var oldest, newest string
	// Simulate a persisted store exceeding the global cap, while every user
	// individually remains below the per-account limit.
	for i := 0; i < 205; i++ {
		account := state.Accounts[0]
		account.ID = fmt.Sprintf("user_%032x", i+1)
		account.Username = fmt.Sprintf("cap-user-%d", i+1)
		state.Accounts = append(state.Accounts, account)
		for j := 0; j < accountMaxSessionsPerUser; j++ {
			index := i*accountMaxSessionsPerUser + j
			digest := sha256.Sum256([]byte(fmt.Sprintf("session-%d", index)))
			encoded := hex.EncodeToString(digest[:])
			if index == 0 {
				oldest = encoded
			}
			newest = encoded
			state.Sessions = append(state.Sessions, accountSession{
				Digest: encoded, UserID: account.ID,
				ExpiresAt: now.Add(AccountSessionLifetime + time.Duration(index)*time.Second),
			})
		}
	}
	content, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.path, content, 0600); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenAccountStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	persisted := accountState(t, reopened.path)
	if len(persisted.Sessions) != accountMaxSessions || len(reopened.sessions) != accountMaxSessions {
		t.Fatalf("bounded sessions = %d disk / %d memory", len(persisted.Sessions), len(reopened.sessions))
	}
	if _, exists := reopened.sessions[oldest]; exists {
		t.Fatal("global cleanup retained oldest session")
	}
	if _, exists := reopened.sessions[newest]; !exists {
		t.Fatal("global cleanup removed newest session")
	}
}
