package bots

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const ExpectedAccountHeader = "X-Connect-Bots-Account"
const ExpectedNodeHeader = "X-Connect-Bots-Node"

var errHostAccountChanged = errors.New("account session changed; sign in again")

// HostServerConfig describes host-wide account authentication. Account ownership
// comes only from a server-side session; a requested node must belong to it.
type HostServerConfig struct {
	Root                 string
	StaticDir            string
	StaticFS             fs.FS
	AllowedOrigins       []string
	RegistrationAllowed  bool
	LegacyWorkspace      bool
	LegacyToken          string
	ResolveTenant        func(context.Context, Account) (*Server, error)
	ResolveInternalToken func(string) (*Server, bool)
	Nodes                *NodeHub
	PublicURL            string
	NodeBinaryDir        string
	Version              string
}

type hostUser struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

type hostSession struct {
	Authenticated        bool      `json:"authenticated"`
	User                 *hostUser `json:"user,omitempty"`
	RegistrationAllowed  bool      `json:"registrationAllowed"`
	SetupRequired        bool      `json:"setupRequired,omitempty"`
	LegacyClaimAvailable bool      `json:"legacyClaimAvailable,omitempty"`
}

type hostRequest struct {
	cancel context.CancelFunc
}

// HostServer owns accounts and routes authenticated requests to their individual
// workspace servers. Workspace and runtime lifecycle belongs to the resolver.
type HostServer struct {
	config                 HostServerConfig
	accounts               *AccountStore
	handler                http.Handler
	legacyHash             [32]byte
	legacyKey              [32]byte
	legacyReady            bool
	kdfSlots               chan struct{}
	loginMu                sync.Mutex
	loginLimits            map[string]loginAttempt
	registerMu             sync.Mutex
	sessionsMu             sync.Mutex
	requests               map[[32]byte]map[*hostRequest]struct{}
	sessionRecheckInterval time.Duration
	closed                 bool
}

func NewHostServer(config HostServerConfig) (*HostServer, error) {
	if config.Root == "" || config.ResolveTenant == nil {
		return nil, fmt.Errorf("host requires a data root and tenant resolver")
	}
	if config.PublicURL != "" {
		var err error
		config.PublicURL, err = ValidateNodePublicURL(config.PublicURL, true)
		if err != nil {
			return nil, err
		}
	}
	accounts, err := OpenAccountStore(config.Root)
	if err != nil {
		return nil, err
	}
	opened := false
	defer func() {
		if !opened {
			accounts.Close()
		}
	}()
	legacy, err := hostWorkspacePolicy(accounts, config.LegacyWorkspace)
	if err != nil {
		return nil, err
	}
	config.LegacyWorkspace = legacy
	config.AllowedOrigins = append([]string(nil), config.AllowedOrigins...)
	host := &HostServer{
		config: config, accounts: accounts, kdfSlots: make(chan struct{}, 4),
		loginLimits: make(map[string]loginAttempt),
		requests:    make(map[[32]byte]map[*hostRequest]struct{}), sessionRecheckInterval: 30 * time.Second,
	}
	// Fresh installations need no token. A legacy token only authorizes claiming
	// the old workspace once, and is never used for normal account login.
	if legacy && !accounts.HasOwner() {
		token, err := loadOwnerToken(config.Root, config.LegacyToken)
		if err != nil {
			return nil, err
		}
		host.legacyHash = sha256.Sum256([]byte(token))
		key := hmac.New(sha256.New, []byte(token))
		key.Write([]byte("connect-bots/session/v1"))
		copy(host.legacyKey[:], key.Sum(nil))
		host.legacyReady = true
	}
	host.config.LegacyToken = ""
	host.handler = host.buildHandler()
	opened = true
	return host, nil
}

