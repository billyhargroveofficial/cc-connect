package bots

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func loadOwnerToken(root, configured string) (string, error) {
	if configured != "" {
		return configured, nil
	}
	if value := os.Getenv("CONNECT_BOTS_TOKEN"); value != "" {
		return value, nil
	}
	path := filepath.Join(root, "token")
	data, err := os.ReadFile(path)
	if err == nil {
		if err := os.Chmod(path, 0600); err != nil {
			return "", fmt.Errorf("secure owner token file: %w", err)
		}
		value := strings.TrimSpace(string(data))
		if len(value) < 16 {
			return "", fmt.Errorf("owner token file contains an invalid token")
		}
		return value, nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("read owner token file: %w", err)
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate owner token: %w", err)
	}
	value := base64.RawURLEncoding.EncodeToString(random[:])
	if err := writeInitialFile(path, []byte(value+"\n")); err != nil {
		return "", err
	}
	// Another process might have won O_EXCL; always use the persisted value.
	data, err = os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read created owner token file: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": s.authenticated(r)})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.originAllowed(r) {
		writeError(w, http.StatusForbidden, fmt.Errorf("request origin is not allowed"))
		return
	}
	if !s.allowLogin(r.RemoteAddr) {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, fmt.Errorf("too many login attempts; try again later"))
		return
	}
	var input struct {
		Token string `json:"token"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	provided := sha256.Sum256([]byte(input.Token))
	if subtle.ConstantTimeCompare(provided[:], s.tokenHash[:]) != 1 {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("invalid owner token"))
		return
	}
	value, err := s.newCookie()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: value, Path: "/api/studio", HttpOnly: true, Secure: requestHTTPS(r), SameSite: http.SameSiteStrictMode, MaxAge: int(sessionDuration.Seconds()), Expires: time.Now().Add(sessionDuration)})
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if !s.originAllowed(r) {
		writeError(w, http.StatusForbidden, fmt.Errorf("request origin is not allowed"))
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Path: "/api/studio", HttpOnly: true, Secure: requestHTTPS(r), SameSite: http.SameSiteStrictMode, MaxAge: -1, Expires: time.Unix(1, 0)})
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": false})
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authenticated(r) {
			writeError(w, http.StatusUnauthorized, fmt.Errorf("owner login required"))
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions && !s.originAllowed(r) {
			writeError(w, http.StatusForbidden, fmt.Errorf("request origin is not allowed"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authenticated(r *http.Request) bool {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || len(cookie.Value) > 1024 {
		return false
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 3 {
		return false
	}
	expiry, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || expiry <= time.Now().Unix() || expiry > time.Now().Add(sessionDuration+time.Minute).Unix() {
		return false
	}
	actual, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, s.cookieKey[:])
	mac.Write([]byte(parts[0] + "." + parts[1]))
	return hmac.Equal(actual, mac.Sum(nil))
}

func (s *Server) newCookie() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", fmt.Errorf("generate session: %w", err)
	}
	payload := strconv.FormatInt(time.Now().Add(sessionDuration).Unix(), 10) + "." + base64.RawURLEncoding.EncodeToString(nonce[:])
	mac := hmac.New(sha256.New, s.cookieKey[:])
	mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (s *Server) originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false
	}
	for _, allowed := range s.config.AllowedOrigins {
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

func requestHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	// A local reverse proxy may terminate TLS. Forwarded headers from a remote
	// network peer cannot change cookie security or Origin checks.
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback() && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func (s *Server) allowLogin(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	now := time.Now()
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	for key, attempt := range s.loginLimits {
		if now.Sub(attempt.since) >= time.Minute {
			delete(s.loginLimits, key)
		}
	}
	if len(s.loginLimits) >= 2048 {
		if _, exists := s.loginLimits[host]; !exists {
			return false
		}
	}
	attempt := s.loginLimits[host]
	if attempt.since.IsZero() {
		attempt.since = now
	}
	attempt.count++
	s.loginLimits[host] = attempt
	return attempt.count <= 10
}

func sameSecret(a, b string) bool {
	left, right := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(left[:], right[:]) == 1
}
