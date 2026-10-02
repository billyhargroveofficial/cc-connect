package bots

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var (
	nodeTestAccountA = Account{ID: "user_11111111111111111111111111111111", Username: "alpha"}
	nodeTestAccountB = Account{ID: "user_22222222222222222222222222222222", Username: "beta"}
)

func openTestNodeHub(t *testing.T) *NodeHub {
	t.Helper()
	hub, err := OpenNodeHub(NodeHubConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hub.Close() })
	return hub
}

func nodeTestServer(t *testing.T, hub *NodeHub) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/studio/nodes/enroll", hub.EnrollHTTP)
	mux.HandleFunc("GET /api/studio/nodes/connect", hub.ConnectHTTP)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func nodeTestCredential(t *testing.T, hub *NodeHub, serverURL string, account Account) NodeCredential {
	t.Helper()
	enrollment, err := hub.CreateEnrollment(account, "My Mac")
	if err != nil {
		t.Fatal(err)
	}
	credential, err := PairNode(context.Background(), serverURL, enrollment.Code, NodeMetadata{Hostname: "test-mac", OS: "darwin", Arch: "arm64", Version: "test"}, false)
	if err != nil {
		t.Fatal(err)
	}
	return credential
}

func startTestNodeClient(t *testing.T, credential NodeCredential, handler http.Handler) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	connected := make(chan struct{}, 2)
	result := make(chan error, 1)
	go func() {
		result <- RunNodeClient(ctx, NodeClientConfig{
			Credential: credential, Metadata: NodeMetadata{Hostname: "test-mac", OS: "darwin", Arch: "arm64", Version: "test"},
			OnConnected: func() {
				select {
				case connected <- struct{}{}:
				default:
				}
			},
		}, handler)
	}()
	t.Cleanup(cancel)
	select {
	case <-connected:
	case err := <-result:
		cancel()
		t.Fatalf("node stopped before connecting: %v", err)
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("node did not connect")
	}
	return cancel, result
}

