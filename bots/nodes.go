package bots

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	LocalNodeID            = "local"
	NodeEnrollmentLifetime = 10 * time.Minute
	nodeMaxStateBytes      = 4 << 20
	nodeMaxNodes           = 1024
	nodeMaxNodesPerAccount = 32
	nodeMaxPending         = 256
)

var (
	ErrNodeNotFound          = errors.New("node not found")
	ErrNodeOffline           = errors.New("node is offline")
	ErrNodeNameInvalid       = errors.New("node name must contain 1 to 80 characters without control characters")
	ErrNodeLimitReached      = errors.New("node limit reached; remove an unused node first")
	ErrNodeEnrollmentInvalid = errors.New("pairing code is invalid or expired")
	ErrNodeRevoked           = errors.New("node access was revoked; pair this device again")
	nodeIDPattern            = regexp.MustCompile(`^node_[a-f0-9]{32}$`)
)

// NodeMetadata describes the device, without exposing local paths or Codex
// credentials. The node owns its workspace and its own Codex configuration.
type NodeMetadata struct {
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Version  string `json:"version"`
}

// NodeInfo is safe to return to the signed-in owner. Credentials are kept in a
// separate private representation and never appear in node listings.
type NodeInfo struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Status     string    `json:"status"`
	Online     bool      `json:"online"`
	Local      bool      `json:"local"`
	Hostname   string    `json:"hostname,omitempty"`
	OS         string    `json:"os,omitempty"`
	Arch       string    `json:"arch,omitempty"`
	Version    string    `json:"version,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
	LastSeenAt time.Time `json:"lastSeenAt,omitempty"`
}

func (n NodeInfo) MarshalJSON() ([]byte, error) {
	type info NodeInfo
	var lastSeen *time.Time
	if !n.LastSeenAt.IsZero() {
		lastSeen = &n.LastSeenAt
	}
	return json.Marshal(struct {
		info
		LastSeenAt *time.Time `json:"lastSeenAt,omitempty"`
	}{info: info(n), LastSeenAt: lastSeen})
}

// NodeEnrollment contains a one-time secret. Only its digest is persisted.
type NodeEnrollment struct {
	NodeID    string    `json:"nodeId"`
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// NodeCredential is returned once to the device that successfully pairs. The
// caller must save it in a private file; the hub stores only Token's digest.
type NodeCredential struct {
	ServerURL string `json:"serverUrl"`
	NodeID    string `json:"nodeId"`
	Token     string `json:"token"`
}

type NodeHubConfig struct {
	Root          string
	AllowInsecure bool
}

type nodeRecord struct {
	ID          string       `json:"id"`
	AccountID   string       `json:"accountId"`
	Name        string       `json:"name"`
	Metadata    NodeMetadata `json:"metadata"`
	CreatedAt   time.Time    `json:"createdAt"`
	LastSeenAt  time.Time    `json:"lastSeenAt,omitempty"`
	TokenDigest string       `json:"tokenDigest,omitempty"`
}

type nodePairing struct {
	Digest    string    `json:"digest"`
	NodeID    string    `json:"nodeId"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type nodeDiskState struct {
	Version     int           `json:"version"`
	Nodes       []nodeRecord  `json:"nodes"`
	Enrollments []nodePairing `json:"enrollments"`
}

// NodeHub maps opaque device credentials to exactly one account. The outbound
// WebSocket carries only that device's workspace API, never host authentication
// or another account's workspace. A file lock prevents concurrent registries.
type NodeHub struct {
	mu          sync.Mutex
	config      NodeHubConfig
	path        string
	lock        *storeFileLock
	nodes       map[string]nodeRecord
	pairings    map[string]nodePairing
	connections map[string]*nodeSocket
	attempts    map[string]loginAttempt
	now         func() time.Time
	write       func(string, []byte) error
	closed      bool
}

