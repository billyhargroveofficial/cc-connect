package bots

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNodePublicURLRequiresExplicitTLSOrNetworkOptIn(t *testing.T) {
	for _, test := range []struct {
		input    string
		insecure bool
		want     string
	}{
		{"", false, ""},
		{" https://bots.example.com/ ", false, "https://bots.example.com"},
		{"https://bots.example.com:8443", false, "https://bots.example.com:8443"},
		{"http://localhost:5173/", false, "http://localhost:5173"},
		{"http://127.0.0.1:9830", false, "http://127.0.0.1:9830"},
		{"http://[::1]:9830", false, "http://[::1]:9830"},
		{"http://192.168.1.15:5173", true, "http://192.168.1.15:5173"},
		{"http://192.168.1.15:5173", false, "invalid"},
		{"http://bots.example.com", false, "invalid"},
		{"https://user:secret@bots.example.com", false, "invalid"},
		{"https://bots.example.com/nodes", false, "invalid"},
		{"https://bots.example.com/?", false, "invalid"},
		{"https://bots.example.com/?node=1", false, "invalid"},
		{"https://bots.example.com/#secret", false, "invalid"},
		{"https://bots.example.com:0", false, "invalid"},
		{"https://bots.example.com:65536", false, "invalid"},
		{"//bots.example.com", false, "invalid"},
		{"wss://bots.example.com", false, "invalid"},
	} {
		t.Run(test.input, func(t *testing.T) {
			got, err := ValidateNodePublicURL(test.input, test.insecure)
			if test.want == "invalid" {
				if err == nil {
					t.Fatalf("unsafe origin accepted: %q", got)
				}
			} else if err != nil || got != test.want {
				t.Fatalf("ValidateNodePublicURL=%q,%v; want %q", got, err, test.want)
			}
		})
	}
}

func TestNodeBindingCannotRerouteMutationsOrConflictWithFileLinks(t *testing.T) {
	for _, test := range []struct {
		method string
		query  string
		header string
		want   string
		valid  bool
	}{
		{http.MethodGet, "", "", LocalNodeID, true},
		{http.MethodPost, "", "", LocalNodeID, true},
		{http.MethodGet, "?node=node_a", "", "node_a", true},
		{http.MethodHead, "?node=node_a", "", "node_a", true},
		{http.MethodGet, "", "node_a", "node_a", true},
		{http.MethodPost, "", "node_a", "node_a", true},
		{http.MethodPost, "?node=node_a", "", "", false},
		{http.MethodPost, "?node=local", "", "", false},
		{http.MethodPut, "?node=node_a", "node_a", "node_a", true},
		{http.MethodGet, "?node=node_a", "node_b", "", false},
		{http.MethodDelete, "?node=node_a", "node_b", "", false},
		{http.MethodGet, "?node=node_a&node=node_b", "", "", false},
		{http.MethodGet, "", " node_a", "", false},
		{http.MethodGet, "", "node/a", "", false},
	} {
		request := httptest.NewRequest(test.method, "/api/studio/bots"+test.query, nil)
		request.Header.Set(ExpectedNodeHeader, test.header)
		if got, valid := expectedNode(request); valid != test.valid || (valid && got != test.want) {
			t.Errorf("%s %s header=%q: %q,%v; want %q,%v", test.method, test.query, test.header, got, valid, test.want, test.valid)
		}
	}
	request := httptest.NewRequest(http.MethodGet, "/api/studio/bots", nil)
	request.Header.Add(ExpectedNodeHeader, "node_a")
	request.Header.Add(ExpectedNodeHeader, "node_a")
	if _, valid := expectedNode(request); valid {
		t.Fatal("ambiguous duplicate node headers accepted")
	}
}