func TestNodeHub_EnrollmentIsScopedOneTimeAndPersistedAsDigests(t *testing.T) {
	hub := openTestNodeHub(t)
	server := nodeTestServer(t, hub)
	first, err := hub.CreateEnrollment(nodeTestAccountA, " MacBook ")
	if err != nil {
		t.Fatal(err)
	}
	if first.ExpiresAt.Sub(hub.now()) < 9*time.Minute || first.ExpiresAt.Sub(hub.now()) > NodeEnrollmentLifetime {
		t.Fatal("pairing lifetime is not ten minutes")
	}
	if nodes := hub.List(nodeTestAccountB); len(nodes) != 0 {
		t.Fatalf("another account can list pending nodes: %+v", nodes)
	}
	if err := hub.Remove(nodeTestAccountB, first.NodeID); !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("another account can remove a pending node: %v", err)
	}
	nodes := hub.List(nodeTestAccountA)
	if len(nodes) != 1 || nodes[0].Status != "pending" || nodes[0].Name != "MacBook" || nodes[0].Local {
		t.Fatalf("unexpected pending node: %+v", nodes)
	}
	content, err := os.ReadFile(hub.path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(content, []byte(first.Code)) || !bytes.Contains(content, []byte(nodeDigest(first.Code))) {
		t.Fatal("pairing code was stored in plaintext or its digest is missing")
	}
	credential, err := PairNode(context.Background(), server.URL, first.Code, NodeMetadata{OS: "darwin", Arch: "arm64"}, false)
	if err != nil || credential.NodeID != first.NodeID || credential.ServerURL != server.URL {
		t.Fatalf("pairing failed: %v", err)
	}
	if _, err := PairNode(context.Background(), server.URL, first.Code, NodeMetadata{}, false); !errors.Is(err, ErrNodeEnrollmentInvalid) {
		t.Fatalf("pairing code was reusable: %v", err)
	}
	content, err = os.ReadFile(hub.path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(content, []byte(credential.Token)) || bytes.Contains(content, []byte(first.Code)) || !bytes.Contains(content, []byte(nodeDigest(credential.Token))) {
		t.Fatal("a raw credential leaked to the persisted registry")
	}
	if info, err := os.Stat(hub.path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("node registry is not private: %v", err)
	}
	if err := hub.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenNodeHub(hub.config)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	nodes = reopened.List(nodeTestAccountA)
	if len(nodes) != 1 || nodes[0].ID != credential.NodeID || nodes[0].Status != "offline" || nodes[0].OS != "darwin" {
		t.Fatalf("paired node was not restored offline: %+v", nodes)
	}
	if !reopened.nodeAuthenticated(credential.NodeID, nodeDigest(credential.Token)) {
		t.Fatal("restored credential no longer authenticates")
	}
}

func TestNodeHub_ExpiredEnrollmentCannotPair(t *testing.T) {
	hub := openTestNodeHub(t)
	now := time.Now()
	hub.now = func() time.Time { return now }
	enrollment, err := hub.CreateEnrollment(nodeTestAccountA, "Expired Mac")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(NodeEnrollmentLifetime)
	if _, err := hub.enroll(enrollment.Code, NodeMetadata{}); !errors.Is(err, ErrNodeEnrollmentInvalid) {
		t.Fatalf("expired pairing granted access: %v", err)
	}
	if nodes := hub.List(nodeTestAccountA); len(nodes) != 0 {
		t.Fatalf("expired pending enrollment remains visible: %+v", nodes)
	}
}

func TestNodeHub_PersistenceFailureDoesNotBurnEnrollmentOrRevokeCredential(t *testing.T) {
	hub := openTestNodeHub(t)
	enrollment, err := hub.CreateEnrollment(nodeTestAccountA, "Mac")
	if err != nil {
		t.Fatal(err)
	}
	write := hub.write
	hub.write = func(string, []byte) error { return fmt.Errorf("simulated disk failure") }
	if _, err := hub.enroll(enrollment.Code, NodeMetadata{}); err == nil {
		t.Fatal("pairing succeeded without persisting its credential")
	}
	hub.write = write
	credential, err := hub.enroll(enrollment.Code, NodeMetadata{})
	if err != nil {
		t.Fatalf("failed write burned the enrollment: %v", err)
	}
	hub.write = func(string, []byte) error { return fmt.Errorf("simulated disk failure") }
	if err := hub.Remove(nodeTestAccountA, credential.NodeID); err == nil {
		t.Fatal("revoke succeeded without a persistent write")
	}
	if !hub.nodeAuthenticated(credential.NodeID, nodeDigest(credential.Token)) {
		t.Fatal("failed removal invalidated the credential in memory")
	}
	hub.write = write
}

func TestNodeHub_RegistryRejectsSymlinkAndSecondWriter(t *testing.T) {
	hub := openTestNodeHub(t)
	if second, err := OpenNodeHub(hub.config); err == nil {
		second.Close()
		t.Fatal("second registry writer acquired the same state")
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "auth"), 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte(`{"version":1,"nodes":[],"enrollments":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "auth", "nodes.json")); err != nil {
		t.Fatal(err)
	}
	if symlinkHub, err := OpenNodeHub(NodeHubConfig{Root: root}); err == nil {
		symlinkHub.Close()
		t.Fatal("registry accepted a symlink state file")
	}
}

func TestNodeHub_ProxyIsScopedAndStripsHostCredentials(t *testing.T) {
	hub := openTestNodeHub(t)
	server := nodeTestServer(t, hub)
	credential := nodeTestCredential(t, hub, server.URL, nodeTestAccountA)
	var calls atomic.Int32
	body := strings.Repeat("upload", 30_000)
	_, result := startTestNodeClient(t, credential, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		for _, header := range []string{"Authorization", "Cookie", "Origin", "Referer", "Forwarded", "X-Forwarded-For", ExpectedAccountHeader, "X-Connect-Bots-Node", nodeIDHeader} {
			if r.Header.Get(header) != "" {
				t.Errorf("host credential/header reached node: %s", header)
			}
		}
		if r.URL.Query().Get("expectedAccount") != "" || r.URL.Query().Get("node") != "" || r.URL.Query().Get("after") != "5" {
			t.Errorf("node selector leaked or workspace query disappeared: %s", r.URL.RawQuery)
		}
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/octet-stream" {
			t.Errorf("request metadata changed: %s, %s", r.Method, r.Header.Get("Content-Type"))
		}
		data, err := io.ReadAll(r.Body)
		if err != nil || string(data) != body {
			t.Errorf("chunked request did not round-trip: %v (%d bytes)", err, len(data))
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="result.txt"`)
		w.Header().Set("Set-Cookie", "must-not-reach-browser")
		w.Header().Set("X-Forwarded-For", "must-not-reach-browser")
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, body)
	}))
	_ = result
	request := httptest.NewRequest(http.MethodPost, "/api/studio/bots/bot_test/uploads?node="+credential.NodeID+"&expectedAccount="+nodeTestAccountA.ID+"&after=5", strings.NewReader(body))
	for _, header := range []string{"Authorization", "Cookie", "Origin", "Referer", "Forwarded", "X-Forwarded-For", ExpectedAccountHeader, "X-Connect-Bots-Node", nodeIDHeader} {
		request.Header.Set(header, "secret")
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	denied := httptest.NewRecorder()
	hub.Proxy(nodeTestAccountB, credential.NodeID, denied, request)
	if denied.Code != http.StatusNotFound || calls.Load() != 0 {
		t.Fatalf("cross-account request reached remote handler: %d, calls=%d", denied.Code, calls.Load())
	}
	response := httptest.NewRecorder()
	hub.Proxy(nodeTestAccountA, credential.NodeID, response, request)
	if response.Code != http.StatusCreated || response.Body.String() != body || calls.Load() != 1 {
		t.Fatalf("proxy failed: status=%d calls=%d size=%d", response.Code, calls.Load(), response.Body.Len())
	}
	if response.Header().Get("Set-Cookie") != "" || response.Header().Get("X-Forwarded-For") != "" || response.Header().Get("Content-Disposition") == "" {
		t.Fatalf("unsafe headers or lost download header: %+v", response.Header())
	}
	nodes := hub.List(nodeTestAccountA)
	if len(nodes) != 1 || !nodes[0].Online || nodes[0].Status != "online" || nodes[0].Hostname != "test-mac" {
		t.Fatalf("connected device is not online: %+v", nodes)
	}
}