// Persist the first-start ownership policy. Merely starting a legacy host
// creates accounts.json; that file alone cannot mean its data is safe to claim
// anonymously on the next restart.
func hostWorkspacePolicy(accounts *AccountStore, legacy bool) (bool, error) {
	path := filepath.Join(filepath.Dir(accounts.path), "workspace.json")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		content, err := json.Marshal(struct {
			Version         int  `json:"version"`
			LegacyWorkspace bool `json:"legacyWorkspace"`
		}{Version: 1, LegacyWorkspace: legacy})
		if err != nil {
			return false, fmt.Errorf("encode workspace ownership policy: %w", err)
		}
		if err := writePrivateAtomic(path, content); err != nil {
			return false, fmt.Errorf("persist workspace ownership policy: %w", err)
		}
		return legacy, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect workspace ownership policy: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > 1024 {
		return false, fmt.Errorf("invalid workspace ownership policy file")
	}
	if err := os.Chmod(path, 0600); err != nil {
		return false, fmt.Errorf("protect workspace ownership policy: %w", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read workspace ownership policy: %w", err)
	}
	var policy struct {
		Version         int   `json:"version"`
		LegacyWorkspace *bool `json:"legacyWorkspace"`
	}
	if err := json.Unmarshal(content, &policy); err != nil || policy.Version != 1 || policy.LegacyWorkspace == nil {
		return false, fmt.Errorf("invalid workspace ownership policy")
	}
	return *policy.LegacyWorkspace, nil
}

func (h *HostServer) Handler() http.Handler { return h.handler }

// PreloadTenants restores persistent runtimes, maintenance and Telegram
// bindings before the host accepts requests after a restart.
func (h *HostServer) PreloadTenants(ctx context.Context) error {
	var failures []error
	for _, account := range h.accounts.List() {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		if _, err := h.tenant(ctx, account); err != nil {
			failures = append(failures, fmt.Errorf("open workspace for account %s: %w", account.ID, err))
		}
	}
	return errors.Join(failures...)
}

func (h *HostServer) Close() error {
	h.sessionsMu.Lock()
	if h.closed {
		h.sessionsMu.Unlock()
		return nil
	}
	h.closed = true
	for _, requests := range h.requests {
		for request := range requests {
			request.cancel()
		}
	}
	h.requests = make(map[[32]byte]map[*hostRequest]struct{})
	h.sessionsMu.Unlock()
	return h.accounts.Close()
}

func (h *HostServer) buildHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/studio/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/studio/session", h.session)
	mux.HandleFunc("POST /api/studio/login", h.login)
	mux.HandleFunc("POST /api/studio/register", h.register)
	mux.HandleFunc("POST /api/studio/logout", h.logout)
	h.addNodeRoutes(mux)
	mux.HandleFunc("/api/studio/internal/tools", h.internalTool)
	mux.Handle("/api/studio/", h.requireAccount(http.HandlerFunc(h.dispatchTenant)))
	mux.Handle("/", newStaticHandler(ServerConfig{StaticDir: h.config.StaticDir, StaticFS: h.config.StaticFS}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		if strings.HasPrefix(r.URL.Path, "/api/studio") || strings.HasPrefix(r.URL.Path, "/api/nodes") {
			w.Header().Set("Cache-Control", "no-store")
		}
		mux.ServeHTTP(w, r)
	})
}

func (h *HostServer) session(w http.ResponseWriter, r *http.Request) {
	account, err := h.accounts.ResolveSession(hostCookie(r))
	if err == nil {
		writeJSON(w, http.StatusOK, h.sessionResult(&account, false))
		return
	}
	if !errors.Is(err, ErrInvalidCredentials) {
		h.authError(w, "resolve account session", err)
		return
	}
	legacy := !h.accounts.HasOwner() && h.legacyAuthenticated(r)
	writeJSON(w, http.StatusOK, h.sessionResult(nil, legacy))
}

