package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestInternalToolURLMatchesHTTPBind(t *testing.T) {
	for _, test := range []struct {
		addr string
		want string
	}{
		{"127.0.0.1:9830", "http://127.0.0.1:9830"},
		{"127.0.0.2:9830", "http://127.0.0.2:9830"},
		{"[::1]:9830", "http://[::1]:9830"},
		{"[::ffff:127.0.0.1]:9830", "http://127.0.0.1:9830"},
		{"0.0.0.0:9830", "http://127.0.0.1:9830"},
		{"[::]:9830", "http://[::1]:9830"},
		{":9830", "http://127.0.0.1:9830"},
		{"192.0.2.10:9830", ""},
		{"[2001:db8::1]:9830", ""},
		{"bots.internal:9830", ""},
		{"127.0.0.1:0", ""},
		{"127.0.0.1:65536", ""},
		{"127.0.0.1:http", ""},
		{"127.0.0.1", ""},
	} {
		t.Run(test.addr, func(t *testing.T) {
			got, err := internalToolURL(test.addr)
			if test.want == "" {
				if err == nil {
					t.Fatalf("unsupported bind was accepted: %q", got)
				}
				if strings.Contains(test.addr, "192.168.") && !strings.Contains(err.Error(), "--addr 0.0.0.0:9830") {
					t.Fatalf("LAN rejection has no working alternative: %v", err)
				}
			} else if err != nil || got != test.want {
				t.Fatalf("internalToolURL(%q)=%q, %v; want %q", test.addr, got, err, test.want)
			}
		})
	}
}

func TestInternalToolURLReachesLoopbackListener(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "localhost", "::1"} {
		t.Run(host, func(t *testing.T) {
			listener, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
			if err != nil {
				if host == "::1" {
					t.Skipf("host has no IPv6 loopback: %v", err)
				}
				t.Fatal(err)
			}
			server := newHTTPServer(context.Background(), "", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				remote, _, err := net.SplitHostPort(r.RemoteAddr)
				if err != nil || !net.ParseIP(remote).IsLoopback() {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			go server.Serve(listener)
			_, port, err := net.SplitHostPort(listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			endpoint, err := internalToolURL(net.JoinHostPort(host, port))
			if err != nil {
				t.Fatal(err)
			}
			transport := &http.Transport{Proxy: nil}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: time.Second}
			response, err := client.Get(endpoint)
			if err != nil {
				t.Fatalf("internal bridge cannot reach %s bind: %v", host, err)
			}
			response.Body.Close()
			if response.StatusCode != http.StatusNoContent {
				t.Fatalf("internal client did not use loopback: %s", response.Status)
			}
		})
	}
}

func TestShutdownCancelsOpenEventStream(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := newHTTPServer(ctx, "", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, ": connected\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	go server.Serve(listener)
	response, err := http.Get("http://" + listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	cancel()
	deadline, done := context.WithTimeout(context.Background(), time.Second)
	defer done()
	if err := server.Shutdown(deadline); err != nil {
		t.Fatalf("open SSE blocked graceful shutdown: %v", err)
	}
}