func TestNodeHub_ProxyStreamsSSEAndPropagatesBrowserCancellation(t *testing.T) {
	hub := openTestNodeHub(t)
	server := nodeTestServer(t, hub)
	credential := nodeTestCredential(t, hub, server.URL, nodeTestAccountA)
	canceled := make(chan struct{})
	startTestNodeClient(t, credential, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: progress\ndata: first\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(canceled)
	}))
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hub.Proxy(nodeTestAccountA, credential.NodeID, w, r)
	}))
	defer proxy.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, proxy.URL+"/api/studio/events?after=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if line, err := bufio.NewReader(response.Body).ReadString('\n'); err != nil || line != "event: progress\n" {
		t.Fatalf("SSE was buffered instead of streaming: %q %v", line, err)
	}
	cancel()
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("browser cancellation did not reach the device handler")
	}
}

func TestNodeHub_RevokeCancelsRequestAndStopsReconnectWithoutReplay(t *testing.T) {
	hub := openTestNodeHub(t)
	server := nodeTestServer(t, hub)
	credential := nodeTestCredential(t, hub, server.URL, nodeTestAccountA)
	started, canceled := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	_, result := startTestNodeClient(t, credential, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		close(started)
		<-r.Context().Done()
		close(canceled)
	}))
	proxyDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response := httptest.NewRecorder()
		hub.Proxy(nodeTestAccountA, credential.NodeID, response, httptest.NewRequest(http.MethodPost, "/api/studio/bots/bot_test/messages", strings.NewReader(`{"text":"one mutation"}`)))
		proxyDone <- response
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("mutation never reached the node")
	}
	if err := hub.Remove(nodeTestAccountB, credential.NodeID); !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("other account could revoke a live device: %v", err)
	}
	if err := hub.Remove(nodeTestAccountA, credential.NodeID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("revocation did not cancel the active device handler")
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrNodeRevoked) {
			t.Fatalf("revoked device kept reconnecting: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("revoked device did not stop")
	}
	select {
	case response := <-proxyDone:
		if response.Code != http.StatusBadGateway {
			t.Fatalf("incomplete mutation response did not report disconnect: %d", response.Code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("browser request hung after revocation")
	}
	if calls.Load() != 1 || len(hub.List(nodeTestAccountA)) != 0 {
		t.Fatal("mutation was retried or revoked node remains visible")
	}
}

func TestNodeHub_DisconnectCancelsActiveRequestAndReportsOffline(t *testing.T) {
	hub := openTestNodeHub(t)
	server := nodeTestServer(t, hub)
	credential := nodeTestCredential(t, hub, server.URL, nodeTestAccountA)
	started, canceled := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	cancelClient, result := startTestNodeClient(t, credential, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		close(started)
		<-r.Context().Done()
		close(canceled)
	}))
	proxyDone := make(chan struct{})
	go func() {
		hub.Proxy(nodeTestAccountA, credential.NodeID, httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/studio/bots/bot_test/stop", nil))
		close(proxyDone)
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not start")
	}
	cancelClient()
	for _, channel := range []<-chan struct{}{canceled, proxyDone} {
		select {
		case <-channel:
		case <-time.After(3 * time.Second):
			t.Fatal("disconnect left an active request")
		}
	}
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("client cancellation did not stop reconnect: %v", err)
	}
	response := httptest.NewRecorder()
	hub.Proxy(nodeTestAccountA, credential.NodeID, response, httptest.NewRequest(http.MethodGet, "/api/studio/bots", nil))
	if response.Code != http.StatusServiceUnavailable || calls.Load() != 1 {
		t.Fatalf("offline node did not fail cleanly: %d calls=%d", response.Code, calls.Load())
	}
}