func OpenNodeHub(config NodeHubConfig) (*NodeHub, error) {
	if config.Root == "" {
		return nil, fmt.Errorf("node registry requires a data root")
	}
	abs, err := filepath.Abs(config.Root)
	if err != nil {
		return nil, fmt.Errorf("node registry root: %w", err)
	}
	if err := privateDir(abs); err != nil {
		return nil, err
	}
	root, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("resolve node registry root: %w", err)
	}
	dir := filepath.Join(root, "auth")
	if err := privateDir(dir); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(dir, "nodes.lock")
	if info, err := os.Lstat(lockPath); err == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("invalid node registry lock file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect node registry lock: %w", err)
	}
	lock, err := acquireStoreLock(lockPath)
	if err != nil {
		return nil, err
	}
	opened := false
	defer func() {
		if !opened {
			lock.Close()
		}
	}()
	h := &NodeHub{
		config: config, path: filepath.Join(dir, "nodes.json"), lock: lock,
		nodes: make(map[string]nodeRecord), pairings: make(map[string]nodePairing),
		connections: make(map[string]*nodeSocket), attempts: make(map[string]loginAttempt),
		now: time.Now, write: writePrivateAtomic,
	}
	info, err := os.Lstat(h.path)
	fresh := errors.Is(err, os.ErrNotExist)
	if err != nil && !fresh {
		return nil, fmt.Errorf("inspect node registry: %w", err)
	}
	if !fresh {
		if !info.Mode().IsRegular() || info.Size() > nodeMaxStateBytes {
			return nil, fmt.Errorf("invalid node registry file")
		}
		if err := os.Chmod(h.path, 0600); err != nil {
			return nil, fmt.Errorf("protect node registry: %w", err)
		}
		content, err := os.ReadFile(h.path)
		if err != nil {
			return nil, fmt.Errorf("read node registry: %w", err)
		}
		if err := h.load(content); err != nil {
			return nil, err
		}
	}
	if fresh || h.prunePairingsLocked(h.now()) {
		if err := h.saveLocked(); err != nil {
			return nil, err
		}
	}
	opened = true
	return h, nil
}

func (h *NodeHub) load(content []byte) error {
	var state nodeDiskState
	if err := json.Unmarshal(content, &state); err != nil {
		return fmt.Errorf("decode node registry: %w", err)
	}
	if state.Version != 1 || len(state.Nodes) > nodeMaxNodes || len(state.Enrollments) > nodeMaxPending {
		return fmt.Errorf("invalid node registry version or size")
	}
	accountCounts := make(map[string]int)
	credentialDigests := make(map[string]bool)
	for _, node := range state.Nodes {
		if !nodeIDPattern.MatchString(node.ID) || !accountIDPattern.MatchString(node.AccountID) || node.CreatedAt.IsZero() || validNodeName(node.Name) != nil || validateNodeMetadata(node.Metadata) != nil {
			return fmt.Errorf("invalid node in registry")
		}
		if node.TokenDigest != "" && !accountDigestPattern.MatchString(node.TokenDigest) {
			return fmt.Errorf("invalid credential digest in node registry")
		}
		if node.TokenDigest != "" {
			if credentialDigests[node.TokenDigest] {
				return fmt.Errorf("duplicate credential digest in node registry")
			}
			credentialDigests[node.TokenDigest] = true
		}
		if _, exists := h.nodes[node.ID]; exists {
			return fmt.Errorf("duplicate node in registry")
		}
		accountCounts[node.AccountID]++
		if accountCounts[node.AccountID] > nodeMaxNodesPerAccount {
			return fmt.Errorf("too many nodes for an account in registry")
		}
		h.nodes[node.ID] = node
	}
	pending := make(map[string]bool)
	for _, pairing := range state.Enrollments {
		node, exists := h.nodes[pairing.NodeID]
		if !accountDigestPattern.MatchString(pairing.Digest) || !exists || node.TokenDigest != "" || !pairing.ExpiresAt.After(node.CreatedAt) || pairing.ExpiresAt.After(node.CreatedAt.Add(NodeEnrollmentLifetime)) || pending[pairing.NodeID] {
			return fmt.Errorf("invalid enrollment in node registry")
		}
		if _, exists := h.pairings[pairing.Digest]; exists {
			return fmt.Errorf("duplicate enrollment in node registry")
		}
		pending[pairing.NodeID] = true
		h.pairings[pairing.Digest] = pairing
	}
	for _, node := range h.nodes {
		if node.TokenDigest == "" && !pending[node.ID] {
			return fmt.Errorf("pending node has no enrollment in registry")
		}
	}
	return nil
}

func (h *NodeHub) saveLocked() error {
	state := nodeDiskState{Version: 1, Nodes: make([]nodeRecord, 0, len(h.nodes)), Enrollments: make([]nodePairing, 0, len(h.pairings))}
	for _, node := range h.nodes {
		state.Nodes = append(state.Nodes, node)
	}
	for _, pairing := range h.pairings {
		state.Enrollments = append(state.Enrollments, pairing)
	}
	sort.Slice(state.Nodes, func(i, j int) bool { return state.Nodes[i].ID < state.Nodes[j].ID })
	sort.Slice(state.Enrollments, func(i, j int) bool { return state.Enrollments[i].Digest < state.Enrollments[j].Digest })
	content, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode node registry: %w", err)
	}
	if err := h.write(h.path, content); err != nil {
		return fmt.Errorf("persist node registry: %w", err)
	}
	return nil
}

