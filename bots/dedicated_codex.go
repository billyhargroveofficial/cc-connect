package bots

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// CodexAppServerConfig keeps runtime state separate while CODEX_HOME continues
// to supply the owner's native authentication, instructions, and skills.
type CodexAppServerConfig struct {
	DataDir         string
	Command         string
	CodexHome       string
	StartupTimeout  time.Duration
	ShutdownTimeout time.Duration
}

// CodexAppServer owns exactly one dedicated child. It never manages Codex's
// shared daemon or attaches to its implicit control socket.
type CodexAppServer struct {
	endpoint        string
	runtimeDir      string
	cmd             *exec.Cmd
	log             *os.File
	shutdownTimeout time.Duration
	done            chan struct{}
	mu              sync.Mutex
	waitErr         error
	closeOnce       sync.Once
	closeErr        error
}

// ValidateCodexAppServerURL requires an explicit dedicated endpoint. In
// particular, unix:// without a path would select Codex's shared control socket.
func ValidateCodexAppServerURL(raw string) (string, error) {
	endpoint := strings.TrimSpace(raw)
	if strings.HasPrefix(endpoint, "unix://") {
		path := strings.TrimPrefix(endpoint, "unix://")
		if !filepath.IsAbs(path) || strings.ContainsRune(path, '\x00') {
			return "", fmt.Errorf("Codex app-server URL requires an absolute Unix socket path")
		}
		return "unix://" + filepath.Clean(path), nil
	}
	parsed, err := url.Parse(endpoint)
	if err == nil && (parsed.Scheme == "ws" || parsed.Scheme == "wss") && parsed.Hostname() != "" && parsed.User == nil && parsed.Fragment == "" {
		return endpoint, nil
	}
	return "", fmt.Errorf("Codex app-server requires an explicit unix:///path, ws://host:port, or wss://host endpoint")
}

func StartCodexAppServer(ctx context.Context, cfg CodexAppServerConfig) (*CodexAppServer, error) {
	if ctx == nil {
		return nil, fmt.Errorf("dedicated Codex app-server requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.DataDir) == "" {
		return nil, fmt.Errorf("dedicated Codex app-server requires a data directory")
	}
	dataDir, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("dedicated Codex app-server data directory: %w", err)
	}
	stateDir := filepath.Join(dataDir, "codex", "state")
	logDir := filepath.Join(dataDir, "codex", "log")
	for _, path := range []string{stateDir, logDir} {
		if err := os.MkdirAll(path, 0700); err != nil {
			return nil, fmt.Errorf("dedicated Codex app-server private directory: %w", err)
		}
	}
	runtimeDir, endpoint, err := codexAppServerEndpoint(dataDir)
	if err != nil {
		return nil, err
	}
	log, err := os.OpenFile(filepath.Join(logDir, "app-server.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		_ = os.RemoveAll(runtimeDir)
		return nil, fmt.Errorf("dedicated Codex app-server log: %w", err)
	}
	command := strings.TrimSpace(cfg.Command)
	if command == "" {
		command = "codex"
	}
	cmd := exec.Command(command, "app-server", "--listen", endpoint,
		"-c", "sqlite_home="+strconv.Quote(stateDir), "-c", "log_dir="+strconv.Quote(logDir))
	cmd.Dir = dataDir
	cmd.Stdout, cmd.Stderr = log, log
	if cfg.CodexHome != "" {
		cmd.Env = codexAppServerEnv(os.Environ(), "CODEX_HOME", cfg.CodexHome)
	}
	configureCodexAppServerProcess(cmd)
	if err := cmd.Start(); err != nil {
		_ = log.Close()
		_ = os.RemoveAll(runtimeDir)
		return nil, fmt.Errorf("start dedicated Codex app-server: %w", err)
	}
	if cfg.StartupTimeout <= 0 {
		// A private SQLite directory needs to index native rollouts on its first
		// start. Codex can spend more than 30 seconds behind that startup gate.
		cfg.StartupTimeout = 120 * time.Second
	}
	if cfg.ShutdownTimeout <= 0 {
		cfg.ShutdownTimeout = 5 * time.Second
	}
	server := &CodexAppServer{
		endpoint: endpoint, runtimeDir: runtimeDir, cmd: cmd, log: log,
		shutdownTimeout: cfg.ShutdownTimeout, done: make(chan struct{}),
	}
	go func() {
		err := cmd.Wait()
		server.mu.Lock()
		server.waitErr = err
		server.mu.Unlock()
		close(server.done)
	}()
	readyCtx, cancel := context.WithTimeout(ctx, cfg.StartupTimeout)
	err = server.waitReady(readyCtx)
	cancel()
	if err != nil {
		closeErr := server.Close()
		return nil, errors.Join(fmt.Errorf("dedicated Codex app-server startup: %w", err), closeErr)
	}
	go func() {
		select {
		case <-ctx.Done():
			_ = server.Close()
		case <-server.done:
		}
	}()
	return server, nil
}

func codexAppServerEnv(env []string, key, value string) []string {
	prefix := key + "="
	result := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}

func (s *CodexAppServer) URL() string           { return s.endpoint }
func (s *CodexAppServer) Done() <-chan struct{} { return s.done }

// Err reports the child's exit result after Done closes.
func (s *CodexAppServer) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.waitErr
}