func TestHostServer_NodeAdminAndBinaryDownloadsStayOnHub(t *testing.T) {
	binaries := t.TempDir()
	const binaryName = "connect-bots-node-darwin-arm64"
	if err := os.WriteFile(filepath.Join(binaries, binaryName), []byte("MAC_BINARY_TEST"), 0755); err != nil {
		t.Fatal(err)
	}
	f := newConfiguredHostTest(t, false, func(config *HostServerConfig) {
		config.NodeBinaryDir = binaries
		config.Version = "test-host-version"
	})
	session, cookie := hostAuth(t, f, "/api/studio/register", "alice", nil, nil)
	hostResponse(t, hostRequestHTTP(t, f, http.MethodGet, "/api/studio/nodes", "", "", nil), http.StatusUnauthorized)
	request, _ := http.NewRequest(http.MethodGet, f.http.URL+"/api/studio/nodes?node=unknown_remote", nil)
	request.AddCookie(cookie)
	request.Header.Set(ExpectedAccountHeader, session.User.ID)
	request.Header.Set(ExpectedNodeHeader, "different_remote")
	response, err := f.http.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	content := hostResponse(t, response, http.StatusOK)
	var result struct {
		Nodes []NodeInfo `json:"nodes"`
	}
	if err := json.Unmarshal(content, &result); err != nil || len(result.Nodes) != 1 || result.Nodes[0].ID != LocalNodeID || !result.Nodes[0].Local || !result.Nodes[0].Online || result.Nodes[0].Version != "test-host-version" {
		t.Fatalf("local node list = %s: %v", content, err)
	}
	path := "/api/studio/nodes/binary/darwin/arm64"
	hostResponse(t, hostRequestHTTP(t, f, http.MethodGet, path, "", "", nil), http.StatusUnauthorized)
	response = hostRequestHTTP(t, f, http.MethodGet, path+"?node=unknown_remote", "", "", cookie)
	if response.Header.Get("Content-Type") != "application/octet-stream" || !strings.Contains(response.Header.Get("Content-Disposition"), binaryName) {
		t.Fatalf("download headers = %+v", response.Header)
	}
	if content := hostResponse(t, response, http.StatusOK); string(content) != "MAC_BINARY_TEST" {
		t.Fatalf("download = %q", content)
	}
	hostResponse(t, hostRequestHTTP(t, f, http.MethodGet, "/api/studio/nodes/binary/darwin/windows", "", "", cookie), http.StatusNotFound)
	hostResponse(t, hostRequestHTTP(t, f, http.MethodDelete, "/api/studio/nodes/local", "", f.http.URL, cookie), http.StatusNotFound)
	hostResponse(t, hostRequestHTTP(t, f, http.MethodGet, "/api/studio/nodes/not-a-route?node=unknown_remote", "", "", cookie), http.StatusNotFound)
	if err := os.Symlink(filepath.Join(binaries, binaryName), filepath.Join(binaries, "connect-bots-node-darwin-amd64")); err != nil {
		t.Fatal(err)
	}
	hostResponse(t, hostRequestHTTP(t, f, http.MethodGet, "/api/studio/nodes/binary/darwin/amd64", "", "", cookie), http.StatusNotFound)
}

func TestHostServer_RejectsConflictingNodeBeforeWorkspaceDispatch(t *testing.T) {
	f := newHostTest(t, false)
	session, cookie := hostAuth(t, f, "/api/studio/register", "alice", nil, nil)
	for _, test := range []struct{ method, query, header string }{
		{http.MethodGet, "?node=node_a", "node_b"},
		{http.MethodPost, "?node=node_a", ""},
		{http.MethodPut, "?node=local", ""},
	} {
		request, _ := http.NewRequest(test.method, f.http.URL+"/api/studio/user/instructions"+test.query, strings.NewReader(`{"content":"MUST_NOT_APPLY"}`))
		request.AddCookie(cookie)
		request.Header.Set(ExpectedAccountHeader, session.User.ID)
		request.Header.Set(ExpectedNodeHeader, test.header)
		request.Header.Set("Origin", f.http.URL)
		response, err := f.http.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		content := hostResponse(t, response, http.StatusConflict)
		if !strings.Contains(string(content), `"code":"node_changed"`) {
			t.Fatalf("node conflict response = %s", content)
		}
	}
	hostResponse(t, hostRequestHTTP(t, f, http.MethodGet, "/api/studio/bots?node=unknown_remote", "", "", cookie), http.StatusNotFound)
	content := hostResponse(t, hostRequestHTTP(t, f, http.MethodGet, "/api/studio/user/instructions", "", "", cookie), http.StatusOK)
	if strings.Contains(string(content), "MUST_NOT_APPLY") {
		t.Fatal("rejected query-only mutation changed the local workspace")
	}
}

func TestHostEventStreamClassifierIncludesRemoteSelectorsAndRoutedEvents(t *testing.T) {
	for _, path := range []string{"/api/studio/events?node=node_a", "/api/studio/bots/bot_a/events?node=node_a"} {
		if !isHostEventStream(httptest.NewRequest(http.MethodGet, path, nil)) {
			t.Fatalf("routed stream skipped session revalidation: %s", path)
		}
	}
	for _, test := range []struct{ method, path string }{{http.MethodPost, "/api/studio/events"}, {http.MethodGet, "/api/studio/nodes/connect"}, {http.MethodGet, "/events"}} {
		if isHostEventStream(httptest.NewRequest(test.method, test.path, nil)) {
			t.Fatalf("non-stream classified as event stream: %s %s", test.method, test.path)
		}
	}
}

