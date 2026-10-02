//go:build !windows

package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/bots"
)

func TestConnectBotsMultiUserMainHelper(t *testing.T) {
	if os.Getenv("CONNECT_BOTS_TEST_MULTIUSER_MAIN") != "1" {
		return
	}
	flag.CommandLine = flag.NewFlagSet("connect-bots", flag.ContinueOnError)
	os.Args = []string{"connect-bots", "--data=" + os.Getenv("CONNECT_BOTS_TEST_DATA"),
		"--addr=" + os.Getenv("CONNECT_BOTS_TEST_ADDR"), "--flov-url=",
		"--codex-app-server-url=ws://127.0.0.1:1"}
	if err := run(); err != nil {
		t.Fatal(err)
	}
}

func startMultiUserCommand(t *testing.T, root string) (string, *http.Client, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestConnectBotsMultiUserMainHelper$")
	command.Env = append(os.Environ(), "CONNECT_BOTS_TEST_MULTIUSER_MAIN=1", "CONNECT_BOTS_TEST_DATA="+root, "CONNECT_BOTS_TEST_ADDR="+addr)
	var logs bytes.Buffer
	command.Stdout, command.Stderr = &logs, &logs
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- command.Wait() }()
	exited := false
	t.Cleanup(func() {
		if !exited {
			_ = command.Process.Kill()
			<-stopped
		}
	})
	transport := &http.Transport{Proxy: nil}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	base := "http://" + addr
	deadline := time.Now().Add(10 * time.Second)
	for {
		response, err := client.Get(base + "/api/studio/health")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				break
			}
		}
		select {
		case err := <-stopped:
			exited = true
			t.Fatalf("command exited before readiness: %v\n%s", err, logs.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("multi-user command did not become ready")
		}
		time.Sleep(20 * time.Millisecond)
	}
	stop := func() {
		t.Helper()
		if exited {
			return
		}
		if err := command.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-stopped:
			exited = true
			if err != nil {
				t.Fatalf("graceful multi-user shutdown: %v\n%s", err, logs.String())
			}
		case <-time.After(5 * time.Second):
			t.Fatal("multi-user command did not shut down")
		}
	}
	return base, client, stop
}