func TestNodeTunnel_RejectsHostRoutesAndUnsafeURLs(t *testing.T) {
	for _, uri := range []string{
		"/api/studio/login", "/api/studio/session", "/api/studio/register", "/api/studio/logout", "/api/studio/internal/tools", "/api/studio/nodes", "/api/studio/nodes/enroll", "/",
		"/api/studio/bots/../../internal/tools", "/api/studio/bots/%2e%2e/../internal/tools", "/api/studio/bots/\\internal", "https://elsewhere.example/api/studio/bots", "/api/studio/bots//id",
	} {
		parsed, err := url.ParseRequestURI(uri)
		if err == nil {
			if _, err := nodeWorkspaceURI(parsed); err == nil {
				t.Errorf("tunnel accepted unsafe URI: %s", uri)
			}
		}
	}
	for _, uri := range []string{"/api/studio/bots", "/api/studio/bots/bot_test/files/file_test", "/api/studio/events?after=7", "/api/studio/user/instructions", "/api/studio/skills/content", "/api/studio/transcribe", "/api/studio/maintenance/run"} {
		parsed, _ := url.ParseRequestURI(uri)
		if _, err := nodeWorkspaceURI(parsed); err != nil {
			t.Errorf("tunnel rejected workspace URI %s: %v", uri, err)
		}
	}
}