func (h *HostServer) login(w http.ResponseWriter, r *http.Request) {
	if !h.authOriginAndLimit(w, r) {
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeHostCredentials(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	release, ok := h.acquireKDF(w)
	if !ok {
		return
	}
	account, err := h.accounts.Authenticate(input.Username, input.Password)
	release()
	if errors.Is(err, ErrInvalidCredentials) {
		writeError(w, http.StatusUnauthorized, ErrInvalidCredentials)
		return
	}
	if err != nil {
		h.authError(w, "authenticate account", err)
		return
	}
	h.finishLogin(w, r, account, http.StatusOK)
}

func (h *HostServer) register(w http.ResponseWriter, r *http.Request) {
	if !h.authOriginAndLimit(w, r) {
		return
	}
	if !h.config.RegistrationAllowed {
		writeError(w, http.StatusForbidden, fmt.Errorf("account registration is disabled"))
		return
	}
	var input struct {
		Username  string `json:"username"`
		Password  string `json:"password"`
		AccessKey string `json:"accessKey,omitempty"`
	}
	if err := decodeHostCredentials(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	release, ok := h.acquireKDF(w)
	if !ok {
		return
	}
	// Serializing the owner decision also handles two concurrent first signups:
	// exactly one account owns a fresh root and both registrations can succeed.
	h.registerMu.Lock()
	owner, err := h.registrationOwner(r, input.AccessKey)
	var account Account
	if err == nil {
		account, err = h.accounts.Register(input.Username, input.Password, owner)
	}
	h.registerMu.Unlock()
	release()
	if err != nil {
		h.registrationError(w, err)
		return
	}
	h.finishLogin(w, r, account, http.StatusCreated)
}

func (h *HostServer) registrationOwner(r *http.Request, accessKey string) (bool, error) {
	hasOwner := h.accounts.HasOwner()
	if accessKey != "" {
		provided := sha256.Sum256([]byte(accessKey))
		if !h.legacyReady || !hmac.Equal(provided[:], h.legacyHash[:]) {
			return false, ErrInvalidCredentials
		}
		if hasOwner {
			return false, fmt.Errorf("%w: existing workspace has already been claimed", ErrConflict)
		}
		return true, nil
	}
	if hasOwner {
		return false, nil
	}
	return !h.config.LegacyWorkspace || h.legacyAuthenticated(r), nil
}

func (h *HostServer) registrationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalidCredentials):
		writeError(w, http.StatusUnauthorized, ErrInvalidCredentials)
	case errors.Is(err, ErrUsernameTaken):
		writeError(w, http.StatusConflict, ErrUsernameTaken)
	case errors.Is(err, ErrInvalid), errors.Is(err, ErrConflict):
		writeError(w, statusForError(err), err)
	default:
		h.authError(w, "register account", err)
	}
}

func (h *HostServer) finishLogin(w http.ResponseWriter, r *http.Request, account Account, status int) {
	if _, err := h.tenant(r.Context(), account); err != nil {
		if status == http.StatusCreated {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"code":  "account_created_workspace_unavailable",
				"error": "Your account was created, but its workspace is unavailable. Use Sign in to try again.",
			})
		} else {
			writeError(w, http.StatusServiceUnavailable, fmt.Errorf("workspace is unavailable; try again later"))
		}
		return
	}
	token, expiresAt, err := h.accounts.NewSession(account.ID)
	if err != nil {
		h.authError(w, "create account session", err)
		return
	}
	if err := h.revokeSession(hostCookie(r)); err != nil {
		if revokeErr := h.accounts.RevokeSession(token); revokeErr != nil {
			slog.Error("revoke unissued account session", "error", revokeErr)
		}
		h.authError(w, "rotate account session", err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: token, Path: "/api/studio", HttpOnly: true,
		Secure: requestHTTPS(r), SameSite: http.SameSiteStrictMode,
		MaxAge: int(AccountSessionLifetime.Seconds()), Expires: expiresAt,
	})
	writeJSON(w, status, h.sessionResult(&account, false))
}

func (h *HostServer) logout(w http.ResponseWriter, r *http.Request) {
	if !h.originAllowed(r) {
		writeError(w, http.StatusForbidden, fmt.Errorf("request origin is not allowed"))
		return
	}
	if err := h.revokeLogoutSession(r); err != nil {
		if errors.Is(err, errHostAccountChanged) {
			h.accountChanged(w)
			return
		}
		h.authError(w, "revoke account session", err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Path: "/api/studio", HttpOnly: true,
		Secure: requestHTTPS(r), SameSite: http.SameSiteStrictMode,
		MaxAge: -1, Expires: time.Unix(1, 0),
	})
	writeJSON(w, http.StatusOK, h.sessionResult(nil, false))
}

func (h *HostServer) sessionResult(account *Account, legacy bool) hostSession {
	result := hostSession{
		Authenticated: account != nil, RegistrationAllowed: h.config.RegistrationAllowed,
		SetupRequired:        h.config.RegistrationAllowed && !h.config.LegacyWorkspace && h.accounts.Count() == 0,
		LegacyClaimAvailable: legacy,
	}
	if account != nil {
		result.User = &hostUser{ID: account.ID, Username: account.Username}
	}
	return result
}

type hostAccountKey struct{}