func (h *NodeHub) prunePairingsLocked(now time.Time) bool {
	changed := false
	for digest, pairing := range h.pairings {
		if !now.Before(pairing.ExpiresAt) {
			delete(h.pairings, digest)
			if node := h.nodes[pairing.NodeID]; node.TokenDigest == "" {
				delete(h.nodes, pairing.NodeID)
			}
			changed = true
		}
	}
	return changed
}

func (h *NodeHub) Close() error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	connections := make([]*nodeSocket, 0, len(h.connections))
	for _, connection := range h.connections {
		connections = append(connections, connection)
	}
	h.connections = make(map[string]*nodeSocket)
	h.mu.Unlock()
	for _, connection := range connections {
		connection.closeGoingAway()
	}
	return h.lock.Close()
}

func (h *NodeHub) List(account Account) []NodeInfo {
	h.mu.Lock()
	defer h.mu.Unlock()
	nodes := make([]NodeInfo, 0)
	if h.closed || !accountIDPattern.MatchString(account.ID) {
		return nodes
	}
	for _, node := range h.nodes {
		if node.AccountID != account.ID {
			continue
		}
		if node.TokenDigest == "" && !h.pendingNodeValidLocked(node.ID, h.now()) {
			continue
		}
		info := NodeInfo{
			ID: node.ID, Name: node.Name, Status: "offline", CreatedAt: node.CreatedAt, LastSeenAt: node.LastSeenAt,
			Hostname: node.Metadata.Hostname, OS: node.Metadata.OS, Arch: node.Metadata.Arch, Version: node.Metadata.Version,
		}
		if node.TokenDigest == "" {
			info.Status = "pending"
		} else if connection := h.connections[node.ID]; connection != nil && !connection.isClosed() {
			info.Status, info.Online = "online", true
			info.LastSeenAt = connection.lastSeen()
		}
		nodes = append(nodes, info)
	}
	sort.Slice(nodes, func(i, j int) bool {
		if !nodes[i].CreatedAt.Equal(nodes[j].CreatedAt) {
			return nodes[i].CreatedAt.Before(nodes[j].CreatedAt)
		}
		return nodes[i].ID < nodes[j].ID
	})
	return nodes
}

func (h *NodeHub) pendingNodeValidLocked(id string, now time.Time) bool {
	for _, pairing := range h.pairings {
		if pairing.NodeID == id && now.Before(pairing.ExpiresAt) {
			return true
		}
	}
	return false
}

func (h *NodeHub) CreateEnrollment(account Account, name string) (NodeEnrollment, error) {
	name = strings.TrimSpace(name)
	if err := validNodeName(name); err != nil {
		return NodeEnrollment{}, err
	}
	if !accountIDPattern.MatchString(account.ID) {
		return NodeEnrollment{}, ErrInvalidCredentials
	}
	id, err := randomID("node_")
	if err != nil {
		return NodeEnrollment{}, err
	}
	code, err := nodeSecret(24)
	if err != nil {
		return NodeEnrollment{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return NodeEnrollment{}, os.ErrClosed
	}
	// Pruning is persisted with the new enrollment. Expired pairings never grant
	// access even if a disk write fails during this best-effort cleanup.
	h.prunePairingsLocked(h.now())
	count := 0
	for _, node := range h.nodes {
		if node.AccountID == account.ID {
			count++
		}
	}
	if len(h.nodes) >= nodeMaxNodes || count >= nodeMaxNodesPerAccount || len(h.pairings) >= nodeMaxPending {
		return NodeEnrollment{}, ErrNodeLimitReached
	}
	result := NodeEnrollment{NodeID: id, Code: code, ExpiresAt: h.now().Add(NodeEnrollmentLifetime)}
	digest := nodeDigest(code)
	h.nodes[id] = nodeRecord{ID: id, AccountID: account.ID, Name: name, CreatedAt: h.now()}
	h.pairings[digest] = nodePairing{Digest: digest, NodeID: id, ExpiresAt: result.ExpiresAt}
	if err := h.saveLocked(); err != nil {
		delete(h.nodes, id)
		delete(h.pairings, digest)
		return NodeEnrollment{}, err
	}
	return result, nil
}

func (h *NodeHub) Remove(account Account, nodeID string) error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return os.ErrClosed
	}
	node, exists := h.nodes[nodeID]
	if !exists || node.AccountID != account.ID || !accountIDPattern.MatchString(account.ID) {
		h.mu.Unlock()
		return ErrNodeNotFound
	}
	delete(h.nodes, nodeID)
	removedPairings := make(map[string]nodePairing)
	for digest, pairing := range h.pairings {
		if pairing.NodeID == nodeID {
			removedPairings[digest] = pairing
			delete(h.pairings, digest)
		}
	}
	if err := h.saveLocked(); err != nil {
		h.nodes[nodeID] = node
		for digest, pairing := range removedPairings {
			h.pairings[digest] = pairing
		}
		h.mu.Unlock()
		return err
	}
	connection := h.connections[nodeID]
	delete(h.connections, nodeID)
	h.mu.Unlock()
	if connection != nil {
		connection.closeRevoked()
	}
	return nil
}