func newConnectedNodeHostTest(t *testing.T, handler http.Handler) (*hostTestFixture, hostSession, *http.Cookie, NodeEnrollment) {
	t.Helper()
	var nodes *NodeHub
	f := newConfiguredHostTest(t, false, func(config *HostServerConfig) {
		var err error
		nodes, err = OpenNodeHub(NodeHubConfig{Root: config.Root})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := nodes.Close(); err != nil {
				t.Error(err)
			}
		})
		config.Nodes, config.PublicURL = nodes, "https://hub.example.test"
	})
	session, cookie := hostAuth(t, f, "/api/studio/register", "alice", nil, nil)
	content := hostResponse(t, hostRequestHTTP(t, f, http.MethodPost, "/api/studio/nodes/enrollments", `{"name":"Alice Mac"}`, f.http.URL, cookie), http.StatusCreated)
	var enrollment struct {
		NodeEnrollment
		ServerURL string `json:"serverUrl"`
	}
	if err := json.Unmarshal(content, &enrollment); err != nil || enrollment.NodeID == "" || enrollment.Code == "" || enrollment.ServerURL != "https://hub.example.test" {
		t.Fatalf("enrollment response = %s: %v", content, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	metadata := NodeMetadata{Hostname: "Alice-Mac", OS: "darwin", Arch: "arm64", Version: "node-test-version"}
	credential, err := PairNode(ctx, f.http.URL, enrollment.Code, metadata, false)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	connected := make(chan struct{})
	var once sync.Once
	done := make(chan error, 1)
	go func() {
		done <- RunNodeClient(ctx, NodeClientConfig{Credential: credential, Metadata: metadata, OnConnected: func() { once.Do(func() { close(connected) }) }}, handler)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, ErrNodeRevoked) {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("node client did not stop")
		}
	})
	select {
	case <-connected:
	case err := <-done:
		done <- err
		t.Fatalf("node client exited before connect: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("node client did not connect")
	}
	return f, session, cookie, enrollment.NodeEnrollment
}