func (h *HostServer) requireAccount(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		account, ctx, release, err := h.beginRequest(r)
		if err != nil {
			if errors.Is(err, ErrInvalidCredentials) {
				writeError(w, http.StatusUnauthorized, fmt.Errorf("account login required"))
			} else {
				h.authError(w, "authorize account request", err)
			}
			return
		}
		defer release()
		if !h.expectedAccount(r, account.ID) {
			h.accountChanged(w)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions && !h.originAllowed(r) {
			writeError(w, http.StatusForbidden, fmt.Errorf("request origin is not allowed"))
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, hostAccountKey{}, account)))
	})
}

func (h *HostServer) accountChanged(w http.ResponseWriter) {
	writeJSON(w, http.StatusConflict, map[string]string{"code": "account_changed", "error": errHostAccountChanged.Error()})
}

// Every UI request identifies the account whose state produced it. A cookie
// shared by browser tabs can change to another user while an old tab is open.
// File links and EventSource use a query binding because they cannot set headers.
func (h *HostServer) expectedAccount(r *http.Request, accountID string) bool {
	expected := r.Header.Get(ExpectedAccountHeader)
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		query := r.URL.Query().Get("expectedAccount")
		if query != "" {
			if expected != "" && expected != query {
				return false
			}
			expected = query
		}
	}
	return expected == accountID
}

// Resolve and track under the same lock used for revocation. Logout cannot miss
// an SSE stream that was authorized concurrently and leave it reading data.
func (h *HostServer) beginRequest(r *http.Request) (Account, context.Context, func(), error) {
	token := hostCookie(r)
	digest := sha256.Sum256([]byte(token))
	h.sessionsMu.Lock()
	defer h.sessionsMu.Unlock()
	if h.closed {
		return Account{}, nil, nil, os.ErrClosed
	}
	account, expiresAt, err := h.accounts.ResolveSessionExpiry(token)
	if err != nil {
		return Account{}, nil, nil, err
	}
	ctx, cancel := context.WithDeadline(r.Context(), expiresAt)
	request := &hostRequest{cancel: cancel}
	if h.requests[digest] == nil {
		h.requests[digest] = make(map[*hostRequest]struct{})
	}
	h.requests[digest][request] = struct{}{}
	if isHostEventStream(r) {
		go h.revalidateStream(ctx, cancel, token, h.sessionRecheckInterval)
	}
	release := func() {
		cancel()
		h.sessionsMu.Lock()
		defer h.sessionsMu.Unlock()
		delete(h.requests[digest], request)
		if len(h.requests[digest]) == 0 {
			delete(h.requests, digest)
		}
	}
	return account, ctx, release, nil
}

// An absolute deadline handles expiry. Periodic server-state validation also
// stops streams when the session is evicted or revoked outside this HostServer.
func (h *HostServer) revalidateStream(ctx context.Context, cancel context.CancelFunc, token string, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := h.accounts.ResolveSession(token); err != nil {
				cancel()
				return
			}
		}
	}
}

func (h *HostServer) revokeSession(token string) error {
	h.sessionsMu.Lock()
	defer h.sessionsMu.Unlock()
	return h.revokeSessionLocked(token)
}

func (h *HostServer) revokeLogoutSession(r *http.Request) error {
	h.sessionsMu.Lock()
	defer h.sessionsMu.Unlock()
	token := hostCookie(r)
	account, err := h.accounts.ResolveSession(token)
	if err == nil {
		if !h.expectedAccount(r, account.ID) {
			return errHostAccountChanged
		}
	} else if !errors.Is(err, ErrInvalidCredentials) {
		return err
	} else if r.Header.Get(ExpectedAccountHeader) != "" {
		return errHostAccountChanged
	}
	return h.revokeSessionLocked(token)
}

func (h *HostServer) revokeSessionLocked(token string) error {
	if err := h.accounts.RevokeSession(token); err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(token))
	for request := range h.requests[digest] {
		request.cancel()
	}
	delete(h.requests, digest)
	return nil
}

func (h *HostServer) dispatchTenant(w http.ResponseWriter, r *http.Request) {
	account, ok := r.Context().Value(hostAccountKey{}).(Account)
	if !ok {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("account login required"))
		return
	}
	nodeID, valid := expectedNode(r)
	if !valid {
		writeJSON(w, http.StatusConflict, map[string]string{"code": "node_changed", "error": "workspace host changed; select the host again"})
		return
	}
	if nodeID != LocalNodeID {
		if h.config.Nodes == nil {
			writeError(w, http.StatusNotFound, fmt.Errorf("host not found"))
			return
		}
		h.config.Nodes.Proxy(account, nodeID, w, r)
		return
	}
	tenant, err := h.tenant(r.Context(), account)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("workspace is unavailable; try again later"))
		return
	}
	tenant.APIHandler().ServeHTTP(w, r)
}

