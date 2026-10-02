// Connect Bots is an additive entry point. It leaves cc-connect's legacy CLI,
// configured projects, and platform services untouched.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	_ "github.com/chenhg5/cc-connect/agent/codex"
	_ "github.com/chenhg5/cc-connect/agent/pi"
	"github.com/chenhg5/cc-connect/bots"
	_ "github.com/chenhg5/cc-connect/platform/telegram"
	"github.com/chenhg5/cc-connect/studio"
)

var version = "dev"

func main() {
	configureLogging()
	if err := run(); err != nil {
		slog.Error("Connect Bots stopped", "error", err)
		os.Exit(1)
	}
}

func run() (runErr error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home: %w", err)
	}
	defaultData := filepath.Join(home, ".local", "share", "connect-bots")
	if stateHome := os.Getenv("XDG_DATA_HOME"); stateHome != "" {
		defaultData = filepath.Join(stateHome, "connect-bots")
	}
	addr := flag.String("addr", "127.0.0.1:9830", "HTTP listening address")
	data := flag.String("data", defaultData, "private persistent bot workspace")
	flov := flag.String("flov-url", "http://127.0.0.1:17432/v1/audio/transcriptions", "Flov transcription endpoint")
	assets := flag.String("assets", "", "serve a frontend build directory instead of embedded assets")
	origins := flag.String("origins", "", "additional allowed browser origins, comma separated")
	registration := flag.Bool("registration", true, "allow new account registration")
	publicURL := flag.String("public-url", "", "externally reachable HTTP(S) origin for remote host enrollment")
	allowInsecureNodes := flag.Bool("allow-insecure-nodes", false, "allow remote hosts over plain HTTP on a trusted network")
	nodeBinaries := flag.String("node-binaries", "", "directory containing downloadable macOS node binaries")
	codexEndpoint := flag.String("codex-app-server-url", "", "explicit dedicated Codex endpoint; default starts a private app-server")
	httpsAddr := flag.String("https-addr", "", "optional HTTPS listening address (browser microphone on LAN)")
	tlsCert := flag.String("tls-cert", "", "HTTPS certificate chain file")
	tlsKey := flag.String("tls-key", "", "HTTPS private key file")
	showVersion := flag.Bool("version", false, "print version")
	flag.Parse()
	if *showVersion {
		fmt.Println("Connect Bots " + version)
		return nil
	}
	if flag.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q; remote hosts use the connect-bots-node executable", flag.Arg(0))
	}
	nodePublicURL, err := bots.ValidateNodePublicURL(*publicURL, *allowInsecureNodes)
	if err != nil {
		return err
	}
	internalURL, err := internalToolURL(*addr)
	if err != nil {
		return err
	}
	if *httpsAddr != "" && (*tlsCert == "" || *tlsKey == "") {
		return fmt.Errorf("https-addr requires tls-cert and tls-key")
	}
	if *httpsAddr == "" && (*tlsCert != "" || *tlsKey != "") {
		return fmt.Errorf("tls-cert and tls-key require https-addr")
	}
	dataRoot, err := filepath.Abs(*data)
	if err != nil {
		return fmt.Errorf("resolve workspace root: %w", err)
	}
	legacyWorkspace, err := hasLegacyWorkspace(dataRoot)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	codexURL := strings.TrimSpace(*codexEndpoint)
	var ownedCodex *bots.CodexAppServer
	var codexDone <-chan struct{}
	if codexURL == "" {
		// Keep the server alive until every account runtime has interrupted its
		// owned work and detached its adapters. HTTP cancellation runs first.
		var stopCodex context.CancelFunc
		ownedCodex, stopCodex, err = startOwnedCodexAppServer(ctx, bots.CodexAppServerConfig{DataDir: dataRoot, StartupTimeout: 120 * time.Second})
		if err != nil {
			return err
		}
		defer func() {
			runErr = errors.Join(runErr, ownedCodex.Close())
			stopCodex()
		}()
		codexURL, codexDone = ownedCodex.URL(), ownedCodex.Done()
	} else {
		codexURL, err = bots.ValidateCodexAppServerURL(codexURL)
		if err != nil {
			return fmt.Errorf("dedicated Codex endpoint: %w", err)
		}
	}
	config := bots.ServerConfig{
		StaticDir: *assets, FlovURL: *flov,
	}
	if *assets == "" {
		config.StaticFS = studio.Assets()
	}
	for _, origin := range strings.Split(*origins, ",") {
		if origin = strings.TrimSpace(origin); origin != "" {
			config.AllowedOrigins = append(config.AllowedOrigins, origin)
		}
	}
	tenants, err := NewTenantManager(TenantManagerConfig{
		Context: ctx, DataRoot: dataRoot, CodexURL: codexURL,
		InternalURL: internalURL, FlovURL: *flov, Server: config,
	})
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, tenants.Close()) }()
	nodes, err := bots.OpenNodeHub(bots.NodeHubConfig{Root: dataRoot, AllowInsecure: *allowInsecureNodes})
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, nodes.Close()) }()
	server, err := bots.NewHostServer(bots.HostServerConfig{
		Root: dataRoot, StaticDir: config.StaticDir, StaticFS: config.StaticFS,
		AllowedOrigins: config.AllowedOrigins, RegistrationAllowed: *registration,
		LegacyWorkspace: legacyWorkspace,
		ResolveTenant: func(requestCtx context.Context, account bots.Account) (*bots.Server, error) {
			if err := requestCtx.Err(); err != nil {
				return nil, err
			}
			return tenants.Resolve(account)
		},
		ResolveInternalToken: tenants.ResolveInternalToken,
		Nodes:                nodes, PublicURL: nodePublicURL, NodeBinaryDir: *nodeBinaries, Version: version,
	})
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, server.Close()) }()
	preloadErr := server.PreloadTenants(ctx)
	if ctx.Err() != nil {
		return nil
	}
	if preloadErr != nil {
		// A damaged account workspace must not take other users offline. Its
		// requests can retry resolution and receive the host's generic error.
		slog.Warn("Connect Bots could not restore every account workspace", "error", preloadErr)
	}
	httpServer := newHTTPServer(ctx, *addr, server.Handler())
	stopped := make(chan error, 2)
	go func() { stopped <- httpServer.ListenAndServe() }()
	var tlsServer *http.Server
	if *httpsAddr != "" {
		tlsServer = newHTTPServer(ctx, *httpsAddr, server.Handler())
		go func() { stopped <- tlsServer.ListenAndServeTLS(*tlsCert, *tlsKey) }()
		slog.Info("Connect Bots HTTPS listening", "addr", *httpsAddr)
	}
	slog.Info("Connect Bots listening", "addr", *addr, "workspace", dataRoot, "accounts_file", filepath.Join(dataRoot, "auth", "accounts.json"))
	var serveErr error
	select {
	case err := <-stopped:
		if err != nil && err != http.ErrServerClosed {
			serveErr = fmt.Errorf("serve: %w", err)
		}
	case <-ctx.Done():
	case <-codexDone:
		serveErr = ownedCodex.Err()
		if serveErr == nil {
			serveErr = errors.New("dedicated Codex app-server exited unexpectedly")
		}
	}
	// Cancel every streaming request before waiting for active HTTP handlers.
	// A browser's SSE connection otherwise keeps Shutdown active indefinitely.
	cancel()
	shutdownCtx, done := context.WithTimeout(context.Background(), 15*time.Second)
	defer done()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		serveErr = errors.Join(serveErr, fmt.Errorf("HTTP shutdown: %w", err))
		if err := httpServer.Close(); err != nil {
			serveErr = errors.Join(serveErr, fmt.Errorf("HTTP close: %w", err))
		}
	}
	if tlsServer != nil {
		if err := tlsServer.Shutdown(shutdownCtx); err != nil {
			serveErr = errors.Join(serveErr, fmt.Errorf("HTTPS shutdown: %w", err))
			if err := tlsServer.Close(); err != nil {
				serveErr = errors.Join(serveErr, fmt.Errorf("HTTPS close: %w", err))
			}
		}
	}
	return serveErr
}

