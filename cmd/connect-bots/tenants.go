package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sync"

	"github.com/chenhg5/cc-connect/bots"
)

var tenantAccountIDPattern = regexp.MustCompile(`^user_[a-f0-9]{32}$`)

// TenantManagerConfig keeps host transport settings separate from account
// state. Every account gets its own store, workspace and runtime; the explicit
// product app-server remains the host's inference transport.
type TenantManagerConfig struct {
	Context     context.Context
	DataRoot    string
	CodexURL    string
	InternalURL string
	FlovURL     string
	Server      bots.ServerConfig
}

type managedTenant struct {
	store        *bots.Store
	runtime      *bots.Runtime
	telegram     *bots.TelegramManager
	server       *bots.Server
	internalHash [32]byte
	closeOnce    sync.Once
	closeErr     error
}

// TenantManager owns the process lifetime of account workspaces. The original
// owner workspace is opened eagerly at the existing data root so upgrading a
// single-user installation preserves bot IDs, journals and native threads.
type TenantManager struct {
	mu        sync.Mutex
	config    TenantManagerConfig
	ctx       context.Context
	cancel    context.CancelFunc
	root      string
	owner     *managedTenant
	tenants   map[string]*managedTenant
	closed    bool
	closeOnce sync.Once
	closeErr  error
}

func NewTenantManager(config TenantManagerConfig) (*TenantManager, error) {
	if config.Context == nil {
		config.Context = context.Background()
	}
	endpoint, err := bots.ValidateCodexAppServerURL(config.CodexURL)
	if err != nil {
		return nil, fmt.Errorf("tenant Codex endpoint: %w", err)
	}
	config.CodexURL = endpoint
	ctx, cancel := context.WithCancel(config.Context)
	m := &TenantManager{config: config, ctx: ctx, cancel: cancel, tenants: make(map[string]*managedTenant)}
	owner, err := m.openTenant(config.DataRoot, true)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("open owner workspace: %w", err)
	}
	m.owner, m.root = owner, owner.store.Root()
	return m, nil
}

// Resolve accepts identities supplied by the host's authenticated account
// store, never usernames or paths supplied by a browser. Holding the lock while
// opening ensures concurrent first requests share exactly one store lock.
func (m *TenantManager) Resolve(account bots.Account) (*bots.Server, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, os.ErrClosed
	}
	if err := m.ctx.Err(); err != nil {
		return nil, err
	}
	if account.Owner {
		return m.owner.server, nil
	}
	if !tenantAccountIDPattern.MatchString(account.ID) {
		return nil, fmt.Errorf("%w: invalid account identity", bots.ErrInvalid)
	}
	if tenant := m.tenants[account.ID]; tenant != nil {
		return tenant.server, nil
	}
	root, err := m.accountRoot(account.ID)
	if err != nil {
		return nil, err
	}
	tenant, err := m.openTenant(root, false)
	if err != nil {
		return nil, fmt.Errorf("open account workspace: %w", err)
	}
	m.tenants[account.ID] = tenant
	return tenant.server, nil
}

func (m *TenantManager) OwnerServer() *bots.Server {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.owner.server
}

// ResolveInternalToken routes only process-local Pi bridge credentials. These
// credentials are independent of browser sessions and never leave the runtime.
func (m *TenantManager) ResolveInternalToken(raw string) (*bots.Server, bool) {
	if raw == "" {
		return nil, false
	}
	digest := sha256.Sum256([]byte(raw))
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, false
	}
	var found *bots.Server
	if subtle.ConstantTimeCompare(digest[:], m.owner.internalHash[:]) == 1 {
		found = m.owner.server
	}
	// Check every loaded workspace rather than stopping on the first match.
	for _, tenant := range m.tenants {
		if subtle.ConstantTimeCompare(digest[:], tenant.internalHash[:]) == 1 {
			found = tenant.server
		}
	}
	return found, found != nil
}

func (m *TenantManager) accountRoot(id string) (string, error) {
	users := filepath.Join(m.root, "users")
	if err := os.Mkdir(users, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("create account directory: %w", err)
	}
	info, err := os.Lstat(users)
	if err != nil {
		return "", fmt.Errorf("inspect account directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("account directory must not be a symbolic link")
	}
	if err := os.Chmod(users, 0700); err != nil {
		return "", fmt.Errorf("secure account directory: %w", err)
	}
	root := filepath.Join(users, id)
	info, err = os.Lstat(root)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect account workspace: %w", err)
	}
	if err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return "", errors.New("account workspace must be a directory without symbolic links")
	}
	return root, nil
}

