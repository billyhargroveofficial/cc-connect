package bots

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	accountPasswordCost       = 12
	accountMaxSessions        = 4096
	accountMaxSessionsPerUser = 20
	accountMaxStateBytes      = 8 << 20
	AccountSessionLifetime    = 30 * 24 * time.Hour
)

var (
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrUsernameTaken      = errors.New("username is already taken")
	accountIDPattern      = regexp.MustCompile(`^user_[a-f0-9]{32}$`)
	accountNamePattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{2,31}$`)
	accountDigestPattern  = regexp.MustCompile(`^[a-f0-9]{64}$`)
	accountHashPattern    = regexp.MustCompile(`^\$2a\$12\$[./A-Za-z0-9]{53}$`)
	accountDummyOnce      sync.Once
	accountDummyHash      []byte
	accountDummyError     error
)

// Account is safe to return to a client. Password hashes only exist in the
// private account store and its explicitly separate on-disk representation.
type Account struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	Owner     bool      `json:"owner"`
	CreatedAt time.Time `json:"createdAt"`
}

type accountRecord struct {
	account      Account
	passwordHash []byte
}

type accountDiskRecord struct {
	Account
	PasswordHash string `json:"passwordHash"`
}

type accountSession struct {
	Digest    string    `json:"digest"`
	UserID    string    `json:"userId"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type accountDiskState struct {
	Version  int                 `json:"version"`
	Accounts []accountDiskRecord `json:"accounts"`
	Sessions []accountSession    `json:"sessions"`
}

// AccountStore serializes registrations and session changes, and holds a file
// lock so separate processes cannot overwrite one another's private state.
type AccountStore struct {
	mu       sync.Mutex
	path     string
	accounts map[string]accountRecord
	names    map[string]string
	sessions map[string]accountSession
	lock     *storeFileLock
	now      func() time.Time
	write    func(string, []byte) error
	closed   bool
}

func OpenAccountStore(root string) (*AccountStore, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("account root: %w", err)
	}
	if err := privateDir(abs); err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("resolve account root: %w", err)
	}
	dir := filepath.Join(resolved, "auth")
	if err := privateDir(dir); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(dir, "accounts.lock")
	if info, err := os.Lstat(lockPath); err == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("invalid account lock file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect account lock: %w", err)
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
	accountDummyOnce.Do(func() {
		digest := sha256.Sum256([]byte("Connect Bots unknown-account password"))
		accountDummyHash, accountDummyError = bcrypt.GenerateFromPassword(digest[:], accountPasswordCost)
	})
	if accountDummyError != nil {
		return nil, fmt.Errorf("prepare account authentication: %w", accountDummyError)
	}
	s := &AccountStore{
		path: filepath.Join(dir, "accounts.json"), lock: lock, now: time.Now, write: writePrivateAtomic,
		accounts: make(map[string]accountRecord), names: make(map[string]string),
		sessions: make(map[string]accountSession),
	}
	info, err := os.Lstat(s.path)
	fresh := errors.Is(err, os.ErrNotExist)
	if err != nil && !fresh {
		return nil, fmt.Errorf("inspect account state: %w", err)
	}
	if !fresh {
		if !info.Mode().IsRegular() || info.Size() > accountMaxStateBytes {
			return nil, fmt.Errorf("invalid account state file")
		}
		if err := os.Chmod(s.path, 0600); err != nil {
			return nil, fmt.Errorf("protect account state: %w", err)
		}
		content, err := os.ReadFile(s.path)
		if err != nil {
			return nil, fmt.Errorf("read account state: %w", err)
		}
		if err := s.load(content); err != nil {
			return nil, err
		}
	}
	pruned := s.pruneSessionsLocked(s.now())
	if fresh || pruned {
		if _, err := s.saveLocked(); err != nil {
			return nil, err
		}
	}
	opened = true
	return s, nil
}

func (s *AccountStore) load(content []byte) error {
	var state accountDiskState
	if err := json.Unmarshal(content, &state); err != nil {
		return fmt.Errorf("decode account state: %w", err)
	}
	if state.Version != 1 {
		return fmt.Errorf("unsupported account state version %d", state.Version)
	}
	owners := 0
	for _, record := range state.Accounts {
		account := record.Account
		if !accountIDPattern.MatchString(account.ID) || !accountNamePattern.MatchString(account.Username) || account.CreatedAt.IsZero() {
			return fmt.Errorf("invalid account in state")
		}
		if _, exists := s.accounts[account.ID]; exists {
			return fmt.Errorf("duplicate account id in state")
		}
		if _, exists := s.names[account.Username]; exists {
			return fmt.Errorf("duplicate account username in state")
		}
		cost, err := bcrypt.Cost([]byte(record.PasswordHash))
		if err != nil || cost != accountPasswordCost || !accountHashPattern.MatchString(record.PasswordHash) {
			return fmt.Errorf("invalid password hash in account state")
		}
		if account.Owner {
			owners++
		}
		s.accounts[account.ID] = accountRecord{account: account, passwordHash: []byte(record.PasswordHash)}
		s.names[account.Username] = account.ID
	}
	if owners > 1 {
		return fmt.Errorf("multiple owners in account state")
	}
	for _, session := range state.Sessions {
		if !accountDigestPattern.MatchString(session.Digest) || session.ExpiresAt.IsZero() {
			return fmt.Errorf("invalid session in account state")
		}
		if _, exists := s.accounts[session.UserID]; !exists {
			return fmt.Errorf("session account missing from state")
		}
		if _, exists := s.sessions[session.Digest]; exists {
			return fmt.Errorf("duplicate session in account state")
		}
		s.sessions[session.Digest] = session
	}
	return nil
}