func (h *HostServer) tenant(ctx context.Context, account Account) (*Server, error) {
	tenant, err := h.config.ResolveTenant(ctx, account)
	if err == nil && tenant == nil {
		err = fmt.Errorf("tenant resolver returned no workspace")
	}
	if err != nil {
		slog.Error("resolve account workspace", "userId", account.ID, "error", err)
	}
	return tenant, err
}

func (h *HostServer) internalTool(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("method not allowed"))
		return
	}
	remote, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		remote = r.RemoteAddr
	}
	address := net.ParseIP(remote)
	authorization := r.Header.Get("Authorization")
	if address == nil || !address.IsLoopback() || !strings.HasPrefix(authorization, "Bearer ") || len(authorization) > 1024 || h.config.ResolveInternalToken == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("internal authentication required"))
		return
	}
	tenant, ok := h.config.ResolveInternalToken(strings.TrimPrefix(authorization, "Bearer "))
	if !ok || tenant == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("internal authentication required"))
		return
	}
	tenant.InternalHandler().ServeHTTP(w, r)
}

func (h *HostServer) legacyAuthenticated(r *http.Request) bool {
	if !h.legacyReady {
		return false
	}
	parts := strings.Split(hostCookie(r), ".")
	if len(parts) != 3 {
		return false
	}
	now := time.Now()
	expiry, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || expiry <= now.Unix() || expiry > now.Add(sessionDuration+time.Minute).Unix() {
		return false
	}
	actual, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, h.legacyKey[:])
	mac.Write([]byte(parts[0] + "." + parts[1]))
	return hmac.Equal(actual, mac.Sum(nil))
}

func hostCookie(r *http.Request) string {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || len(cookie.Value) > 1024 {
		return ""
	}
	return cookie.Value
}

func (h *HostServer) originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false
	}
	for _, allowed := range h.config.AllowedOrigins {
		if strings.EqualFold(strings.TrimSuffix(allowed, "/"), origin) {
			return true
		}
	}
	scheme := "http"
	if requestHTTPS(r) {
		scheme = "https"
	}
	return strings.EqualFold(parsed.Scheme, scheme) && strings.EqualFold(parsed.Host, r.Host)
}

func (h *HostServer) authOriginAndLimit(w http.ResponseWriter, r *http.Request) bool {
	if !h.originAllowed(r) {
		writeError(w, http.StatusForbidden, fmt.Errorf("request origin is not allowed"))
		return false
	}
	if !h.allowLogin(r.RemoteAddr) {
		h.authBusy(w)
		return false
	}
	return true
}

func (h *HostServer) allowLogin(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	now := time.Now()
	h.loginMu.Lock()
	defer h.loginMu.Unlock()
	for key, attempt := range h.loginLimits {
		if now.Sub(attempt.since) >= time.Minute {
			delete(h.loginLimits, key)
		}
	}
	if len(h.loginLimits) >= 2048 {
		if _, exists := h.loginLimits[host]; !exists {
			return false
		}
	}
	attempt := h.loginLimits[host]
	if attempt.since.IsZero() {
		attempt.since = now
	}
	attempt.count++
	h.loginLimits[host] = attempt
	return attempt.count <= 10
}

func (h *HostServer) acquireKDF(w http.ResponseWriter) (func(), bool) {
	select {
	case h.kdfSlots <- struct{}{}:
		return func() { <-h.kdfSlots }, true
	default:
		h.authBusy(w)
		return nil, false
	}
}

func (h *HostServer) authBusy(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "60")
	writeError(w, http.StatusTooManyRequests, fmt.Errorf("too many authentication attempts; try again later"))
}

func (h *HostServer) authError(w http.ResponseWriter, operation string, err error) {
	slog.Error(operation, "error", err)
	writeError(w, http.StatusServiceUnavailable, fmt.Errorf("account service is unavailable; try again later"))
}

func decodeHostCredentials(w http.ResponseWriter, r *http.Request, value any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 2048)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("invalid JSON request")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("request must contain one JSON value")
	}
	return nil
}
