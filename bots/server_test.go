package bots

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

const testOwnerToken = "test-owner-token-never-use-in-production"

func TestTenantServer_NoOwnerTokenOrHostRoutes(t *testing.T) {
	for _, existingToken := range []bool{false, true} {
		name := "absent-token"
		if existingToken {
			name = "invalid-existing-token"
		}
		t.Run(name, func(t *testing.T) {
			store := testStore(t)
			tokenPath := filepath.Join(store.Root(), "token")
			if existingToken {
				if err := os.WriteFile(tokenPath, []byte("invalid"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			workspace := NewWorkspace(store)
			server, err := NewTenantServer(store, nil, ServerConfig{
				Token: testOwnerToken, Workspace: workspace, FlovURL: "http://flov.local/transcribe",
				StaticFS: fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("studio")}},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			if server.Workspace() != workspace || server.Maintenance() == nil || server.config.Token != "" {
				t.Fatal("workspace services were not initialized without owner credentials")
			}
			if got := server.Workspace().settings().FlovURL; got != "http://flov.local/transcribe" {
				t.Fatalf("FlovURL = %q", got)
			}
			info, err := os.Stat(tokenPath)
			if existingToken {
				if err != nil || info.Mode().Perm() != 0644 {
					t.Fatalf("tenant construction accessed owner token: %v %v", info, err)
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("tenant construction created an owner token: %v", err)
			}
			for _, path := range []string{"/api/studio/bots", "/api/studio/user/instructions", "/api/studio/maintenance"} {
				recorder := httptest.NewRecorder()
				server.APIHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
				if recorder.Code != http.StatusOK {
					t.Fatalf("workspace route %s = %d: %s", path, recorder.Code, recorder.Body.String())
				}
			}
			for _, route := range []struct{ method, path string }{
				{http.MethodGet, "/"},
				{http.MethodGet, "/api/studio/session"},
				{http.MethodGet, "/api/studio/health"},
				{http.MethodPost, "/api/studio/login"},
				{http.MethodPost, "/api/studio/logout"},
				{http.MethodPost, "/api/studio/internal/tools"},
			} {
				recorder := httptest.NewRecorder()
				server.Handler().ServeHTTP(recorder, httptest.NewRequest(route.method, route.path, nil))
				if recorder.Code != http.StatusNotFound {
					t.Fatalf("host route mounted in tenant %s %s = %d", route.method, route.path, recorder.Code)
				}
			}
		})
	}
}

func TestTenantServer_APIHandlerUsesItsWorkspace(t *testing.T) {
	firstStore, secondStore := testStore(t), testStore(t)
	first, err := NewTenantServer(firstStore, nil, ServerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := NewTenantServer(secondStore, nil, ServerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	firstBot := firstStore.ListBots()[0]
	for _, route := range []string{
		"/api/studio/bots/" + firstBot.ID,
		"/api/studio/bots/" + firstBot.ID + "/instructions",
		"/api/studio/bots/" + firstBot.ID + "/events",
	} {
		for _, tenant := range []struct {
			server *Server
			want   int
		}{{first, http.StatusOK}, {second, http.StatusNotFound}} {
			recorder := httptest.NewRecorder()
			tenant.server.APIHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, route, nil))
			if recorder.Code != tenant.want {
				t.Fatalf("workspace route %s = %d, want %d", route, recorder.Code, tenant.want)
			}
		}
	}
}

func TestTenantServer_InternalHandlerRetainsAuthenticationAndMethodChecks(t *testing.T) {
	server, err := NewTenantServer(testStore(t), nil, ServerConfig{InternalToken: "test-internal-token"})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	for _, test := range []struct {
		method, remote, token string
		want                  int
	}{
		{http.MethodPost, "192.0.2.10:3000", "test-internal-token", http.StatusUnauthorized},
		{http.MethodPost, "127.0.0.1:3000", testOwnerToken, http.StatusUnauthorized},
		{http.MethodPost, "127.0.0.1:3000", "", http.StatusUnauthorized},
		{http.MethodPost, "127.0.0.1:3000", "test-internal-token", http.StatusServiceUnavailable},
		{http.MethodGet, "127.0.0.1:3000", "test-internal-token", http.StatusMethodNotAllowed},
	} {
		request := httptest.NewRequest(test.method, "/api/studio/internal/tools", strings.NewReader(`{"botId":"x","name":"bots_list"}`))
		request.RemoteAddr = test.remote
		request.Header.Set("Authorization", "Bearer "+test.token)
		recorder := httptest.NewRecorder()
		server.InternalHandler().ServeHTTP(recorder, request)
		if recorder.Code != test.want {
			t.Fatalf("internal %s %s = %d, want %d", test.method, test.remote, recorder.Code, test.want)
		}
	}
}

func testServer(t *testing.T) (*Store, *Server, *httptest.Server) {
	t.Helper()
	store := testStore(t)
	server, err := NewServer(store, nil, ServerConfig{Token: testOwnerToken, InternalToken: "test-internal-token"})
	if err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(server.Handler())
	t.Cleanup(func() { host.Close(); server.Close() })
	return store, server, host
}

func requestHTTP(t *testing.T, host *httptest.Server, method, path, body, origin string, cookie *http.Cookie) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, host.URL+path, strings.NewReader(body))
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
	}
	response, err := host.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func loginCookie(t *testing.T, host *httptest.Server) *http.Cookie {
	t.Helper()
	response := requestHTTP(t, host, http.MethodPost, "/api/studio/login", `{"token":"`+testOwnerToken+`"}`, host.URL, nil)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("login status %d: %s", response.StatusCode, body)
	}
	cookies := response.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies: %+v", cookies)
	}
	return cookies[0]
}

