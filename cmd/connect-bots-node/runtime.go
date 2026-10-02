package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/bots"
)

type nodeRunOptions struct {
	Root         string
	CodexCommand string
	CodexHome    string
	FlovURL      string
}

type nodeWorkspace struct {
	store        *bots.Store
	runtime      *bots.Runtime
	telegram     *bots.TelegramManager
	server       *bots.Server
	internal     *http.Server
	listener     net.Listener
	internalDone chan error
	closeOnce    sync.Once
	closeErr     error
}

func runNode(parent context.Context, config nodeConfig, options nodeRunOptions, deps nodeDependencies) (runErr error) {
	if err := parent.Err(); err != nil {
		return nil
	}
	lock, err := acquireNodeLock(options.Root)
	if err != nil {
		return err
	}
	defer lock.Close()
	// Read again while locked: a concurrent pair --replace may have finished
	// between command parsing and the lock. Never run with a stale credential.
	saved, root, err := loadNodeConfig(options.Root)
	if err != nil {
		return err
	}
	if saved.Credential != config.Credential {
		return errors.New("node pairing changed before startup; run the command again")
	}
	options.Root = root
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	workspace := &nodeWorkspace{}
	workspace.store, err = bots.OpenStore(root)
	if err != nil {
		return fmt.Errorf("open node workspace: %w", err)
	}
	// The dedicated Codex child must outlive every runtime adapter and final
	// journal write. Cancellation only terminates a child while it is warming;
	// after readiness teardown closes the workspace first, then the child.
	ownedCodex, stopCodex, err := startOwnedCodex(ctx, bots.CodexAppServerConfig{
		DataDir: root, Command: options.CodexCommand, CodexHome: options.CodexHome,
		StartupTimeout: 120 * time.Second,
	}, deps.startCodex)
	if err != nil {
		if parent.Err() != nil && errors.Is(err, context.Canceled) {
			return workspace.Close()
		}
		return errors.Join(err, workspace.Close())
	}
	defer func() {
		runErr = errors.Join(runErr, workspace.Close(), ownedCodex.Close())
		stopCodex()
	}()
	if err := workspace.initialize(ctx, options, ownedCodex.URL(), deps); err != nil {
		return err
	}
	clientDone := make(chan error, 1)
	go func() {
		clientDone <- deps.runClient(ctx, bots.NodeClientConfig{
			Credential: config.Credential, Metadata: deps.metadata(), AllowInsecure: config.AllowInsecure,
			OnConnected: func() { slog.Info("Connect Bots Node connected", "node", config.Credential.NodeID) },
		}, workspace.server.APIHandler())
	}()
	slog.Info("Connect Bots Node started", "node", config.Credential.NodeID, "workspace", root)
	var clientErr error
	clientEnded := false
	select {
	case clientErr = <-clientDone:
		clientEnded = true
	case <-ctx.Done():
	case <-ownedCodex.Done():
		clientErr = ownedCodex.Err()
		if clientErr == nil {
			clientErr = errors.New("dedicated Codex app-server exited unexpectedly")
		}
	case serveErr := <-workspace.internalDone:
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			clientErr = fmt.Errorf("local tool listener stopped: %w", serveErr)
		} else {
			clientErr = errors.New("local tool listener stopped unexpectedly")
		}
	}
	// Cancel tunnel requests before draining local adapters. Mutations accepted
	// by Runtime retain their durable state and are never replayed here.
	cancel()
	if !clientEnded {
		select {
		case err := <-clientDone:
			if !errors.Is(err, context.Canceled) {
				clientErr = errors.Join(clientErr, err)
			}
		case <-time.After(15 * time.Second):
			clientErr = errors.Join(clientErr, errors.New("node connection did not stop after cancellation"))
		}
	}
	if parent.Err() != nil && errors.Is(clientErr, context.Canceled) {
		clientErr = nil
	}
	return clientErr
}

func startOwnedCodex(startup context.Context, config bots.CodexAppServerConfig, start func(context.Context, bots.CodexAppServerConfig) (codexProcess, error)) (codexProcess, context.CancelFunc, error) {
	ctx, stop := context.WithCancel(context.Background())
	stopStartupBridge := context.AfterFunc(startup, stop)
	server, err := start(ctx, config)
	stopStartupBridge()
	if err != nil {
		stop()
		return nil, nil, fmt.Errorf("start node Codex app-server: %w", err)
	}
	return server, stop, nil
}