func validNodeName(name string) error {
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 80 || name != strings.TrimSpace(name) {
		return ErrNodeNameInvalid
	}
	for _, char := range name {
		if unicode.IsControl(char) {
			return ErrNodeNameInvalid
		}
	}
	return nil
}

func validateNodeMetadata(metadata NodeMetadata) error {
	for _, field := range []struct {
		value string
		limit int
	}{{metadata.Hostname, 128}, {metadata.OS, 64}, {metadata.Arch, 64}, {metadata.Version, 128}} {
		if len(field.value) > field.limit || !utf8.ValidString(field.value) {
			return fmt.Errorf("invalid device metadata")
		}
		for _, char := range field.value {
			if unicode.IsControl(char) {
				return fmt.Errorf("invalid device metadata")
			}
		}
	}
	return nil
}

func nodeSecret(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate node credential: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func nodeDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func validNodeSecret(value string, size int) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) == size && base64.RawURLEncoding.EncodeToString(decoded) == value
}

func (h *NodeHub) secureRequest(r *http.Request) bool {
	return h.config.AllowInsecure || requestHTTPS(r) || nodeLoopbackAddress(r.RemoteAddr)
}

func nodeLoopbackAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	return net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
}

func (h *NodeHub) allowPublicAttempt(r *http.Request) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return false
	}
	now := h.now()
	for address, attempt := range h.attempts {
		if now.Sub(attempt.since) >= time.Minute {
			delete(h.attempts, address)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	attempt := h.attempts[host]
	if attempt.since.IsZero() {
		if len(h.attempts) >= 2048 {
			return false
		}
		attempt.since = now
	}
	attempt.count++
	h.attempts[host] = attempt
	return attempt.count <= 60
}

func (h *NodeHub) EnrollHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("pairing requires POST"))
		return
	}
	if !h.secureRequest(r) {
		writeError(w, http.StatusForbidden, fmt.Errorf("node pairing requires HTTPS; allow insecure nodes explicitly for a trusted LAN"))
		return
	}
	var input struct {
		Code     string       `json:"code"`
		Metadata NodeMetadata `json:"metadata"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || !validNodeSecret(input.Code, 24) || validateNodeMetadata(input.Metadata) != nil {
		h.rejectPublicPairingAttempt(w, r, http.StatusBadRequest, fmt.Errorf("invalid node pairing request"))
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		h.rejectPublicPairingAttempt(w, r, http.StatusBadRequest, fmt.Errorf("invalid node pairing request"))
		return
	}
	credential, err := h.enroll(input.Code, input.Metadata)
	if err != nil {
		if errors.Is(err, ErrNodeEnrollmentInvalid) {
			h.rejectPublicPairingAttempt(w, r, http.StatusUnauthorized, err)
		} else {
			writeError(w, http.StatusServiceUnavailable, fmt.Errorf("node registry is unavailable"))
		}
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, credential)
}

// A reverse proxy puts unrelated clients under the same RemoteAddr. Throttle
// failed public attempts only, so anonymous traffic cannot deny a valid one-time
// code to its owner. A successful pairing still consumes the code atomically.
func (h *NodeHub) rejectPublicPairingAttempt(w http.ResponseWriter, r *http.Request, status int, err error) {
	if !h.allowPublicAttempt(r) {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, fmt.Errorf("too many node connection attempts"))
		return
	}
	writeError(w, status, err)
}

func (h *NodeHub) enroll(code string, metadata NodeMetadata) (NodeCredential, error) {
	token, err := nodeSecret(32)
	if err != nil {
		return NodeCredential{}, err
	}
	digest := nodeDigest(code)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return NodeCredential{}, os.ErrClosed
	}
	pairing, exists := h.pairings[digest]
	if !exists || !h.now().Before(pairing.ExpiresAt) {
		return NodeCredential{}, ErrNodeEnrollmentInvalid
	}
	node, exists := h.nodes[pairing.NodeID]
	if !exists || node.TokenDigest != "" {
		return NodeCredential{}, ErrNodeEnrollmentInvalid
	}
	old := node
	node.Metadata, node.TokenDigest = metadata, nodeDigest(token)
	h.nodes[node.ID] = node
	delete(h.pairings, digest)
	if err := h.saveLocked(); err != nil {
		h.nodes[node.ID] = old
		h.pairings[digest] = pairing
		return NodeCredential{}, err
	}
	return NodeCredential{NodeID: node.ID, Token: token}, nil
}