func (s *AccountStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.lock.Close()
}

func (s *AccountStore) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.accounts)
}

func (s *AccountStore) List() []Account {
	s.mu.Lock()
	defer s.mu.Unlock()
	accounts := make([]Account, 0, len(s.accounts))
	for _, record := range s.accounts {
		accounts = append(accounts, record.account)
	}
	sort.Slice(accounts, func(i, j int) bool {
		if !accounts[i].CreatedAt.Equal(accounts[j].CreatedAt) {
			return accounts[i].CreatedAt.Before(accounts[j].CreatedAt)
		}
		return accounts[i].ID < accounts[j].ID
	})
	return accounts
}

func (s *AccountStore) HasOwner() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hasOwnerLocked()
}

func (s *AccountStore) hasOwnerLocked() bool {
	for _, record := range s.accounts {
		if record.account.Owner {
			return true
		}
	}
	return false
}

func (s *AccountStore) Get(id string) (Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Account{}, os.ErrClosed
	}
	record, exists := s.accounts[id]
	if !exists {
		return Account{}, ErrNotFound
	}
	return record.account, nil
}

func normalizedAccountName(username string) string {
	return strings.Map(func(char rune) rune {
		if char >= 'A' && char <= 'Z' {
			return char + ('a' - 'A')
		}
		return char
	}, strings.TrimSpace(username))
}

func (s *AccountStore) Register(username, password string, owner bool) (Account, error) {
	username = normalizedAccountName(username)
	if !accountNamePattern.MatchString(username) {
		return Account{}, fmt.Errorf("%w: username must be 3–32 ASCII letters, digits, dots, underscores or hyphens and start with a letter or digit", ErrInvalid)
	}
	if len(password) < 8 || len(password) > 128 {
		return Account{}, fmt.Errorf("%w: password must be 8–128 bytes", ErrInvalid)
	}
	// bcrypt accepts at most 72 bytes. Hash the complete, untrimmed password to
	// a fixed-size digest first, so long passwords keep all their entropy.
	digest := sha256.Sum256([]byte(password))
	hash, err := bcrypt.GenerateFromPassword(digest[:], accountPasswordCost)
	if err != nil {
		return Account{}, fmt.Errorf("hash account password: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Account{}, os.ErrClosed
	}
	if _, exists := s.names[username]; exists {
		return Account{}, ErrUsernameTaken
	}
	if owner && s.hasOwnerLocked() {
		return Account{}, fmt.Errorf("%w: owner account already exists", ErrConflict)
	}
	id, err := randomID("user_")
	if err != nil {
		return Account{}, err
	}
	account := Account{ID: id, Username: username, Owner: owner, CreatedAt: s.now().UTC()}
	s.accounts[id] = accountRecord{account: account, passwordHash: hash}
	s.names[username] = id
	if committed, err := s.saveLocked(); err != nil {
		if !committed {
			delete(s.accounts, id)
			delete(s.names, username)
		}
		return Account{}, err
	}
	return account, nil
}

func (s *AccountStore) Authenticate(username, password string) (Account, error) {
	username = normalizedAccountName(username)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return Account{}, os.ErrClosed
	}
	id, exists := s.names[username]
	record := s.accounts[id]
	s.mu.Unlock()
	hash := accountDummyHash
	if exists {
		hash = record.passwordHash
	}
	digest := sha256.Sum256([]byte(password))
	err := bcrypt.CompareHashAndPassword(hash, digest[:])
	if !exists || len(password) < 8 || len(password) > 128 || err != nil {
		return Account{}, ErrInvalidCredentials
	}
	return record.account, nil
}

func accountSessionDigest(token string) (string, bool) {
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(decoded) != 32 || base64.RawURLEncoding.EncodeToString(decoded) != token {
		return "", false
	}
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:]), true
}

func (s *AccountStore) NewSession(userID string) (string, time.Time, error) {
	var entropy [32]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", time.Time{}, fmt.Errorf("generate account session: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(entropy[:])
	digest, _ := accountSessionDigest(token)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", time.Time{}, os.ErrClosed
	}
	if _, exists := s.accounts[userID]; !exists {
		return "", time.Time{}, ErrInvalidCredentials
	}
	before := s.copySessionsLocked()
	now := s.now().UTC()
	s.pruneSessionsLocked(now)
	s.makeSessionRoomLocked(userID)
	expiresAt := now.Add(AccountSessionLifetime)
	s.sessions[digest] = accountSession{Digest: digest, UserID: userID, ExpiresAt: expiresAt}
	if committed, err := s.saveLocked(); err != nil {
		if !committed {
			s.sessions = before
		}
		return "", time.Time{}, err
	}
	return token, expiresAt, nil
}