// Check before starting Codex or opening a store: both initialize directories,
// and OpenStore creates state.json even on a brand-new installation. A legacy
// workspace must be claimed through its existing owner credential.
func hasLegacyWorkspace(root string) (bool, error) {
	_, err := os.Stat(filepath.Join(root, "state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect existing workspace: %w", err)
	}
	return true, nil
}

// Pi tools reach the same HTTP listener over loopback. A specific LAN bind
// would expose the UI while making this authenticated local bridge unavailable.
func internalToolURL(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	portNumber, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || portNumber < 1 || portNumber > 65535 {
		return "", fmt.Errorf("addr must contain a TCP port from 1 to 65535")
	}
	if host == "" {
		host = "0.0.0.0"
	} else if strings.EqualFold(host, "localhost") {
		// Resolve with the same tcp address preference as ListenAndServe so a
		// localhost bind and the tool client use the same address family.
		resolved, err := net.ResolveTCPAddr("tcp", addr)
		if err != nil {
			return "", fmt.Errorf("resolve loopback addr: %w", err)
		}
		host = resolved.IP.String()
	}
	ip := net.ParseIP(host)
	if ip == nil || (!ip.IsLoopback() && !ip.IsUnspecified()) {
		return "", fmt.Errorf("addr must bind a wildcard or loopback address; use --addr 0.0.0.0:%s for LAN access", port)
	}
	if ip.IsUnspecified() {
		if ip.To4() != nil {
			host = "127.0.0.1"
		} else {
			host = "::1"
		}
	} else {
		host = ip.String()
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(portNumber)), nil
}

// A signal must cancel a warming child without binding the ready child's
// lifetime to HTTP cancellation. After readiness, run closes native sessions
// before explicitly stopping the owned app-server.
func startOwnedCodexAppServer(startupCtx context.Context, config bots.CodexAppServerConfig) (*bots.CodexAppServer, context.CancelFunc, error) {
	codexCtx, stopCodex := context.WithCancel(context.Background())
	stopStartupBridge := context.AfterFunc(startupCtx, stopCodex)
	server, err := bots.StartCodexAppServer(codexCtx, config)
	stopStartupBridge()
	if err != nil {
		stopCodex()
		return nil, nil, err
	}
	return server, stopCodex, nil
}

func newHTTPServer(ctx context.Context, addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr: addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout: 90 * time.Second, MaxHeaderBytes: 1 << 20,
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
}