func TestNodeTLS_InsecureTransportMustBeExplicitOnBothSides(t *testing.T) {
	for _, raw := range []string{"http://192.168.1.15:9830", "http://example.com", "ftp://example.com", "https://user:pass@example.com", "https://example.com/api", "https://example.com/?token=secret"} {
		if _, err := nodeServerURL(raw, false); err == nil {
			t.Errorf("accepted unsafe node server URL: %s", raw)
		}
	}
	for _, raw := range []string{"http://127.0.0.1:9830", "http://[::1]:9830", "http://localhost:9830", "https://example.com"} {
		if _, err := nodeServerURL(raw, false); err != nil {
			t.Errorf("rejected secure/loopback node server URL: %s %v", raw, err)
		}
	}
	if _, err := nodeServerURL("http://192.168.1.15:9830", true); err != nil {
		t.Fatal(err)
	}
	hub := openTestNodeHub(t)
	enrollment, err := hub.CreateEnrollment(nodeTestAccountA, "LAN Mac")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"code": enrollment.Code, "metadata": NodeMetadata{}})
	request := httptest.NewRequest(http.MethodPost, "/api/studio/nodes/enroll", bytes.NewReader(body))
	request.RemoteAddr = "192.168.1.99:45555"
	request.Header.Set("X-Forwarded-Proto", "https") // A remote peer cannot pretend TLS.
	response := httptest.NewRecorder()
	hub.EnrollHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("remote insecure pairing was accepted: %d", response.Code)
	}
	hub.config.AllowInsecure = true
	request = httptest.NewRequest(http.MethodPost, "/api/studio/nodes/enroll", bytes.NewReader(body))
	request.RemoteAddr = "192.168.1.99:45555"
	response = httptest.NewRecorder()
	hub.EnrollHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("explicit LAN pairing was rejected: %d %s", response.Code, response.Body.String())
	}
}

func TestNodeHub_ProxyRejectsOversizedBodiesAndHeadersBeforeDispatch(t *testing.T) {
	hub := openTestNodeHub(t)
	server := nodeTestServer(t, hub)
	credential := nodeTestCredential(t, hub, server.URL, nodeTestAccountA)
	var calls atomic.Int32
	startTestNodeClient(t, credential, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
	}))
	request := httptest.NewRequest(http.MethodPost, "/api/studio/bots/bot_test/uploads", nil)
	request.ContentLength = nodeMaxRequestBytes + 1
	response := httptest.NewRecorder()
	hub.Proxy(nodeTestAccountA, credential.NodeID, response, request)
	if response.Code != http.StatusRequestEntityTooLarge || calls.Load() != 0 {
		t.Fatal("oversized request reached the handler")
	}
	request = httptest.NewRequest(http.MethodGet, "/api/studio/bots", nil)
	request.Header.Set("Accept", strings.Repeat("x", 17<<10))
	response = httptest.NewRecorder()
	hub.Proxy(nodeTestAccountA, credential.NodeID, response, request)
	if response.Code != http.StatusRequestHeaderFieldsTooLarge || calls.Load() != 0 {
		t.Fatal("oversized headers reached the handler")
	}
}

func TestNodeClient_CancelDrainsWorkspaceHandlersBeforeReturning(t *testing.T) {
	hub := openTestNodeHub(t)
	server := nodeTestServer(t, hub)
	credential := nodeTestCredential(t, hub, server.URL, nodeTestAccountA)
	started, cancelReceived, allowFinish := make(chan struct{}), make(chan struct{}), make(chan struct{})
	cancelClient, result := startTestNodeClient(t, credential, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(cancelReceived)
		<-allowFinish
	}))
	proxyDone := make(chan struct{})
	go func() {
		hub.Proxy(nodeTestAccountA, credential.NodeID, httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/studio/bots", nil))
		close(proxyDone)
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		close(allowFinish)
		t.Fatal("workspace handler did not start")
	}
	cancelClient()
	select {
	case <-cancelReceived:
	case <-time.After(3 * time.Second):
		close(allowFinish)
		t.Fatal("workspace handler was not canceled")
	}
	select {
	case err := <-result:
		close(allowFinish)
		t.Fatalf("node returned while a workspace handler was still running: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(allowFinish)
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("node failed to return after the handler drained")
	}
	select {
	case <-proxyDone:
	case <-time.After(3 * time.Second):
		t.Fatal("proxy hung after cancellation")
	}
}

func TestNodeHub_InFlightLimitBoundsLongLivedStreams(t *testing.T) {
	hub := openTestNodeHub(t)
	server := nodeTestServer(t, hub)
	credential := nodeTestCredential(t, hub, server.URL, nodeTestAccountA)
	started := make(chan struct{}, nodeMaxInflight)
	var calls atomic.Int32
	startTestNodeClient(t, credential, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		started <- struct{}{}
		<-r.Context().Done()
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{}, nodeMaxInflight)
	for range nodeMaxInflight {
		go func() {
			request := httptest.NewRequest(http.MethodGet, "/api/studio/events", nil).WithContext(ctx)
			hub.Proxy(nodeTestAccountA, credential.NodeID, httptest.NewRecorder(), request)
			done <- struct{}{}
		}()
	}
	for range nodeMaxInflight {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("allowed stream did not start")
		}
	}
	response := httptest.NewRecorder()
	hub.Proxy(nodeTestAccountA, credential.NodeID, response, httptest.NewRequest(http.MethodGet, "/api/studio/bots", nil))
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "node_busy") || calls.Load() != nodeMaxInflight {
		t.Fatalf("in-flight limit did not protect the device: status=%d calls=%d", response.Code, calls.Load())
	}
	cancel()
	for range nodeMaxInflight {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("long-lived request did not cancel")
		}
	}
}

