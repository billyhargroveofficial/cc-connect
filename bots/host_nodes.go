package bots

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// ValidateNodePublicURL normalizes the externally reachable hub origin. It must
// be explicit because a browser development proxy may use a different port from
// the API listener. Plain HTTP beyond loopback needs an explicit operator opt-in.
func ValidateNodePublicURL(value string, allowInsecure bool) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.Opaque != "" || (parsed.Path != "" && parsed.Path != "/") || parsed.RawPath != "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("public-url must be an HTTP or HTTPS origin without credentials, path, query or fragment")
	}
	if port := parsed.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return "", fmt.Errorf("public-url has an invalid TCP port")
		}
	}
	if parsed.Scheme == "http" && !allowInsecure {
		host := parsed.Hostname()
		ip := net.ParseIP(host)
		if !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
			return "", fmt.Errorf("public-url requires HTTPS; use --allow-insecure-nodes only for a trusted network")
		}
	}
	parsed.Path = ""
	return parsed.String(), nil
}

// A node is part of workspace identity, just as the account is. Browser file
// links and EventSource can bind it through a query; mutations must use a header.
func expectedNode(r *http.Request) (string, bool) {
	values := r.Header.Values(ExpectedNodeHeader)
	if len(values) > 1 {
		return "", false
	}
	header := r.Header.Get(ExpectedNodeHeader)
	query, hasQuery := r.URL.Query()["node"]
	if hasQuery && len(query) != 1 {
		return "", false
	}
	selected := header
	if hasQuery {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && header == "" {
			return "", false
		}
		if header != "" && normalizeNodeID(header) != normalizeNodeID(query[0]) {
			return "", false
		}
		selected = query[0]
	}
	selected = normalizeNodeID(selected)
	if len(selected) > 128 || strings.TrimSpace(selected) != selected || strings.ContainsAny(selected, "/\\\x00\r\n\t ,") {
		return "", false
	}
	return selected, true
}

func normalizeNodeID(value string) string {
	if value == "" {
		return LocalNodeID
	}
	return value
}

// The remote selector does not change the workspace API path. Keep validation
// for any routed event endpoint too, so a future nested stream cannot outlive an
// evicted session while its node connection remains online.
func isHostEventStream(r *http.Request) bool {
	return r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/studio/") && strings.HasSuffix(r.URL.Path, "/events")
}

func (h *HostServer) addNodeRoutes(mux *http.ServeMux) {
	if h.config.Nodes != nil {
		mux.HandleFunc("POST /api/studio/nodes/enroll", h.config.Nodes.EnrollHTTP)
		mux.HandleFunc("GET /api/studio/nodes/connect", h.config.Nodes.ConnectHTTP)
		// Direct API origins can use these aliases while the development proxy
		// and browser client keep all node traffic under /api/studio.
		mux.HandleFunc("POST /api/nodes/enroll", h.config.Nodes.EnrollHTTP)
		mux.HandleFunc("GET /api/nodes/connect", h.config.Nodes.ConnectHTTP)
	}
	mux.Handle("GET /api/studio/nodes", h.requireAccount(http.HandlerFunc(h.listNodes)))
	mux.Handle("POST /api/studio/nodes/enrollments", h.requireAccount(http.HandlerFunc(h.createNodeEnrollment)))
	mux.Handle("DELETE /api/studio/nodes/{id}", h.requireAccount(http.HandlerFunc(h.removeNode)))
	mux.Handle("GET /api/studio/nodes/binary/darwin/{arch}", h.requireAccount(http.HandlerFunc(h.nodeBinary)))
	// Node administration stays on the hub, even when a selected remote node
	// header is attached. Unknown node routes must never reach the tunnel.
	mux.Handle("/api/studio/nodes/", h.requireAccount(http.NotFoundHandler()))
	mux.Handle("/api/studio/nodes", h.requireAccount(http.NotFoundHandler()))
	mux.Handle("/api/nodes/", http.NotFoundHandler())
}

func (h *HostServer) listNodes(w http.ResponseWriter, r *http.Request) {
	account := r.Context().Value(hostAccountKey{}).(Account)
	hostname, err := os.Hostname()
	if err != nil {
		hostname = ""
	}
	nodes := []NodeInfo{{
		ID: LocalNodeID, Name: "This server", Status: "online", Online: true, Local: true,
		Hostname: hostname, OS: runtime.GOOS, Arch: runtime.GOARCH, Version: h.config.Version,
		CreatedAt: account.CreatedAt, LastSeenAt: time.Now().UTC(),
	}}
	if h.config.Nodes != nil {
		nodes = append(nodes, h.config.Nodes.List(account)...)
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": nodes})
}

func (h *HostServer) createNodeEnrollment(w http.ResponseWriter, r *http.Request) {
	if h.config.Nodes == nil || h.config.PublicURL == "" {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("remote hosts are unavailable; configure the server public URL"))
		return
	}
	var input struct {
		Name string `json:"name"`
	}
	if err := decodeHostCredentials(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	account := r.Context().Value(hostAccountKey{}).(Account)
	enrollment, err := h.config.Nodes.CreateEnrollment(account, input.Name)
	if err != nil {
		switch {
		case errors.Is(err, ErrNodeNameInvalid):
			writeError(w, http.StatusBadRequest, fmt.Errorf("host name must contain 1 to 80 printable characters"))
		case errors.Is(err, ErrNodeLimitReached):
			writeError(w, http.StatusConflict, fmt.Errorf("host limit reached; remove an unused host first"))
		default:
			h.nodeServiceError(w, "create host enrollment", err)
		}
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		NodeEnrollment
		ServerURL string `json:"serverUrl"`
	}{NodeEnrollment: enrollment, ServerURL: h.config.PublicURL})
}

func (h *HostServer) removeNode(w http.ResponseWriter, r *http.Request) {
	if h.config.Nodes == nil || r.PathValue("id") == LocalNodeID {
		writeError(w, http.StatusNotFound, fmt.Errorf("host not found"))
		return
	}
	account := r.Context().Value(hostAccountKey{}).(Account)
	if err := h.config.Nodes.Remove(account, r.PathValue("id")); err != nil {
		if errors.Is(err, ErrNodeNotFound) {
			writeError(w, http.StatusNotFound, fmt.Errorf("host not found"))
		} else {
			h.nodeServiceError(w, "remove host", err)
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *HostServer) nodeServiceError(w http.ResponseWriter, operation string, err error) {
	// Keep node credentials and the enrollment value out of browser errors.
	slog.Error(operation, "error", err)
	writeError(w, http.StatusServiceUnavailable, fmt.Errorf("remote host service is unavailable; try again later"))
}

func (h *HostServer) nodeBinary(w http.ResponseWriter, r *http.Request) {
	arch := r.PathValue("arch")
	if h.config.NodeBinaryDir == "" || (arch != "arm64" && arch != "amd64") {
		writeError(w, http.StatusNotFound, fmt.Errorf("node binary is unavailable"))
		return
	}
	root, err := os.OpenRoot(h.config.NodeBinaryDir)
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("node binary is unavailable"))
		return
	}
	defer root.Close()
	name := "connect-bots-node-darwin-" + arch
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, fmt.Errorf("node binary is unavailable"))
		return
	}
	binary, err := root.Open(name)
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("node binary is unavailable"))
		return
	}
	defer binary.Close()
	info, err = binary.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, fmt.Errorf("node binary is unavailable"))
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	http.ServeContent(w, r, name, info.ModTime(), binary)
}