// Close gives only our process group a bounded graceful shutdown, then kills
// that same group if needed. The caller should close bot sessions first so
// their goals and native processes can settle before the app-server exits.
func (s *CodexAppServer) Close() error {
	s.closeOnce.Do(func() {
		select {
		case <-s.done:
		default:
			if err := signalCodexAppServerProcess(s.cmd, false); err != nil {
				s.closeErr = errors.Join(s.closeErr, err)
			}
			timer := time.NewTimer(s.shutdownTimeout)
			select {
			case <-s.done:
			case <-timer.C:
				if err := signalCodexAppServerProcess(s.cmd, true); err != nil {
					s.closeErr = errors.Join(s.closeErr, err)
				}
				select {
				case <-s.done:
				case <-time.After(2 * time.Second):
					s.closeErr = errors.Join(s.closeErr, fmt.Errorf("dedicated Codex app-server did not exit after force stop"))
				}
			}
			timer.Stop()
		}
		s.closeErr = errors.Join(s.closeErr, s.log.Close(), os.RemoveAll(s.runtimeDir))
	})
	return s.closeErr
}

func (s *CodexAppServer) waitReady(ctx context.Context) error {
	var lastErr error
	for {
		select {
		case <-s.done:
			if err := s.Err(); err != nil {
				return fmt.Errorf("process exited before readiness: %w", err)
			}
			return fmt.Errorf("process exited before readiness")
		case <-ctx.Done():
			return errors.Join(ctx.Err(), lastErr)
		default:
		}
		// Leave a warming server time to finish accepting and initializing one
		// connection instead of repeatedly abandoning queued handshakes.
		attemptCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		lastErr = probeCodexAppServer(attemptCtx, s.endpoint)
		cancel()
		if lastErr == nil {
			return nil
		}
		retry := time.NewTimer(250 * time.Millisecond)
		select {
		case <-retry.C:
		case <-s.done:
		case <-ctx.Done():
		}
		retry.Stop()
	}
}

// Readiness initializes a connection but creates no thread or model request.
func probeCodexAppServer(ctx context.Context, endpoint string) error {
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	address := endpoint
	if strings.HasPrefix(endpoint, "unix://") {
		socket := strings.TrimPrefix(endpoint, "unix://")
		address = "ws://localhost/"
		dialer.NetDialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		}
	}
	conn, _, err := dialer.DialContext(ctx, address, nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	stopCancellation := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopCancellation()
	conn.SetReadLimit(4 << 20)
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetWriteDeadline(deadline)
		_ = conn.SetReadDeadline(deadline)
	}
	if err := conn.WriteJSON(map[string]any{
		"id": 1, "method": "initialize", "params": map[string]any{
			"clientInfo":   map[string]any{"name": "connect-bots-readiness", "version": "1"},
			"capabilities": map[string]any{"experimentalApi": true},
		},
	}); err != nil {
		return err
	}
	for {
		var response struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := conn.ReadJSON(&response); err != nil {
			return err
		}
		if string(response.ID) != "1" {
			continue
		}
		if len(response.Error) != 0 && string(response.Error) != "null" {
			return fmt.Errorf("initialize rejected; see the private app-server log")
		}
		if len(response.Result) == 0 {
			return fmt.Errorf("initialize returned no result")
		}
		return conn.WriteJSON(map[string]any{"method": "initialized", "params": map[string]any{}})
	}
}
