package bots

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNodeHub_InvalidAttemptsBehindProxyDoNotBlockValidEnrollment(t *testing.T) {
	hub := openTestNodeHub(t)
	enrollment, err := hub.CreateEnrollment(nodeTestAccountA, "My Mac")
	if err != nil {
		t.Fatal(err)
	}
	invalidCode, err := nodeSecret(24)
	if err != nil {
		t.Fatal(err)
	}
	request := func(code string) *httptest.ResponseRecorder {
		body, err := json.Marshal(map[string]any{"code": code, "metadata": NodeMetadata{}})
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, "/api/studio/nodes/enroll", bytes.NewReader(body))
		r.RemoteAddr = "127.0.0.1:45555" // All callers behind the local TLS proxy.
		w := httptest.NewRecorder()
		hub.EnrollHTTP(w, r)
		return w
	}
	for index := range 60 {
		if response := request(invalidCode); response.Code != http.StatusUnauthorized {
			t.Fatalf("invalid pairing attempt %d: HTTP %d", index+1, response.Code)
		}
	}
	if response := request(invalidCode); response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") == "" {
		t.Fatalf("anonymous pairing quota was not enforced: HTTP %d", response.Code)
	}
	response := request(enrollment.Code)
	if response.Code != http.StatusOK {
		t.Fatalf("valid code was denied by anonymous proxy quota: HTTP %d", response.Code)
	}
	var credential NodeCredential
	if err := json.Unmarshal(response.Body.Bytes(), &credential); err != nil || credential.NodeID != enrollment.NodeID || !validNodeSecret(credential.Token, 32) {
		t.Fatal("valid enrollment did not return its node credential")
	}
	if !hub.nodeAuthenticated(credential.NodeID, nodeDigest(credential.Token)) {
		t.Fatal("valid enrollment was not persisted after exhausting anonymous quota")
	}
}

func TestNodeHub_InvalidAttemptsBehindProxyDoNotBlockValidNodeCredential(t *testing.T) {
	hub := openTestNodeHub(t)
	server := nodeTestServer(t, hub)
	enrollment, err := hub.CreateEnrollment(nodeTestAccountA, "My Mac")
	if err != nil {
		t.Fatal(err)
	}
	credential, err := hub.enroll(enrollment.Code, NodeMetadata{})
	if err != nil {
		t.Fatal(err)
	}
	credential.ServerURL = server.URL
	invalidToken, err := nodeSecret(32)
	if err != nil {
		t.Fatal(err)
	}
	request := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/studio/nodes/connect", nil)
		r.RemoteAddr = "127.0.0.1:45555"
		r.Header.Set(nodeIDHeader, credential.NodeID)
		r.Header.Set("Authorization", "Bearer "+invalidToken)
		w := httptest.NewRecorder()
		hub.ConnectHTTP(w, r)
		return w
	}
	for index := range 60 {
		if response := request(); response.Code != http.StatusUnauthorized {
			t.Fatalf("invalid node attempt %d: HTTP %d", index+1, response.Code)
		}
	}
	if response := request(); response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") == "" {
		t.Fatalf("anonymous node quota was not enforced: HTTP %d", response.Code)
	}
	startTestNodeClient(t, credential, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))
	response := httptest.NewRecorder()
	hub.Proxy(nodeTestAccountA, credential.NodeID, response, httptest.NewRequest(http.MethodGet, "/api/studio/bots", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("valid node credential was denied by anonymous proxy quota: HTTP %d", response.Code)
	}
}
