package bots

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"
)

const hostTestPassword = "test password with untrimmed spaces "

type hostTestFixture struct {
	root        string
	host        *HostServer
	http        *httptest.Server
	mu          sync.Mutex
	legacyStore *Store
	stores      map[string]*Store
	servers     map[string]*Server
}

func newHostTest(t *testing.T, legacy bool) *hostTestFixture {
	t.Helper()
	f := &hostTestFixture{root: t.TempDir(), stores: make(map[string]*Store), servers: make(map[string]*Server)}
	if legacy {
		var err error
		f.legacyStore, err = OpenStore(f.root)
		if err != nil {
			t.Fatal(err)
		}
	}
	var err error
	f.host, err = NewHostServer(HostServerConfig{
		Root: f.root, RegistrationAllowed: true, LegacyWorkspace: legacy,
		LegacyToken: testOwnerToken, ResolveTenant: f.resolve,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.http = httptest.NewServer(f.host.Handler())
	t.Cleanup(func() {
		if err := f.host.Close(); err != nil {
			t.Error(err)
		}
		f.http.Close()
		for _, server := range f.servers {
			if err := server.Close(); err != nil {
				t.Error(err)
			}
		}
		closed := make(map[*Store]bool)
		for _, store := range f.stores {
			if err := store.Close(); err != nil {
				t.Error(err)
			}
			closed[store] = true
		}
		if f.legacyStore != nil && !closed[f.legacyStore] {
			if err := f.legacyStore.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	return f
}

func (f *hostTestFixture) resolve(_ context.Context, account Account) (*Server, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if server := f.servers[account.ID]; server != nil {
		return server, nil
	}
	root := filepath.Join(f.root, "users", account.ID)
	store := (*Store)(nil)
	if account.Owner {
		root, store = f.root, f.legacyStore
	}
	var err error
	if store == nil {
		store, err = OpenStore(root)
		if err != nil {
			return nil, err
		}
	}
	workspace := NewWorkspace(store)
	workspace.Configure(WorkspaceConfig{
		NativeSkillDirs: []string{}, BackendSkillDirs: map[string][]string{}, SystemSkillDirs: []string{},
	})
	server, err := NewTenantServer(store, nil, ServerConfig{Workspace: workspace})
	if err != nil {
		store.Close()
		return nil, err
	}
	f.stores[account.ID], f.servers[account.ID] = store, server
	return server, nil
}

func hostAuth(t *testing.T, f *hostTestFixture, path, username string, cookie *http.Cookie, extras map[string]string) (hostSession, *http.Cookie) {
	t.Helper()
	input := map[string]string{"username": username, "password": hostTestPassword}
	for key, value := range extras {
		input[key] = value
	}
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	response := hostRequestHTTP(t, f, http.MethodPost, path, string(body), f.http.URL, cookie)
	defer response.Body.Close()
	want := http.StatusOK
	if path == "/api/studio/register" {
		want = http.StatusCreated
	}
	content, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != want {
		t.Fatalf("%s = %d: %s", path, response.StatusCode, content)
	}
	for _, private := range []string{hostTestPassword, testOwnerToken, "passwordHash", "\"password\":", "\"owner\":", "\"createdAt\":", "\"token\":"} {
		if bytes.Contains(content, []byte(private)) {
			t.Fatalf("private account value in auth response: %s", content)
		}
	}
	var session hostSession
	if err := json.Unmarshal(content, &session); err != nil || !session.Authenticated || session.User == nil {
		t.Fatalf("auth response = %s: %v", content, err)
	}
	cookies := response.Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode || cookies[0].Path != "/api/studio" || cookies[0].MaxAge != int(AccountSessionLifetime.Seconds()) {
		t.Fatalf("session cookie settings = %+v", cookies)
	}
	if len(cookies[0].Value) != 43 || strings.Contains(cookies[0].Value, ".") {
		t.Fatal("account session was not an opaque random value")
	}
	return session, cookies[0]
}

func hostRequestHTTP(t *testing.T, f *hostTestFixture, method, path, body, origin string, cookie *http.Cookie) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, f.http.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		request.AddCookie(cookie)
		if account, err := f.host.accounts.ResolveSession(cookie.Value); err == nil {
			request.Header.Set(ExpectedAccountHeader, account.ID)
		}
	}
	response, err := f.http.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func hostResponse(t *testing.T, response *http.Response, status int) []byte {
	t.Helper()
	defer response.Body.Close()
	content, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != status {
		t.Fatalf("HTTP status = %d, want %d: %s", response.StatusCode, status, content)
	}
	return content
}

func hostReadSession(t *testing.T, f *hostTestFixture, cookie *http.Cookie) hostSession {
	t.Helper()
	content := hostResponse(t, hostRequestHTTP(t, f, http.MethodGet, "/api/studio/session", "", "", cookie), http.StatusOK)
	var session hostSession
	if err := json.Unmarshal(content, &session); err != nil {
		t.Fatal(err)
	}
	return session
}

func TestHostServer_AccountSignupLoginRotationAndLogout(t *testing.T) {
	f := newHostTest(t, false)
	if session := hostReadSession(t, f, nil); session.Authenticated || session.User != nil || !session.RegistrationAllowed || !session.SetupRequired || session.LegacyClaimAvailable {
		t.Fatalf("fresh session = %+v", session)
	}
	hostResponse(t, hostRequestHTTP(t, f, http.MethodGet, "/api/studio/bots", "", "", nil), http.StatusUnauthorized)
	session, cookie := hostAuth(t, f, "/api/studio/register", "Alice", nil, nil)
	account, err := f.host.accounts.Get(session.User.ID)
	if err != nil || !account.Owner || session.User.Username != "alice" || session.SetupRequired || f.stores[account.ID].Root() != f.root {
		t.Fatalf("first account = %+v %+v %v", account, session, err)
	}
	if _, err := os.Stat(filepath.Join(f.root, "token")); !os.IsNotExist(err) {
		t.Fatalf("fresh account setup generated a token: %v", err)
	}
	if got := hostReadSession(t, f, cookie); !got.Authenticated || got.User == nil || *got.User != *session.User {
		t.Fatalf("account session = %+v", got)
	}
	var invalidBodies [][]byte
	for _, input := range []string{
		`{"username":"alice","password":"wrong password"}`,
		`{"username":"missing","password":"wrong password"}`,
	} {
		invalidBodies = append(invalidBodies, hostResponse(t, hostRequestHTTP(t, f, http.MethodPost, "/api/studio/login", input, f.http.URL, nil), http.StatusUnauthorized))
	}
	if !bytes.Equal(invalidBodies[0], invalidBodies[1]) {
		t.Fatal("login errors disclose whether an account exists")
	}
	hostResponse(t, hostRequestHTTP(t, f, http.MethodPost, "/api/studio/login", `{"token":"`+testOwnerToken+`"}`, f.http.URL, nil), http.StatusBadRequest)
	_, rotated := hostAuth(t, f, "/api/studio/login", " ALICE ", cookie, nil)
	if rotated.Value == cookie.Value {
		t.Fatal("login did not rotate the account session")
	}
	hostResponse(t, hostRequestHTTP(t, f, http.MethodGet, "/api/studio/bots", "", "", cookie), http.StatusUnauthorized)
	hostResponse(t, hostRequestHTTP(t, f, http.MethodGet, "/api/studio/bots", "", "", rotated), http.StatusOK)
	forged := *rotated
	forged.Value = rotated.Value[:len(rotated.Value)-1] + "!"
	hostResponse(t, hostRequestHTTP(t, f, http.MethodGet, "/api/studio/bots", "", "", &forged), http.StatusUnauthorized)
	response := hostRequestHTTP(t, f, http.MethodPost, "/api/studio/logout", "", f.http.URL, rotated)
	if cookies := response.Cookies(); len(cookies) != 1 || cookies[0].MaxAge != -1 {
		t.Fatalf("logout did not clear cookie: %+v", cookies)
	}
	hostResponse(t, response, http.StatusOK)
	hostResponse(t, hostRequestHTTP(t, f, http.MethodGet, "/api/studio/bots", "", "", rotated), http.StatusUnauthorized)
	if session := hostReadSession(t, f, rotated); session.Authenticated || session.User != nil {
		t.Fatalf("revoked session = %+v", session)
	}
}

func TestHostServer_RegistrationWorkspaceFailureExplainsAccountAlreadyCreated(t *testing.T) {
	f := newHostTest(t, false)
	f.host.config.ResolveTenant = func(context.Context, Account) (*Server, error) {
		return nil, errors.New("private tenant initialization detail")
	}
	input, _ := json.Marshal(map[string]string{"username": "alice", "password": hostTestPassword})
	response := hostRequestHTTP(t, f, http.MethodPost, "/api/studio/register", string(input), f.http.URL, nil)
	if cookies := response.Cookies(); len(cookies) != 0 {
		t.Fatal("failed workspace initialization issued an authenticated cookie")
	}
	body := hostResponse(t, response, http.StatusServiceUnavailable)
	var detail map[string]string
	if err := json.Unmarshal(body, &detail); err != nil || detail["code"] != "account_created_workspace_unavailable" || !strings.Contains(detail["error"], "account was created") || !strings.Contains(detail["error"], "Sign in") {
		t.Fatalf("registration failure did not explain retry path: %s %v", body, err)
	}
	account, err := f.host.accounts.Authenticate("alice", hostTestPassword)
	if err != nil || f.host.accounts.Count() != 1 {
		t.Fatalf("registration did not persist the account: %+v %v", account, err)
	}
	for _, private := range []string{hostTestPassword, testOwnerToken, account.ID, "passwordHash", "private tenant initialization detail", "\"owner\":", "\"createdAt\":"} {
		if bytes.Contains(body, []byte(private)) {
			t.Fatalf("registration failure disclosed private account data: %s", body)
		}
	}
	body = hostResponse(t, hostRequestHTTP(t, f, http.MethodPost, "/api/studio/login", string(input), f.http.URL, nil), http.StatusServiceUnavailable)
	if bytes.Contains(body, []byte("account_created_workspace_unavailable")) || bytes.Contains(body, []byte("account was created")) {
		t.Fatalf("normal login described a new registration: %s", body)
	}
	f.host.config.ResolveTenant = f.resolve
	session, _ := hostAuth(t, f, "/api/studio/login", "alice", nil, nil)
	if session.User.ID != account.ID || f.host.accounts.Count() != 1 {
		t.Fatal("Sign in retry did not reopen the persisted account")
	}
}

func TestHostServer_LegacyCookieClaimsExistingWorkspaceOnce(t *testing.T) {
	f := newHostTest(t, true)
	original := f.legacyStore.ListBots()[0]
	if _, err := f.legacyStore.AppendEvent(original.ID, "", "system", map[string]string{"content": "legacy history"}); err != nil {
		t.Fatal(err)
	}
	old, err := NewServer(f.legacyStore, nil, ServerConfig{Token: testOwnerToken})
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	value, err := old.newCookie()
	if err != nil {
		t.Fatal(err)
	}
	legacy := &http.Cookie{Name: sessionCookieName, Value: value}
	if session := hostReadSession(t, f, nil); session.SetupRequired || session.LegacyClaimAvailable {
		t.Fatalf("anonymous legacy session = %+v", session)
	}
	if session := hostReadSession(t, f, legacy); session.Authenticated || !session.LegacyClaimAvailable {
		t.Fatalf("migration session = %+v", session)
	}
	hostResponse(t, hostRequestHTTP(t, f, http.MethodGet, "/api/studio/bots", "", "", legacy), http.StatusUnauthorized)
	guest, _ := hostAuth(t, f, "/api/studio/register", "guest", nil, nil)
	guestAccount, _ := f.host.accounts.Get(guest.User.ID)
	if guestAccount.Owner || f.stores[guestAccount.ID].Root() != filepath.Join(f.root, "users", guestAccount.ID) || f.host.accounts.HasOwner() {
		t.Fatalf("anonymous signup claimed legacy root: %+v", guestAccount)
	}
	owner, ownerCookie := hostAuth(t, f, "/api/studio/register", "owner", legacy, nil)
	ownerAccount, _ := f.host.accounts.Get(owner.User.ID)
	if !ownerAccount.Owner || f.stores[ownerAccount.ID] != f.legacyStore {
		t.Fatalf("legacy root not preserved: %+v", ownerAccount)
	}
	content := hostResponse(t, hostRequestHTTP(t, f, http.MethodGet, "/api/studio/bots/"+original.ID+"/events", "", "", ownerCookie), http.StatusOK)
	if !bytes.Contains(content, []byte("legacy history")) {
		t.Fatal("claiming existing workspace lost its history")
	}
	if session := hostReadSession(t, f, legacy); session.LegacyClaimAvailable || session.Authenticated {
		t.Fatalf("legacy credential remained privileged after claim: %+v", session)
	}
	third, _ := hostAuth(t, f, "/api/studio/register", "third", legacy, nil)
	thirdAccount, _ := f.host.accounts.Get(third.User.ID)
	if thirdAccount.Owner || f.stores[thirdAccount.ID] == f.legacyStore {
		t.Fatal("legacy cookie claimed the root twice")
	}
}

func TestHostServer_LegacyAccessKeyRejectsInvalidAndDisablesAfterClaim(t *testing.T) {
	f := newHostTest(t, true)
	input := `{"username":"invalid-key","password":"long test password","accessKey":"invalid key"}`
	hostResponse(t, hostRequestHTTP(t, f, http.MethodPost, "/api/studio/register", input, f.http.URL, nil), http.StatusUnauthorized)
	if f.host.accounts.Count() != 0 {
		t.Fatal("invalid migration key silently created a fresh workspace")
	}
	session, _ := hostAuth(t, f, "/api/studio/register", "owner", nil, map[string]string{"accessKey": testOwnerToken})
	account, _ := f.host.accounts.Get(session.User.ID)
	if !account.Owner {
		t.Fatal("valid migration key did not claim the legacy workspace")
	}
	input = `{"username":"second-owner","password":"long test password","accessKey":"` + testOwnerToken + `"}`
	hostResponse(t, hostRequestHTTP(t, f, http.MethodPost, "/api/studio/register", input, f.http.URL, nil), http.StatusConflict)
	if f.host.accounts.Count() != 1 {
		t.Fatal("claimed migration key created another owner")
	}
}

func TestHostServer_OwnershipPolicySurvivesRestartBeforeSignup(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "fresh"
		if legacy {
			name = "legacy"
		}
		t.Run(name, func(t *testing.T) {
			f := newHostTest(t, legacy)
			if err := f.host.Close(); err != nil {
				t.Fatal(err)
			}
			f.http.Close()
			var err error
			f.host, err = NewHostServer(HostServerConfig{
				Root: f.root, RegistrationAllowed: true, LegacyWorkspace: !legacy,
				LegacyToken: testOwnerToken, ResolveTenant: f.resolve,
			})
			if err != nil {
				t.Fatal(err)
			}
			f.http = httptest.NewServer(f.host.Handler())
			if f.host.config.LegacyWorkspace != legacy {
				t.Fatal("restart changed first-start workspace ownership policy")
			}
			if session := hostReadSession(t, f, nil); session.SetupRequired == legacy {
				t.Fatalf("restarted session = %+v", session)
			}
			session, _ := hostAuth(t, f, "/api/studio/register", "first-user", nil, nil)
			account, _ := f.host.accounts.Get(session.User.ID)
			if account.Owner == legacy {
				t.Fatalf("anonymous registration after restart = %+v", account)
			}
			info, err := os.Stat(filepath.Join(f.root, "auth", "workspace.json"))
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("ownership policy permissions: %v %v", info, err)
			}
		})
	}
}

func TestHostServer_ConcurrentFirstSignupHasOneOwner(t *testing.T) {
	f := newHostTest(t, false)
	type result struct {
		status int
		body   []byte
	}
	results := make(chan result, 2)
	for _, username := range []string{"alice", "bob"} {
		go func(username string) {
			body, _ := json.Marshal(map[string]string{"username": username, "password": hostTestPassword})
			request, _ := http.NewRequest(http.MethodPost, f.http.URL+"/api/studio/register", bytes.NewReader(body))
			request.Header.Set("Origin", f.http.URL)
			response, err := f.http.Client().Do(request)
			if err != nil {
				results <- result{body: []byte(err.Error())}
				return
			}
			content, _ := io.ReadAll(response.Body)
			response.Body.Close()
			results <- result{status: response.StatusCode, body: content}
		}(username)
	}
	for i := 0; i < 2; i++ {
		result := <-results
		if result.status != http.StatusCreated {
			t.Fatalf("concurrent registration = %d: %s", result.status, result.body)
		}
	}
	owners := 0
	for _, account := range f.host.accounts.List() {
		if account.Owner {
			owners++
		}
	}
	if f.host.accounts.Count() != 2 || owners != 1 {
		t.Fatalf("concurrent signup created %d accounts, %d owners", f.host.accounts.Count(), owners)
	}
}

func TestHostServer_TwoUsersHaveIndependentBotsFilesInstructionsAndSkills(t *testing.T) {
	f := newHostTest(t, false)
	alice, aliceCookie := hostAuth(t, f, "/api/studio/register", "alice", nil, nil)
	bob, bobCookie := hostAuth(t, f, "/api/studio/register", "bob", nil, nil)
	aliceStore, bobStore := f.stores[alice.User.ID], f.stores[bob.User.ID]
	aliceBot, err := aliceStore.CreateBot(Bot{Name: "Alice's private bot"})
	if err != nil {
		t.Fatal(err)
	}
	bobBot := bobStore.ListBots()[0]
	attachment, err := f.servers[alice.User.ID].Workspace().StoreUpload(aliceBot.ID, "private.txt", "text/plain", []byte("ALICE_FILE_CONTENT"))
	if err != nil {
		t.Fatal(err)
	}
	aliceWorkspace, bobWorkspace := f.servers[alice.User.ID].Workspace(), f.servers[bob.User.ID].Workspace()
	if err := aliceWorkspace.SetInstructions("", "ALICE_USER_INSTRUCTIONS"); err != nil {
		t.Fatal(err)
	}
	if err := bobWorkspace.SetInstructions("", "BOB_USER_INSTRUCTIONS"); err != nil {
		t.Fatal(err)
	}
	skill, err := aliceWorkspace.CreateSkill("", "private-skill", "---\nname: private-skill\ndescription: Private test skill\n---\nALICE_SKILL_CONTENT")
	if err != nil {
		t.Fatal(err)
	}
	content := hostResponse(t, hostRequestHTTP(t, f, http.MethodGet, "/api/studio/bots?userId="+alice.User.ID+"&tenant="+alice.User.ID, "", "", bobCookie), http.StatusOK)
	if bytes.Contains(content, []byte(aliceBot.ID)) || bytes.Contains(content, []byte(aliceBot.Name)) || !bytes.Contains(content, []byte(bobBot.ID)) {
		t.Fatalf("tenant selection was influenced by request parameters: %s", content)
	}
	for _, route := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/studio/bots/" + aliceBot.ID, ""},
		{http.MethodPatch, "/api/studio/bots/" + aliceBot.ID, `{"name":"stolen"}`},
		{http.MethodDelete, "/api/studio/bots/" + aliceBot.ID, ""},
		{http.MethodGet, "/api/studio/bots/" + aliceBot.ID + "/events", ""},
		{http.MethodGet, "/api/studio/bots/" + aliceBot.ID + "/instructions", ""},
		{http.MethodPut, "/api/studio/bots/" + aliceBot.ID + "/instructions", `{"content":"stolen"}`},
		{http.MethodGet, "/api/studio/bots/" + aliceBot.ID + "/skills", ""},
		{http.MethodGet, attachment.URL, ""},
	} {
		body := hostResponse(t, hostRequestHTTP(t, f, route.method, route.path, route.body, f.http.URL, bobCookie), http.StatusNotFound)
		if bytes.Contains(body, []byte("ALICE_FILE_CONTENT")) {
			t.Fatal("foreign file content leaked")
		}
	}
	hostResponse(t, hostRequestHTTP(t, f, http.MethodGet, "/api/studio/bots/"+bobBot.ID+"/files/"+attachment.ID, "", "", bobCookie), http.StatusBadRequest)
	content = hostResponse(t, hostRequestHTTP(t, f, http.MethodGet, attachment.URL, "", "", aliceCookie), http.StatusOK)
	if string(content) != "ALICE_FILE_CONTENT" {
		t.Fatalf("owner cannot download its attachment: %s", content)
	}
	content = hostResponse(t, hostRequestHTTP(t, f, http.MethodGet, "/api/studio/user/instructions", "", "", bobCookie), http.StatusOK)
	if !bytes.Contains(content, []byte("BOB_USER_INSTRUCTIONS")) || bytes.Contains(content, []byte("ALICE_USER_INSTRUCTIONS")) {
		t.Fatal("user instructions were shared across accounts")
	}
	content = hostResponse(t, hostRequestHTTP(t, f, http.MethodGet, "/api/studio/user/skills", "", "", bobCookie), http.StatusOK)
	if bytes.Contains(content, []byte("private-skill")) {
		t.Fatal("private global skill appeared in another account")
	}
	content = hostResponse(t, hostRequestHTTP(t, f, http.MethodGet, "/api/studio/skills/content?path="+url.QueryEscape(skill.Path), "", "", bobCookie), http.StatusBadRequest)
	if bytes.Contains(content, []byte("ALICE_SKILL_CONTENT")) {
		t.Fatal("known foreign skill path escaped tenant isolation")
	}
	if bot, err := aliceStore.GetBot(aliceBot.ID); err != nil || bot.Name != aliceBot.Name || bot.Status == "archived" {
		t.Fatalf("foreign requests modified Alice's bot: %+v %v", bot, err)
	}
}

func TestHostServer_StaleBrowserTabCannotReadMutateOrLogoutAnotherAccount(t *testing.T) {
	f := newHostTest(t, false)
	alice, aliceCookie := hostAuth(t, f, "/api/studio/register", "alice", nil, nil)
	bob, bobCookie := hostAuth(t, f, "/api/studio/register", "bob", aliceCookie, nil)
	bobStore, bobWorkspace := f.stores[bob.User.ID], f.servers[bob.User.ID].Workspace()
	if err := bobWorkspace.SetInstructions("", "BOB_ORIGINAL_INSTRUCTIONS"); err != nil {
		t.Fatal(err)
	}
	bobBot := bobStore.ListBots()[0]
	attachment, err := bobWorkspace.StoreUpload(bobBot.ID, "private.txt", "text/plain", []byte("BOB_PRIVATE_FILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ method, path, body, header string }{
		{http.MethodPut, "/api/studio/user/instructions", `{"content":"STALE_ALICE_INSTRUCTIONS"}`, alice.User.ID},
		{http.MethodPost, "/api/studio/bots", `{"name":"Created from old tab"}`, alice.User.ID},
		{http.MethodGet, "/api/studio/bots", "", alice.User.ID},
		{http.MethodGet, "/api/studio/events?expectedAccount=" + alice.User.ID, "", ""},
		{http.MethodGet, attachment.URL + "?expectedAccount=" + alice.User.ID, "", ""},
		{http.MethodGet, "/api/studio/bots", "", ""},
		{http.MethodGet, "/api/studio/events", "", ""},
		{http.MethodPost, "/api/studio/logout", "", alice.User.ID},
		{http.MethodPost, "/api/studio/logout", "", ""},
		{http.MethodPut, "/api/studio/user/instructions?expectedAccount=" + bob.User.ID, `{"content":"query-only mutation"}`, ""},
		{http.MethodGet, "/api/studio/bots?expectedAccount=" + alice.User.ID, "", bob.User.ID},
	} {
		request, err := http.NewRequest(test.method, f.http.URL+test.path, strings.NewReader(test.body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Origin", f.http.URL)
		request.Header.Set(ExpectedAccountHeader, test.header)
		request.AddCookie(bobCookie) // Browser-wide cookie has switched to Bob.
		response, err := f.http.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		if len(response.Cookies()) != 0 {
			t.Fatal("rejected stale logout cleared the new account cookie")
		}
		body := hostResponse(t, response, http.StatusConflict)
		var detail map[string]string
		if err := json.Unmarshal(body, &detail); err != nil || detail["code"] != "account_changed" || bytes.Contains(body, []byte(bob.User.ID)) {
			t.Fatalf("unsafe account-change response = %s: %v", body, err)
		}
	}
	if content, _, err := bobWorkspace.UserInstructions(); err != nil || content != "BOB_ORIGINAL_INSTRUCTIONS" || len(bobStore.ListBots()) != 1 {
		t.Fatalf("stale requests modified Bob's workspace: %q %v", content, err)
	}
	if session := hostReadSession(t, f, bobCookie); !session.Authenticated || session.User.ID != bob.User.ID {
		t.Fatal("stale logout revoked Bob's valid session")
	}
	// Native image/audio/download navigation has no custom header. The account
	// query is an equivalent precondition for reads, and does not select a tenant.
	request, _ := http.NewRequest(http.MethodGet, f.http.URL+attachment.URL+"?expectedAccount="+bob.User.ID, nil)
	request.AddCookie(bobCookie)
	response, err := f.http.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if body := hostResponse(t, response, http.StatusOK); string(body) != "BOB_PRIVATE_FILE" {
		t.Fatalf("bound native file URL = %s", body)
	}
	hostResponse(t, hostRequestHTTP(t, f, http.MethodPost, "/api/studio/logout", "", f.http.URL, bobCookie), http.StatusOK)
	hostResponse(t, hostRequestHTTP(t, f, http.MethodPost, "/api/studio/logout", "", f.http.URL, nil), http.StatusOK)
}

func hostSSE(t *testing.T, f *hostTestFixture, cookie *http.Cookie, after uint64) (*http.Response, *bufio.Reader) {
	t.Helper()
	account, err := f.host.accounts.ResolveSession(cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodGet, f.http.URL+"/api/studio/events?after="+strconv.FormatUint(after, 10)+"&expectedAccount="+account.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(cookie)
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("SSE = %d", response.StatusCode)
	}
	t.Cleanup(func() { response.Body.Close() })
	return response, bufio.NewReader(response.Body)
}

func hostSSEEvent(t *testing.T, reader *bufio.Reader) Event {
	t.Helper()
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(line, "data: ") {
			var event Event
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
				t.Fatal(err)
			}
			return event
		}
	}
}

func TestHostServer_EventStreamsAreIsolatedAndLogoutCancelsExistingReaders(t *testing.T) {
	f := newHostTest(t, false)
	alice, aliceCookie := hostAuth(t, f, "/api/studio/register", "alice", nil, nil)
	bob, bobCookie := hostAuth(t, f, "/api/studio/register", "bob", nil, nil)
	a, err := f.stores[alice.User.ID].AppendEvent("", "", "system", map[string]string{"content": "ALICE_PRIVATE_EVENT"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.stores[bob.User.ID].AppendEvent("", "", "system", map[string]string{"content": "BOB_PRIVATE_EVENT"})
	if err != nil {
		t.Fatal(err)
	}
	aResponse, aReader := hostSSE(t, f, aliceCookie, a.Seq-1)
	_, aSecondReader := hostSSE(t, f, aliceCookie, a.Seq-1)
	_, bReader := hostSSE(t, f, bobCookie, b.Seq-1)
	for _, reader := range []*bufio.Reader{aReader, aSecondReader} {
		if event := hostSSEEvent(t, reader); !bytes.Contains(event.Data, []byte("ALICE_PRIVATE_EVENT")) || bytes.Contains(event.Data, []byte("BOB_PRIVATE_EVENT")) {
			t.Fatalf("Alice replay = %+v", event)
		}
	}
	if event := hostSSEEvent(t, bReader); !bytes.Contains(event.Data, []byte("BOB_PRIVATE_EVENT")) || bytes.Contains(event.Data, []byte("ALICE_PRIVATE_EVENT")) {
		t.Fatalf("Bob replay = %+v", event)
	}
	hostResponse(t, hostRequestHTTP(t, f, http.MethodPost, "/api/studio/logout", "", f.http.URL, aliceCookie), http.StatusOK)
	for _, reader := range []*bufio.Reader{aReader, aSecondReader} {
		if _, err := io.ReadAll(reader); err != nil {
			t.Fatalf("logout did not finish event stream: %v", err)
		}
	}
	aResponse.Body.Close()
	if _, err := f.stores[alice.User.ID].AppendEvent("", "", "system", map[string]string{"content": "ALICE_AFTER_LOGOUT"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.stores[bob.User.ID].AppendEvent("", "", "system", map[string]string{"content": "BOB_STILL_CONNECTED"}); err != nil {
		t.Fatal(err)
	}
	if event := hostSSEEvent(t, bReader); !bytes.Contains(event.Data, []byte("BOB_STILL_CONNECTED")) || bytes.Contains(event.Data, []byte("ALICE_AFTER_LOGOUT")) {
		t.Fatalf("unrelated logout affected Bob's stream: %+v", event)
	}
}

func TestHostServer_EventStreamStopsOnSessionExpiryRevocationOrEviction(t *testing.T) {
	for _, cause := range []string{"expiry", "revocation", "eviction"} {
		t.Run(cause, func(t *testing.T) {
			f := newHostTest(t, false)
			session, cookie := hostAuth(t, f, "/api/studio/register", "alice", nil, nil)
			f.host.sessionsMu.Lock()
			f.host.sessionRecheckInterval = 10 * time.Millisecond
			if cause == "expiry" {
				// Keep validation slower than the deadline to exercise expiry itself.
				f.host.sessionRecheckInterval = time.Minute
			}
			f.host.sessionsMu.Unlock()
			if cause == "expiry" {
				digest, _ := accountSessionDigest(cookie.Value)
				f.host.accounts.mu.Lock()
				record := f.host.accounts.sessions[digest]
				record.ExpiresAt = time.Now().Add(200 * time.Millisecond)
				f.host.accounts.sessions[digest] = record
				_, err := f.host.accounts.saveLocked()
				f.host.accounts.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
			}
			_, reader := hostSSE(t, f, cookie, 0)
			switch cause {
			case "revocation":
				if err := f.host.accounts.RevokeSession(cookie.Value); err != nil {
					t.Fatal(err)
				}
			case "eviction":
				for i := 0; i < accountMaxSessionsPerUser; i++ {
					if _, _, err := f.host.accounts.NewSession(session.User.ID); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := f.host.accounts.ResolveSession(cookie.Value); !errors.Is(err, ErrInvalidCredentials) {
					t.Fatalf("old session was not evicted: %v", err)
				}
			}
			started := time.Now()
			if _, err := io.ReadAll(reader); err != nil {
				t.Fatalf("%s did not terminate the event stream: %v", cause, err)
			}
			if time.Since(started) > 2*time.Second {
				t.Fatalf("%s left event stream alive", cause)
			}
			hostResponse(t, hostRequestHTTP(t, f, http.MethodGet, "/api/studio/bots", "", "", cookie), http.StatusUnauthorized)
		})
	}
}

func TestHostServer_InternalCredentialsSelectTenantBeforeBotIdentity(t *testing.T) {
	firstStore, firstRuntime, firstFactory, firstBot := setupRuntime(t)
	secondStore, secondRuntime, secondFactory, secondBot := setupRuntime(t)
	first, err := NewTenantServer(firstStore, firstRuntime, ServerConfig{InternalToken: "first-internal-test-token"})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := NewTenantServer(secondStore, secondRuntime, ServerConfig{InternalToken: "second-internal-test-token"})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if _, err := firstRuntime.SendMessage(context.Background(), firstBot.ID, MessageRequest{Text: "list"}); err != nil {
		t.Fatal(err)
	}
	firstSend := nextRuntimeSend(t, firstFactory)
	if _, err := secondRuntime.SendMessage(context.Background(), secondBot.ID, MessageRequest{Text: "list"}); err != nil {
		t.Fatal(err)
	}
	secondSend := nextRuntimeSend(t, secondFactory)
	defer firstSend.session.complete("done")
	defer secondSend.session.complete("done")
	host, err := NewHostServer(HostServerConfig{
		Root: t.TempDir(), ResolveTenant: func(context.Context, Account) (*Server, error) { return first, nil },
		ResolveInternalToken: func(token string) (*Server, bool) {
			if sameSecret(token, "first-internal-test-token") {
				return first, true
			}
			if sameSecret(token, "second-internal-test-token") {
				return second, true
			}
			return nil, false
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	for _, test := range []struct {
		method, remote, token, source string
		status                        int
		contains, absent              string
	}{
		{http.MethodPost, "127.0.0.1:3000", "first-internal-test-token", firstBot.ID, http.StatusOK, firstBot.ID, secondBot.ID},
		{http.MethodPost, "127.0.0.1:3000", "second-internal-test-token", secondBot.ID, http.StatusOK, secondBot.ID, firstBot.ID},
		{http.MethodPost, "127.0.0.1:3000", "second-internal-test-token", firstBot.ID, http.StatusNotFound, "", firstBot.WorkDir},
		{http.MethodPost, "192.0.2.10:3000", "first-internal-test-token", firstBot.ID, http.StatusUnauthorized, "", firstBot.ID},
		{http.MethodPost, "127.0.0.1:3000", testOwnerToken, firstBot.ID, http.StatusUnauthorized, "", firstBot.ID},
		{http.MethodGet, "127.0.0.1:3000", "first-internal-test-token", firstBot.ID, http.StatusMethodNotAllowed, "", firstBot.ID},
	} {
		input, _ := json.Marshal(map[string]string{"botId": test.source, "name": "bots_list"})
		request := httptest.NewRequest(test.method, "/api/studio/internal/tools", bytes.NewReader(input))
		request.RemoteAddr = test.remote
		request.Header.Set("Authorization", "Bearer "+test.token)
		recorder := httptest.NewRecorder()
		host.Handler().ServeHTTP(recorder, request)
		if recorder.Code != test.status || (test.contains != "" && !bytes.Contains(recorder.Body.Bytes(), []byte(test.contains))) || (test.absent != "" && bytes.Contains(recorder.Body.Bytes(), []byte(test.absent))) {
			t.Fatalf("internal %s %s source=%s: %d %s", test.method, test.remote, test.source, recorder.Code, recorder.Body.String())
		}
	}
}

func TestHostServer_OriginsAndCredentialValidation(t *testing.T) {
	f := newHostTest(t, false)
	for _, path := range []string{"/api/studio/login", "/api/studio/register", "/api/studio/logout"} {
		for _, origin := range []string{"", "https://attacker.invalid", f.http.URL + "/path"} {
			hostResponse(t, hostRequestHTTP(t, f, http.MethodPost, path, `{}`, origin, nil), http.StatusForbidden)
		}
	}
	for _, body := range []string{
		`{"username":"valid","password":"long password","owner":true}`,
		`{"username":"valid","password":"long password","userId":"arbitrary"}`,
		`{"username":"../outside","password":"long password"}`,
		`{"username":"valid","password":"short"}`,
		`{"username":"valid","password":"long password"}{}`,
		strings.Repeat(" ", 2049) + `{}`,
	} {
		hostResponse(t, hostRequestHTTP(t, f, http.MethodPost, "/api/studio/register", body, f.http.URL, nil), http.StatusBadRequest)
	}
	if f.host.accounts.Count() != 0 {
		t.Fatal("invalid registration created an account")
	}
	session, cookie := hostAuth(t, f, "/api/studio/register", "valid", nil, nil)
	bot := f.stores[session.User.ID].ListBots()[0]
	for _, origin := range []string{"", "https://attacker.invalid"} {
		hostResponse(t, hostRequestHTTP(t, f, http.MethodPatch, "/api/studio/bots/"+bot.ID, `{"name":"forbidden"}`, origin, cookie), http.StatusForbidden)
	}
	request := httptest.NewRequest(http.MethodPost, "http://studio.local/api/studio/login", nil)
	request.Header.Set("Origin", "https://studio.local")
	request.Header.Set("X-Forwarded-Proto", "https")
	request.RemoteAddr = "192.0.2.10:1000"
	if f.host.originAllowed(request) {
		t.Fatal("remote request controlled forwarded scheme")
	}
	request.RemoteAddr = "127.0.0.1:1000"
	if !f.host.originAllowed(request) {
		t.Fatal("local HTTPS proxy scheme was rejected")
	}
	f.host.config.AllowedOrigins = []string{"https://configured-studio.example"}
	request.RemoteAddr = "192.0.2.10:1000"
	request.Header.Set("Origin", "https://configured-studio.example")
	if !f.host.originAllowed(request) {
		t.Fatal("configured dev-server origin was rejected")
	}
}

func TestHostServer_HTTPSLoginCookieIsSecure(t *testing.T) {
	f := newHostTest(t, false)
	hostAuth(t, f, "/api/studio/register", "alice", nil, nil)
	input, _ := json.Marshal(map[string]string{"username": "alice", "password": hostTestPassword})
	request := httptest.NewRequest(http.MethodPost, "https://studio.local/api/studio/login", bytes.NewReader(input))
	request.Header.Set("Origin", "https://studio.local")
	recorder := httptest.NewRecorder()
	f.host.Handler().ServeHTTP(recorder, request)
	response := recorder.Result()
	defer response.Body.Close()
	if cookies := response.Cookies(); recorder.Code != http.StatusOK || len(cookies) != 1 || !cookies[0].Secure {
		t.Fatalf("HTTPS account cookie = %+v: %d", cookies, recorder.Code)
	}
}

func TestHostServer_RateAndConcurrentPasswordWorkAreBounded(t *testing.T) {
	f := newHostTest(t, false)
	for i := 0; i < cap(f.host.kdfSlots); i++ {
		f.host.kdfSlots <- struct{}{}
	}
	request := httptest.NewRequest(http.MethodPost, "http://studio.local/api/studio/login", strings.NewReader(`{"username":"none","password":"long test password"}`))
	request.Header.Set("Origin", "http://studio.local")
	request.RemoteAddr = "192.0.2.100:5000"
	recorder := httptest.NewRecorder()
	f.host.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusTooManyRequests || recorder.Header().Get("Retry-After") == "" {
		t.Fatalf("unbounded KDF work: %d %s", recorder.Code, recorder.Body.String())
	}
	for i := 0; i < cap(f.host.kdfSlots); i++ {
		<-f.host.kdfSlots
	}
	for i := 0; i < 11; i++ {
		request := httptest.NewRequest(http.MethodPost, "http://studio.local/api/studio/login", strings.NewReader(`{"unexpected":"credential"}`))
		request.Header.Set("Origin", "http://studio.local")
		request.RemoteAddr = "192.0.2.200:" + strconv.Itoa(5000+i)
		recorder := httptest.NewRecorder()
		f.host.Handler().ServeHTTP(recorder, request)
		want := http.StatusBadRequest
		if i == 10 {
			want = http.StatusTooManyRequests
		}
		if recorder.Code != want {
			t.Fatalf("attempt %d = %d, want %d", i, recorder.Code, want)
		}
	}
}

func TestHostServer_RegistrationCanBeDisabledAndStaticRoutesRemainPublic(t *testing.T) {
	host, err := NewHostServer(HostServerConfig{
		Root: t.TempDir(), ResolveTenant: func(context.Context, Account) (*Server, error) { return nil, errors.New("unused") },
		StaticFS: fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("account studio")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	for _, test := range []struct {
		method, path, origin string
		want                 int
	}{
		{http.MethodGet, "/", "", http.StatusOK},
		{http.MethodGet, "/api/studio/health", "", http.StatusOK},
		{http.MethodGet, "/api/studio/bots", "", http.StatusUnauthorized},
		{http.MethodPost, "/api/studio/register", "http://studio.local", http.StatusForbidden},
	} {
		request := httptest.NewRequest(test.method, "http://studio.local"+test.path, strings.NewReader(`{}`))
		request.Header.Set("Origin", test.origin)
		recorder := httptest.NewRecorder()
		host.Handler().ServeHTTP(recorder, request)
		if recorder.Code != test.want || recorder.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("route %s = %d: %s", test.path, recorder.Code, recorder.Body.String())
		}
	}
	request := httptest.NewRequest(http.MethodGet, "/api/studio/session", nil)
	recorder := httptest.NewRecorder()
	host.Handler().ServeHTTP(recorder, request)
	var session hostSession
	if err := json.Unmarshal(recorder.Body.Bytes(), &session); err != nil || session.RegistrationAllowed || session.SetupRequired {
		t.Fatalf("disabled registration session = %+v %v", session, err)
	}
}

func TestHostServer_SessionsAndTenantPreloadSurviveRestart(t *testing.T) {
	f := newHostTest(t, false)
	alice, aliceCookie := hostAuth(t, f, "/api/studio/register", "alice", nil, nil)
	bob, bobCookie := hostAuth(t, f, "/api/studio/register", "bob", nil, nil)
	if err := f.host.Close(); err != nil {
		t.Fatal(err)
	}
	f.http.Close()
	loaded := make(map[string]int)
	var err error
	f.host, err = NewHostServer(HostServerConfig{
		Root: f.root, RegistrationAllowed: true, ResolveTenant: func(ctx context.Context, account Account) (*Server, error) {
			loaded[account.ID]++
			return f.resolve(ctx, account)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.host.PreloadTenants(context.Background()); err != nil {
		t.Fatal(err)
	}
	if loaded[alice.User.ID] != 1 || loaded[bob.User.ID] != 1 || len(loaded) != 2 {
		t.Fatalf("persistent tenants were not loaded: %+v", loaded)
	}
	f.http = httptest.NewServer(f.host.Handler())
	for _, cookie := range []*http.Cookie{aliceCookie, bobCookie} {
		if session := hostReadSession(t, f, cookie); !session.Authenticated || session.User == nil {
			t.Fatalf("persisted account session = %+v", session)
		}
	}
	if _, err := f.stores[alice.User.ID].GetBot(f.stores[alice.User.ID].ListBots()[0].ID); err != nil {
		t.Fatal("Host.Close closed resolver-owned tenants")
	}
}

func TestHostServer_WorkspacePolicyRejectsSymlinkAndReleasesAccountLock(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "auth"), 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.json")
	content := []byte(`{"version":1,"legacyWorkspace":false}`)
	if err := os.WriteFile(outside, content, 0600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "auth", "workspace.json")
	if err := os.Symlink(outside, marker); err != nil {
		t.Fatal(err)
	}
	config := HostServerConfig{Root: root, ResolveTenant: func(context.Context, Account) (*Server, error) { return nil, nil }}
	if host, err := NewHostServer(config); err == nil {
		host.Close()
		t.Fatal("symlinked workspace policy was accepted")
	}
	if actual, err := os.ReadFile(outside); err != nil || !bytes.Equal(actual, content) {
		t.Fatal("rejected policy changed a file outside auth root")
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	host, err := NewHostServer(config)
	if err != nil {
		t.Fatalf("failed startup leaked its account lock: %v", err)
	}
	defer host.Close()
}