func TestServer_AuthenticationAndOriginChecks(t *testing.T) {
	store, _, host := testServer(t)
	response := requestHTTP(t, host, http.MethodGet, "/api/studio/session", "", "", nil)
	var session map[string]bool
	json.NewDecoder(response.Body).Decode(&session)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || session["authenticated"] {
		t.Fatalf("anonymous session: %+v", session)
	}
	response = requestHTTP(t, host, http.MethodGet, "/api/studio/bots", "", "", nil)
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous bots: %d", response.StatusCode)
	}
	response = requestHTTP(t, host, http.MethodPost, "/api/studio/login", `{"token":"bad"}`, host.URL, nil)
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized || bytes.Contains(body, []byte(testOwnerToken)) {
		t.Fatalf("bad login: %d %s", response.StatusCode, body)
	}
	cookie := loginCookie(t, host)
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/api/studio" {
		t.Fatalf("insecure cookie: %+v", cookie)
	}
	response = requestHTTP(t, host, http.MethodGet, "/api/studio/bots", "", "", cookie)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("authenticated bots: %d", response.StatusCode)
	}
	bot := store.ListBots()[0]
	for _, origin := range []string{"", "https://attacker.invalid"} {
		response = requestHTTP(t, host, http.MethodPatch, "/api/studio/bots/"+bot.ID, `{"name":"forbidden"}`, origin, cookie)
		response.Body.Close()
		if response.StatusCode != http.StatusForbidden {
			t.Fatalf("origin %q status: %d", origin, response.StatusCode)
		}
	}
	response = requestHTTP(t, host, http.MethodPatch, "/api/studio/bots/"+bot.ID, `{"name":"Renamed"}`, host.URL, cookie)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("valid mutation: %d", response.StatusCode)
	}
	got, _ := store.GetBot(bot.ID)
	if got.Name != "Renamed" {
		t.Fatalf("mutation not stored: %+v", got)
	}
	forged := *cookie
	forged.Value = cookie.Value[:len(cookie.Value)-1] + "!"
	response = requestHTTP(t, host, http.MethodGet, "/api/studio/bots", "", "", &forged)
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("forged cookie status: %d", response.StatusCode)
	}
}