// Exercise the command's real host/tenant wiring without model requests. The
// fresh owner and a second account must stay isolated across graceful shutdown,
// including open SSE connections and all three private store locks.
func TestMultiUserCommandPersistsIsolatedWorkspacesAndShutsDown(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data")
	base, client, stop := startMultiUserCommand(t, root)
	identities := make(map[string]string)
	request := func(method, path, body string, cookie *http.Cookie, want int, result any) []*http.Cookie {
		t.Helper()
		req, err := http.NewRequest(method, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", base)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if cookie != nil {
			req.AddCookie(cookie)
			req.Header.Set(bots.ExpectedAccountHeader, identities[cookie.Value])
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != want {
			content, _ := io.ReadAll(response.Body)
			t.Fatalf("%s %s: %s; %s", method, path, response.Status, content)
		}
		if result != nil {
			if err := json.NewDecoder(response.Body).Decode(result); err != nil {
				t.Fatal(err)
			}
		}
		return response.Cookies()
	}
	var alphaSession, betaSession struct {
		Authenticated bool         `json:"authenticated"`
		User          bots.Account `json:"user"`
	}
	alphaCookies := request("POST", "/api/studio/register", `{"username":"alpha","password":"test password alpha"}`, nil, http.StatusCreated, &alphaSession)
	if !alphaSession.Authenticated || len(alphaCookies) != 1 {
		t.Fatal("first account did not receive an authenticated session")
	}
	alphaCookie := alphaCookies[0]
	identities[alphaCookie.Value] = alphaSession.User.ID
	var alphaBot bots.Bot
	request("POST", "/api/studio/bots", `{"name":"Alpha project"}`, alphaCookie, http.StatusCreated, &alphaBot)
	betaCookies := request("POST", "/api/studio/register", `{"username":"beta","password":"test password beta"}`, nil, http.StatusCreated, &betaSession)
	if !betaSession.Authenticated || len(betaCookies) != 1 || alphaSession.User.ID == betaSession.User.ID {
		t.Fatal("second account did not receive its own identity and session")
	}
	betaCookie := betaCookies[0]
	identities[betaCookie.Value] = betaSession.User.ID
	var betaBots struct {
		Bots []bots.Bot `json:"bots"`
	}
	request("GET", "/api/studio/bots", "", betaCookie, http.StatusOK, &betaBots)
	if len(betaBots.Bots) != 1 || betaBots.Bots[0].ID == alphaBot.ID {
		t.Fatalf("second account inherited owner bots: %#v", betaBots.Bots)
	}
	request("GET", "/api/studio/bots/"+alphaBot.ID, "", betaCookie, http.StatusNotFound, nil)
	request("GET", "/api/studio/bots/"+betaBots.Bots[0].ID, "", alphaCookie, http.StatusNotFound, nil)

	streamRequest, err := http.NewRequest("GET", base+"/api/studio/events?expectedAccount="+alphaSession.User.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	streamRequest.AddCookie(alphaCookie)
	stream, err := (&http.Client{Transport: client.Transport}).Do(streamRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	if stream.StatusCode != http.StatusOK {
		t.Fatalf("event stream: %s", stream.Status)
	}
	streamStopped := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, stream.Body)
		close(streamStopped)
	}()
	stop()
	select {
	case <-streamStopped:
	case <-time.After(time.Second):
		t.Fatal("shutdown retained an account event stream")
	}
	accounts, err := bots.OpenAccountStore(root)
	if err != nil {
		t.Fatalf("account lock leaked after shutdown: %v", err)
	}
	defer accounts.Close()
	if accounts.Count() != 2 {
		t.Fatalf("accounts were not persisted: %d", accounts.Count())
	}
	for _, test := range []struct {
		root string
		owns bool
	}{
		{root: root, owns: true},
		{root: filepath.Join(root, "users", betaSession.User.ID)},
	} {
		store, err := bots.OpenStore(test.root)
		if err != nil {
			t.Fatalf("workspace lock leaked after shutdown: %v", err)
		}
		_, findErr := store.GetBot(alphaBot.ID)
		if (findErr == nil) != test.owns {
			t.Errorf("workspace ownership changed during shutdown: %v", findErr)
		}
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	}
}

func TestMultiUserCommandSurvivesCorruptSecondaryWorkspace(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data")
	accounts, err := bots.OpenAccountStore(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = accounts.Close() })
	owner, err := accounts.Register("owner", "test owner password", true)
	if err != nil {
		t.Fatal(err)
	}
	secondary, err := accounts.Register("secondary", "test secondary password", false)
	if err != nil {
		t.Fatal(err)
	}
	ownerToken, _, err := accounts.NewSession(owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	secondaryToken, _, err := accounts.NewSession(secondary.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := accounts.Close(); err != nil {
		t.Fatal(err)
	}
	secondaryRoot := filepath.Join(root, "users", secondary.ID)
	if err := os.MkdirAll(secondaryRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secondaryRoot, "state.json"), []byte("{invalid state"), 0600); err != nil {
		t.Fatal(err)
	}
	base, client, stop := startMultiUserCommand(t, root)
	for _, test := range []struct {
		token   string
		account string
		want    int
	}{
		{ownerToken, owner.ID, http.StatusOK},
		{secondaryToken, secondary.ID, http.StatusServiceUnavailable},
	} {
		req, err := http.NewRequest("GET", base+"/api/studio/bots", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.AddCookie(&http.Cookie{Name: "connect_bots_session", Value: test.token})
		req.Header.Set(bots.ExpectedAccountHeader, test.account)
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != test.want {
			t.Fatalf("workspace request: %s; %s", response.Status, content)
		}
		if test.want == http.StatusServiceUnavailable && strings.Contains(string(content), "invalid state") {
			t.Fatalf("broken workspace exposed private storage errors: %s", content)
		}
	}
	stop()
}