type slowNodeRecorder struct {
	*httptest.ResponseRecorder
}

func (w slowNodeRecorder) Write(data []byte) (int, error) {
	time.Sleep(2 * time.Millisecond)
	return w.ResponseRecorder.Write(data)
}

func TestNodeHub_LargeDownloadBackpressuresSlowBrowserWithoutTruncation(t *testing.T) {
	hub := openTestNodeHub(t)
	server := nodeTestServer(t, hub)
	credential := nodeTestCredential(t, hub, server.URL, nodeTestAccountA)
	data := bytes.Repeat([]byte("download content"), 400_000)
	startTestNodeClient(t, credential, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", fmt.Sprint(len(data)))
		w.Write(data)
	}))
	response := httptest.NewRecorder()
	hub.Proxy(nodeTestAccountA, credential.NodeID, slowNodeRecorder{response}, httptest.NewRequest(http.MethodGet, "/api/studio/bots/bot_test/files/file_test", nil))
	if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), data) {
		t.Fatalf("slow browser download was truncated: status=%d got=%d want=%d", response.Code, response.Body.Len(), len(data))
	}
	if nodes := hub.List(nodeTestAccountA); len(nodes) != 1 || !nodes[0].Online {
		t.Fatal("slow browser download disconnected the node")
	}
}

func TestNodeHub_HandlerPanicAbortsCommittedHTTPResponse(t *testing.T) {
	hub := openTestNodeHub(t)
	server := nodeTestServer(t, hub)
	credential := nodeTestCredential(t, hub, server.URL, nodeTestAccountA)
	startTestNodeClient(t, credential, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "partial response")
		w.(http.Flusher).Flush()
		panic("simulated handler failure")
	}))
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hub.Proxy(nodeTestAccountA, credential.NodeID, w, r)
	}))
	defer proxy.Close()
	response, err := (&http.Client{Timeout: 5 * time.Second}).Get(proxy.URL + "/api/studio/bots")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err == nil {
		t.Fatalf("handler panic became a clean success: status=%d body=%q", response.StatusCode, data)
	}
	if response.StatusCode != http.StatusOK || string(data) != "partial response" {
		t.Fatalf("expected committed partial response before abort, got status=%d body=%q", response.StatusCode, data)
	}
}

func TestNodeHub_MidStreamDisconnectAbortsHTTPInsteadOfCleanEOF(t *testing.T) {
	hub := openTestNodeHub(t)
	server := nodeTestServer(t, hub)
	credential := nodeTestCredential(t, hub, server.URL, nodeTestAccountA)
	canceled := make(chan struct{})
	var calls atomic.Int32
	cancelClient, _ := startTestNodeClient(t, credential, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: partial\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(canceled)
	}))
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hub.Proxy(nodeTestAccountA, credential.NodeID, w, r)
	}))
	defer proxy.Close()
	response, err := (&http.Client{Timeout: 5 * time.Second}).Get(proxy.URL + "/api/studio/events")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	if line, err := reader.ReadString('\n'); err != nil || line != "data: partial\n" {
		t.Fatalf("first streamed line did not arrive: %q %v", line, err)
	}
	connection, err := hub.connection(nodeTestAccountA, credential.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	connection.shutdown()
	if _, err := io.ReadAll(reader); err == nil {
		t.Fatal("mid-stream node disconnect became clean HTTP EOF")
	}
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("disconnect did not cancel the workspace request")
	}
	cancelClient()
	if calls.Load() != 1 {
		t.Fatal("interrupted request was replayed")
	}
}