func TestHostServer_RemoteNodeOwnershipAndWorkspaceRouting(t *testing.T) {
	type observedRequest struct {
		request *http.Request
		body    string
	}
	requests := make(chan observedRequest, 8)
	f, alice, cookie, enrollment := newConnectedNodeHostTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- observedRequest{request: r.Clone(context.Background()), body: string(body)}
		writeJSON(w, http.StatusOK, map[string]string{"content": "REMOTE_WORKSPACE"})
	}))
	bob, bobCookie := hostAuth(t, f, "/api/studio/register", "bob", nil, nil)
	content := hostResponse(t, hostRequestHTTP(t, f, http.MethodGet, "/api/studio/nodes", "", "", cookie), http.StatusOK)
	if !strings.Contains(string(content), enrollment.NodeID) || !strings.Contains(string(content), `"online":true`) || !strings.Contains(string(content), "Alice-Mac") || strings.Contains(string(content), enrollment.Code) {
		t.Fatalf("Alice node listing = %s", content)
	}
	content = hostResponse(t, hostRequestHTTP(t, f, http.MethodGet, "/api/studio/nodes", "", "", bobCookie), http.StatusOK)
	if strings.Contains(string(content), enrollment.NodeID) || strings.Contains(string(content), "Alice-Mac") {
		t.Fatalf("Bob can see Alice node: %s", content)
	}
	path := "/api/studio/user/instructions?node=" + enrollment.NodeID + "&expectedAccount=" + alice.User.ID
	content = hostResponse(t, hostRequestHTTP(t, f, http.MethodGet, path, "", "", cookie), http.StatusOK)
	if !strings.Contains(string(content), "REMOTE_WORKSPACE") {
		t.Fatalf("selected node did not serve workspace request: %s", content)
	}
	remoteRequest := (<-requests).request
	for _, private := range []string{"Cookie", "Authorization", ExpectedAccountHeader, ExpectedNodeHeader} {
		if remoteRequest.Header.Get(private) != "" {
			t.Fatalf("hub forwarded private browser header %s", private)
		}
	}
	if remoteRequest.URL.Query().Get("node") != "" || remoteRequest.URL.Query().Get("expectedAccount") != "" {
		t.Fatalf("hub selectors reached node: %s", remoteRequest.URL)
	}
	mutation, _ := http.NewRequest(http.MethodPut, f.http.URL+"/api/studio/user/instructions", strings.NewReader(`{"content":"REMOTE_MUTATION"}`))
	mutation.AddCookie(cookie)
	mutation.Header.Set(ExpectedAccountHeader, alice.User.ID)
	mutation.Header.Set(ExpectedNodeHeader, enrollment.NodeID)
	mutation.Header.Set("Origin", f.http.URL)
	mutation.Header.Set("Content-Type", "application/json")
	response, err := f.http.Client().Do(mutation)
	if err != nil {
		t.Fatal(err)
	}
	hostResponse(t, response, http.StatusOK)
	observed := <-requests
	if observed.request.Method != http.MethodPut || observed.body != `{"content":"REMOTE_MUTATION"}` {
		t.Fatalf("node mutation changed in transit: method=%s body=%q", observed.request.Method, observed.body)
	}
	hostResponse(t, hostRequestHTTP(t, f, http.MethodGet, "/api/studio/bots?node="+enrollment.NodeID+"&expectedAccount="+bob.User.ID, "", "", bobCookie), http.StatusNotFound)
	hostResponse(t, hostRequestHTTP(t, f, http.MethodDelete, "/api/studio/nodes/"+enrollment.NodeID, "", f.http.URL, bobCookie), http.StatusNotFound)
	hostResponse(t, hostRequestHTTP(t, f, http.MethodGet, "/api/studio/nodes/unknown-route?node="+enrollment.NodeID, "", "", cookie), http.StatusNotFound)
	content = hostResponse(t, hostRequestHTTP(t, f, http.MethodGet, "/api/studio/nodes?node="+enrollment.NodeID, "", "", cookie), http.StatusOK)
	if !strings.Contains(string(content), `"local":true`) {
		t.Fatalf("hub admin was proxied to selected node: %s", content)
	}
	select {
	case request := <-requests:
		t.Fatalf("forbidden or hub-admin request reached node: %s", request.request.URL)
	default:
	}
	content = hostResponse(t, hostRequestHTTP(t, f, http.MethodGet, "/api/studio/user/instructions", "", "", cookie), http.StatusOK)
	if strings.Contains(string(content), "REMOTE_WORKSPACE") || strings.Contains(string(content), "REMOTE_MUTATION") {
		t.Fatal("default local workspace routed to the remote node")
	}
	hostResponse(t, hostRequestHTTP(t, f, http.MethodPost, "/api/studio/nodes/enrollments", `{"name":"Wrong owner","accountId":"`+bob.User.ID+`"}`, f.http.URL, cookie), http.StatusBadRequest)
	hostResponse(t, hostRequestHTTP(t, f, http.MethodPost, "/api/studio/nodes/enrollments", `{"name":"   "}`, f.http.URL, cookie), http.StatusBadRequest)
}

func TestHostServer_RemoteEventStreamStopsOnSessionRevocationOrLogout(t *testing.T) {
	for _, cause := range []string{"revocation", "logout", "eviction"} {
		t.Run(cause, func(t *testing.T) {
			stopped := make(chan struct{})
			f, session, cookie, enrollment := newConnectedNodeHostTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, ": connected\n\n")
				w.(http.Flusher).Flush()
				<-r.Context().Done()
				close(stopped)
			}))
			f.host.sessionsMu.Lock()
			f.host.sessionRecheckInterval = 10 * time.Millisecond
			f.host.sessionsMu.Unlock()
			request, _ := http.NewRequest(http.MethodGet, f.http.URL+"/api/studio/events?node="+enrollment.NodeID+"&expectedAccount="+session.User.ID, nil)
			request.AddCookie(cookie)
			response, err := f.http.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			switch cause {
			case "logout":
				hostResponse(t, hostRequestHTTP(t, f, http.MethodPost, "/api/studio/logout", "", f.http.URL, cookie), http.StatusOK)
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
			}
			select {
			case <-stopped:
			case <-time.After(3 * time.Second):
				t.Fatalf("%s did not cancel the remote event handler", cause)
			}
			readDone := make(chan error, 1)
			go func() { _, err := io.ReadAll(response.Body); readDone <- err }()
			select {
			case err := <-readDone:
				// Revocation may expire a blocked browser write immediately, so a
				// chunked stream can end with unexpected EOF rather than a clean EOF.
				if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
					t.Fatalf("stream shutdown = %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatalf("%s left the browser stream open", cause)
			}
		})
	}
}