func (workspace *nodeWorkspace) initialize(ctx context.Context, options nodeRunOptions, codexURL string, deps nodeDependencies) error {
	endpoint, err := bots.ValidateCodexAppServerURL(codexURL)
	if err != nil {
		return fmt.Errorf("node Codex endpoint: %w", err)
	}
	managedWorkspace := bots.NewWorkspace(workspace.store)
	managedWorkspace.Configure(nodeWorkspaceConfig(options))
	extension, err := bots.InstallPiExtension(workspace.store.Root())
	if err != nil {
		return err
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return fmt.Errorf("generate local tool credential: %w", err)
	}
	internalToken := hex.EncodeToString(random[:])
	// Only the separately credentialed Pi tool bridge listens, and only on
	// loopback. The workspace API is supplied directly to the outbound tunnel.
	workspace.listener, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen for local Pi tools: %w", err)
	}
	internalURL := "http://" + workspace.listener.Addr().String()
	workspace.runtime = deps.newRuntime(workspace.store, nodeRuntimeConfig(options, managedWorkspace, endpoint, extension, internalURL, internalToken))
	workspace.telegram = bots.NewTelegramManager(workspace.store, workspace.runtime)
	workspace.telegram.SetWorkspace(managedWorkspace)
	// This is a personal node, so its local environment, native Codex skills,
	// and Telegram credentials belong to the node owner rather than the hub.
	workspace.server, err = bots.NewTenantServer(workspace.store, workspace.runtime, bots.ServerConfig{
		FlovURL: options.FlovURL, InternalToken: internalToken, Workspace: managedWorkspace,
		BotChanged: func(string) {
			go func() {
				if err := workspace.telegram.Sync(ctx); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, os.ErrClosed) {
					slog.Warn("Connect Bots Node: synchronize Telegram bots", "error", err)
				}
			}()
		},
	})
	if err != nil {
		return err
	}
	workspace.internal = &http.Server{
		Handler: workspace.server.InternalHandler(), ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout: 90 * time.Second, MaxHeaderBytes: 1 << 20,
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	workspace.internalDone = make(chan error, 1)
	go func() { workspace.internalDone <- workspace.internal.Serve(workspace.listener) }()
	if err := workspace.telegram.Sync(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Warn("Connect Bots Node: initialize Telegram bots", "error", err)
	}
	workspace.server.StartBackground(ctx)
	return nil
}

func nodeWorkspaceConfig(options nodeRunOptions) bots.WorkspaceConfig {
	config := bots.WorkspaceConfig{FlovURL: options.FlovURL}
	codexHome := strings.TrimSpace(options.CodexHome)
	if codexHome == "" {
		codexHome = strings.TrimSpace(os.Getenv("CODEX_HOME"))
	}
	if codexHome != "" {
		home, _ := os.UserHomeDir()
		config.BackendSkillDirs = map[string][]string{
			"codex": {filepath.Join(codexHome, "skills")},
			"pi":    {filepath.Join(home, ".pi", "agent", "skills"), filepath.Join(home, ".pi", "skills")},
		}
		config.SystemSkillDirs = []string{filepath.Join(codexHome, "skills", ".system")}
	}
	return config
}

func nodeRuntimeConfig(options nodeRunOptions, workspace *bots.Workspace, codexURL, extension, internalURL, token string) bots.RuntimeConfig {
	return bots.RuntimeConfig{
		AgentOptions: map[string]map[string]any{
			"codex": {"app_server_url": codexURL, "codex_home": options.CodexHome, "cmd": []string{options.CodexCommand}},
		},
		PiExtensionPath: extension, InternalURL: internalURL, InternalToken: token,
		Instructions: workspace.AgentInstructions, SessionOptions: workspace.SkillSessionOptions,
		ResolveAttachments: workspace.ResolveAttachments, PublishFiles: workspace.PublishFiles,
		VoiceAvailable: options.FlovURL != "",
	}
}

func (workspace *nodeWorkspace) Close() error {
	workspace.closeOnce.Do(func() {
		var failures []error
		if workspace.server != nil {
			failures = append(failures, workspace.server.Close())
		}
		if workspace.telegram != nil {
			failures = append(failures, workspace.telegram.Close())
		}
		if workspace.runtime != nil {
			failures = append(failures, workspace.runtime.Close())
		}
		if workspace.internal != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			shutdownErr := workspace.internal.Shutdown(ctx)
			cancel()
			if shutdownErr != nil {
				failures = append(failures, shutdownErr, workspace.internal.Close())
			}
		} else if workspace.listener != nil {
			failures = append(failures, workspace.listener.Close())
		}
		if workspace.store != nil {
			failures = append(failures, workspace.store.Close())
		}
		workspace.closeErr = errors.Join(failures...)
	})
	return workspace.closeErr
}