type blockedNodeBrowserWriter struct {
	headers http.Header
	started chan struct{}
	release chan struct{}
}

func (w *blockedNodeBrowserWriter) Header() http.Header { return w.headers }
func (w *blockedNodeBrowserWriter) WriteHeader(int)     {}
func (w *blockedNodeBrowserWriter) Write(data []byte) (int, error) {
	close(w.started)
	<-w.release
	return len(data), nil
}

func TestNodeHub_RequestCancellationStopsNodeWhileBrowserWriterBlocked(t *testing.T) {
	hub := openTestNodeHub(t)
	server := nodeTestServer(t, hub)
	credential := nodeTestCredential(t, hub, server.URL, nodeTestAccountA)
	canceled := make(chan struct{})
	startTestNodeClient(t, credential, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "working")
		<-r.Context().Done()
		close(canceled)
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writer := &blockedNodeBrowserWriter{headers: make(http.Header), started: make(chan struct{}), release: make(chan struct{})}
	proxyDone := make(chan struct{})
	go func() {
		hub.Proxy(nodeTestAccountA, credential.NodeID, writer, httptest.NewRequest(http.MethodGet, "/api/studio/bots", nil).WithContext(ctx))
		close(proxyDone)
	}()
	select {
	case <-writer.started:
	case <-time.After(3 * time.Second):
		close(writer.release)
		t.Fatal("browser write did not block")
	}
	cancel()
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		close(writer.release)
		t.Fatal("blocked browser writer prevented node cancellation")
	}
	select {
	case <-proxyDone:
		close(writer.release)
		t.Fatal("test browser writer was not actually blocking")
	default:
	}
	close(writer.release)
	select {
	case <-proxyDone:
	case <-time.After(3 * time.Second):
		t.Fatal("proxy failed to return after the browser writer unblocked")
	}
}

type nodeDeadlineRecorder struct {
	*httptest.ResponseRecorder
	mu       sync.Mutex
	deadline time.Time
}

func (w *nodeDeadlineRecorder) SetWriteDeadline(deadline time.Time) error {
	w.mu.Lock()
	w.deadline = deadline
	w.mu.Unlock()
	return nil
}

func (w *nodeDeadlineRecorder) currentDeadline() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.deadline
}

func TestNodeProxyDeadline_CancelCannotBeOverwrittenAndSuccessResetsDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	writer := &nodeDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	deadline := nodeProxyDeadline{controller: http.NewResponseController(writer), ctx: ctx}
	if !deadline.prepare() || !writer.currentDeadline().After(time.Now()) {
		t.Fatal("proxy did not bound the browser write")
	}
	cancel()
	deadline.cancel()
	if deadline.prepare() {
		t.Fatal("write refresh ignored cancellation")
	}
	deadline.reset()
	if last := writer.currentDeadline(); last.IsZero() || last.After(time.Now()) {
		t.Fatal("write refresh/reset undid the canceled deadline")
	}
	writer = &nodeDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	deadline = nodeProxyDeadline{controller: http.NewResponseController(writer), ctx: context.Background()}
	deadline.prepare()
	deadline.reset()
	if !writer.currentDeadline().IsZero() {
		t.Fatal("normal completion left a write deadline on the keep-alive connection")
	}
	deadline.cancel() // A previously scheduled callback may arrive after reset.
	if !writer.currentDeadline().IsZero() || deadline.prepare() {
		t.Fatal("late cancellation changed the deadline after the handler returned")
	}
}