func (m *TenantManager) openTenant(root string, owner bool) (_ *managedTenant, openErr error) {
	store, err := bots.OpenStore(root)
	if err != nil {
		return nil, err
	}
	tenant := &managedTenant{store: store}
	defer func() {
		if openErr != nil {
			openErr = errors.Join(openErr, tenant.Close())
		}
	}()
	workspace := bots.NewWorkspace(store)
	workspaceConfig := bots.WorkspaceConfig{FlovURL: m.config.FlovURL}
	if !owner {
		// Product-global skills belong to this account. The host owner's native
		// skill directories are deliberately absent from another user's editor.
		workspaceConfig.NativeSkillDirs = []string{}
		workspaceConfig.BackendSkillDirs = map[string][]string{}
		workspaceConfig.SystemSkillDirs = []string{}
	}
	workspace.Configure(workspaceConfig)
	if !owner {
		if err := provisionTenantPiConfig(store.Root()); err != nil {
			return nil, err
		}
	}
	extension, err := bots.InstallPiExtension(store.Root())
	if err != nil {
		return nil, err
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, fmt.Errorf("generate account internal credential: %w", err)
	}
	internalToken := hex.EncodeToString(random[:])
	tenant.internalHash = sha256.Sum256([]byte(internalToken))
	tenant.runtime = bots.NewRuntime(store, tenantRuntimeConfig(m.config, store, workspace, extension, internalToken, owner))
	tenant.telegram = bots.NewTelegramManager(store, tenant.runtime)
	tenant.telegram.SetWorkspace(workspace)
	if !owner {
		tenant.telegram.SetEnvironmentResolver(nil)
	}
	config := m.config.Server
	config.Token = ""
	config.AllowedOrigins = append([]string(nil), config.AllowedOrigins...)
	config.FlovURL, config.InternalToken, config.Workspace = m.config.FlovURL, internalToken, workspace
	config.BotChanged = func(id string) {
		if m.config.Server.BotChanged != nil {
			m.config.Server.BotChanged(id)
		}
		go func() {
			if err := tenant.telegram.Sync(m.ctx); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, os.ErrClosed) {
				slog.Warn("connect-bots: synchronize account Telegram bots", "error", err)
			}
		}()
	}
	tenant.server, err = bots.NewTenantServer(store, tenant.runtime, config)
	if err != nil {
		return nil, err
	}
	if err := tenant.telegram.Sync(m.ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Warn("connect-bots: initialize account Telegram bots", "error", err)
	}
	tenant.server.StartBackground(m.ctx)
	return tenant, nil
}

func tenantRuntimeConfig(config TenantManagerConfig, store *bots.Store, workspace *bots.Workspace, extension, token string, owner bool) bots.RuntimeConfig {
	options := map[string]map[string]any{"codex": {"app_server_url": config.CodexURL}}
	if !owner {
		home := store.Root()
		options["codex"]["env"] = map[string]string{"HOME": home}
		options["pi"] = map[string]any{"env": map[string]string{
			"HOME": home, "PI_CODING_AGENT_DIR": filepath.Join(home, ".pi", "agent"),
		}}
	}
	return bots.RuntimeConfig{
		AgentOptions: options, PiExtensionPath: extension,
		InternalURL: config.InternalURL, InternalToken: token,
		Instructions: workspace.AgentInstructions, SessionOptions: workspace.SkillSessionOptions,
		ResolveAttachments: workspace.ResolveAttachments, ResolveSkills: workspace.ResolveSkills, PublishFiles: workspace.PublishFiles,
		VoiceAvailable: config.FlovURL != "",
	}
}

func (tenant *managedTenant) Close() error {
	tenant.closeOnce.Do(func() {
		var failures []error
		if tenant.server != nil {
			failures = append(failures, tenant.server.Close())
		}
		if tenant.telegram != nil {
			failures = append(failures, tenant.telegram.Close())
		}
		if tenant.runtime != nil {
			failures = append(failures, tenant.runtime.Close())
		}
		// Runtime teardown may append final events. The store must stay open
		// until every runtime adapter and background writer has drained.
		if tenant.store != nil {
			failures = append(failures, tenant.store.Close())
		}
		tenant.closeErr = errors.Join(failures...)
	})
	return tenant.closeErr
}

func (m *TenantManager) Close() error {
	m.closeOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		tenants := []*managedTenant{m.owner}
		for _, tenant := range m.tenants {
			tenants = append(tenants, tenant)
		}
		m.mu.Unlock()
		m.cancel()
		var failures []error
		for _, tenant := range tenants {
			failures = append(failures, tenant.Close())
		}
		m.closeErr = errors.Join(failures...)
	})
	return m.closeErr
}