func TestServer_LocalTokenFileNeverReturnedByAPI(t *testing.T) {
	t.Setenv("CONNECT_BOTS_TOKEN", "")
	store := testStore(t)
	server, err := NewServer(store, nil, ServerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	info, err := os.Stat(filepath.Join(store.Root(), "token"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("token permissions: %v %v", info, err)
	}
	token, err := os.ReadFile(filepath.Join(store.Root(), "token"))
	if err != nil || len(strings.TrimSpace(string(token))) < 32 {
		t.Fatal("missing generated owner token")
	}
	for _, path := range []string{"/api/studio/session", "/api/studio/health", "/api/studio/bots", "/api/studio/token"} {
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if bytes.Contains(recorder.Body.Bytes(), bytes.TrimSpace(token)) {
			t.Fatalf("token leaked from %s", path)
		}
	}
}

func TestServer_LoginRateLimitAndForwardedHeaders(t *testing.T) {
	_, server, _ := testServer(t)
	for attempt := 0; attempt < 11; attempt++ {
		request := httptest.NewRequest(http.MethodPost, "http://owner.local/api/studio/login", strings.NewReader(`{"token":"invalid"}`))
		request.RemoteAddr = "192.0.2.50:40000"
		request.Header.Set("Origin", "http://owner.local")
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)
		want := http.StatusUnauthorized
		if attempt == 10 {
			want = http.StatusTooManyRequests
		}
		if recorder.Code != want {
			t.Fatalf("attempt %d = %d, want %d", attempt, recorder.Code, want)
		}
	}
	request := httptest.NewRequest(http.MethodPost, "http://owner.local/api/studio/login", nil)
	request.RemoteAddr = "192.0.2.1:1000"
	request.Header.Set("X-Forwarded-Proto", "https")
	request.Header.Set("Origin", "https://owner.local")
	if server.originAllowed(request) {
		t.Fatal("remote peer controlled forwarded Origin protocol")
	}
	request.RemoteAddr = "127.0.0.1:1000"
	if !server.originAllowed(request) {
		t.Fatal("local TLS reverse proxy was not recognized")
	}
}

func TestServer_CRUDAndMalformedRequests(t *testing.T) {
	_, _, host := testServer(t)
	cookie := loginCookie(t, host)
	response := requestHTTP(t, host, http.MethodPost, "/api/studio/bots", `{"name":"Second","backend":"pi"}`, host.URL, cookie)
	var bot Bot
	json.NewDecoder(response.Body).Decode(&bot)
	response.Body.Close()
	if response.StatusCode != http.StatusCreated || bot.ID == "" || bot.Backend != "pi" {
		t.Fatalf("create: %d %+v", response.StatusCode, bot)
	}
	for _, body := range []string{`{"name":"evil","workDir":"/tmp/outside"}`, `{"name":"one"}{"name":"two"}`, `{"name":""}`} {
		response = requestHTTP(t, host, http.MethodPost, "/api/studio/bots", body, host.URL, cookie)
		response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("body %s status = %d", body, response.StatusCode)
		}
	}
	response = requestHTTP(t, host, http.MethodGet, "/api/studio/bots/"+bot.ID+"/events?after=-1", "", "", cookie)
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid cursor status = %d", response.StatusCode)
	}
	response = requestHTTP(t, host, http.MethodDelete, "/api/studio/bots/"+bot.ID, "", host.URL, cookie)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("archive status = %d", response.StatusCode)
	}
	response = requestHTTP(t, host, http.MethodGet, "/api/studio/bots/"+bot.ID+"/events", "", "", cookie)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("archived history status = %d", response.StatusCode)
	}
}