func (s *AccountStore) ResolveSession(token string) (Account, error) {
	account, _, err := s.ResolveSessionExpiry(token)
	return account, err
}

// ResolveSessionExpiry gives long-lived requests the absolute session deadline.
// The expiry comes from server state, never from client-controlled cookie data.
func (s *AccountStore) ResolveSessionExpiry(token string) (Account, time.Time, error) {
	digest, valid := accountSessionDigest(token)
	if !valid {
		return Account{}, time.Time{}, ErrInvalidCredentials
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Account{}, time.Time{}, os.ErrClosed
	}
	before := s.copySessionsLocked()
	if s.pruneSessionsLocked(s.now()) {
		if committed, err := s.saveLocked(); err != nil {
			if !committed {
				s.sessions = before
			}
			return Account{}, time.Time{}, err
		}
	}
	session, exists := s.sessions[digest]
	if !exists {
		return Account{}, time.Time{}, ErrInvalidCredentials
	}
	return s.accounts[session.UserID].account, session.ExpiresAt, nil
}

// RevokeSession removes the server-side record, including for still-valid
// cookies. Unknown tokens are an idempotent logout.
func (s *AccountStore) RevokeSession(token string) error {
	digest, valid := accountSessionDigest(token)
	if !valid {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return os.ErrClosed
	}
	before := s.copySessionsLocked()
	changed := s.pruneSessionsLocked(s.now())
	if _, exists := s.sessions[digest]; exists {
		delete(s.sessions, digest)
		changed = true
	}
	if !changed {
		return nil
	}
	if committed, err := s.saveLocked(); err != nil {
		if !committed {
			s.sessions = before
		}
		return err
	}
	return nil
}

func (s *AccountStore) copySessionsLocked() map[string]accountSession {
	copy := make(map[string]accountSession, len(s.sessions))
	for digest, session := range s.sessions {
		copy[digest] = session
	}
	return copy
}

func (s *AccountStore) pruneSessionsLocked(now time.Time) bool {
	changed := false
	for digest, session := range s.sessions {
		if !session.ExpiresAt.After(now) {
			delete(s.sessions, digest)
			changed = true
		}
	}
	// Enforce limits after reopening as well as when issuing new sessions.
	ordered := s.orderedSessionsLocked()
	perUser := make(map[string]int)
	kept := 0
	for i := len(ordered) - 1; i >= 0; i-- {
		session := ordered[i]
		if perUser[session.UserID] >= accountMaxSessionsPerUser || kept >= accountMaxSessions {
			delete(s.sessions, session.Digest)
			changed = true
		} else {
			perUser[session.UserID]++
			kept++
		}
	}
	return changed
}

func (s *AccountStore) orderedSessionsLocked() []accountSession {
	ordered := make([]accountSession, 0, len(s.sessions))
	for _, session := range s.sessions {
		ordered = append(ordered, session)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if !ordered[i].ExpiresAt.Equal(ordered[j].ExpiresAt) {
			return ordered[i].ExpiresAt.Before(ordered[j].ExpiresAt)
		}
		return ordered[i].Digest < ordered[j].Digest
	})
	return ordered
}

func (s *AccountStore) makeSessionRoomLocked(userID string) {
	ordered := s.orderedSessionsLocked()
	count := 0
	for _, session := range ordered {
		if session.UserID == userID {
			count++
		}
	}
	for _, session := range ordered {
		if session.UserID == userID && count >= accountMaxSessionsPerUser {
			delete(s.sessions, session.Digest)
			count--
		}
	}
	for _, session := range ordered {
		if len(s.sessions) < accountMaxSessions {
			break
		}
		delete(s.sessions, session.Digest)
	}
}

func (s *AccountStore) saveLocked() (bool, error) {
	state := accountDiskState{Version: 1, Accounts: make([]accountDiskRecord, 0, len(s.accounts)), Sessions: s.orderedSessionsLocked()}
	for _, record := range s.accounts {
		state.Accounts = append(state.Accounts, accountDiskRecord{Account: record.account, PasswordHash: string(record.passwordHash)})
	}
	sort.Slice(state.Accounts, func(i, j int) bool { return state.Accounts[i].ID < state.Accounts[j].ID })
	content, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return false, fmt.Errorf("encode account state: %w", err)
	}
	if len(content) > accountMaxStateBytes {
		return false, fmt.Errorf("account state exceeds size limit")
	}
	if err := s.write(s.path, content); err != nil {
		// The shared atomic writer can report a directory-sync error after the
		// rename has committed. Keep memory consistent with that file while
		// still reporting the durability error to the caller.
		persisted, readErr := os.ReadFile(s.path)
		committed := readErr == nil && bytes.Equal(persisted, content)
		return committed, fmt.Errorf("persist account state: %w", err)
	}
	return true, nil
}