func TestServer_SSEIndependentBrowsersReplayFromCursor(t *testing.T) {
	store, _, host := testServer(t)
	cookie := loginCookie(t, host)
	first, _ := store.AppendEvent("", "", "system", map[string]string{"content": "first"})
	second, _ := store.AppendEvent("", "", "system", map[string]string{"content": "second"})
	client := &http.Client{Timeout: 5 * time.Second}
	connect := func(after string, last string) (*http.Response, *bufio.Reader) {
		request, _ := http.NewRequest(http.MethodGet, host.URL+"/api/studio/events?after="+after, nil)
		request.AddCookie(cookie)
		if last != "" {
			request.Header.Set("Last-Event-ID", last)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("SSE status %d", response.StatusCode)
		}
		return response, bufio.NewReader(response.Body)
	}
	readEvent := func(reader *bufio.Reader) Event {
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
	r1, reader1 := connect("1", "")
	defer r1.Body.Close()
	r2, reader2 := connect("0", "1")
	defer r2.Body.Close()
	for _, reader := range []*bufio.Reader{reader1, reader2} {
		if event := readEvent(reader); event.Seq != first.Seq {
			t.Fatalf("replay first = %+v", event)
		}
		if event := readEvent(reader); event.Seq != second.Seq {
			t.Fatalf("replay second = %+v", event)
		}
	}
	third, _ := store.AppendEvent("", "", "system", map[string]string{"content": "third"})
	for _, reader := range []*bufio.Reader{reader1, reader2} {
		if event := readEvent(reader); event.Seq != third.Seq {
			t.Fatalf("live = %+v", event)
		}
	}
	r1.Body.Close()
	r3, reader3 := connect("2", strconv.FormatUint(third.Seq, 10))
	defer r3.Body.Close()
	fourth, _ := store.AppendEvent("", "", "system", map[string]string{"content": "fourth"})
	if event := readEvent(reader3); event.Seq != fourth.Seq {
		t.Fatalf("reconnect repeated a delivered event: %+v", event)
	}
}

func TestServer_InternalToolsRequireLoopbackAndSeparateToken(t *testing.T) {
	_, server, _ := testServer(t)
	for _, remote := range []string{"192.0.2.10:3000", "127.0.0.1:3000"} {
		request := httptest.NewRequest(http.MethodPost, "http://owner.local/api/studio/internal/tools", strings.NewReader(`{"botId":"x","name":"bots_list"}`))
		request.RemoteAddr = remote
		request.Header.Set("Authorization", "Bearer "+testOwnerToken)
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("owner token granted internal tools from %s: %d", remote, recorder.Code)
		}
	}
	request := httptest.NewRequest(http.MethodPost, "http://owner.local/api/studio/internal/tools", strings.NewReader(`{"botId":"x","name":"bots_list"}`))
	request.RemoteAddr = "127.0.0.1:3000"
	request.Header.Set("Authorization", "Bearer test-internal-token")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("valid internal auth did not reach runtime: %d", recorder.Code)
	}
}

func TestServer_SessionSurvivesRestartAndTokenRotationRevokesIt(t *testing.T) {
	store, first, host := testServer(t)
	cookie := loginCookie(t, host)
	first.Close()
	restarted, err := NewServer(store, nil, ServerConfig{Token: testOwnerToken})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	request := httptest.NewRequest(http.MethodGet, "http://owner.local/api/studio/session", nil)
	request.AddCookie(cookie)
	if !restarted.authenticated(request) {
		t.Fatal("persisted owner credential did not preserve browser session")
	}
	rotated, err := NewServer(store, nil, ServerConfig{Token: testOwnerToken + "-rotated"})
	if err != nil {
		t.Fatal(err)
	}
	defer rotated.Close()
	if rotated.authenticated(request) {
		t.Fatal("old browser session survived owner credential rotation")
	}
}

func TestServer_GoalSnapshotPreservesReadBaseline(t *testing.T) {
	response := map[string]any{"goal": map[string]any{"objective": "finish"}, "cursor": uint64(4)}
	snapshot, err := goalSnapshot(response, 9)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot["cursor"] != uint64(4) {
		t.Fatal("a later event cursor hid updates newer than the goal snapshot")
	}
	snapshot["cursor"] = uint64(10)
	if response["cursor"] != uint64(4) {
		t.Fatal("HTTP encoding mutated the native response")
	}
	nullGoal, err := goalSnapshot(map[string]any{"goal": nil}, 6)
	if err != nil || nullGoal["cursor"] != uint64(6) || nullGoal["goal"] != nil {
		t.Fatalf("null goal responsechanged: %+v %v", nullGoal, err)
	}
}
