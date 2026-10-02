package bots

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const (
	nodeProtocolVersion     = 1
	nodeMaxRequestBytes     = 32 << 20 // Includes multipart overhead for 25 MiB uploads.
	nodeMaxResponseBytes    = 64 << 20 // SSE is streamed for the lifetime of the request.
	nodeMaxChunkBytes       = 64 << 10
	nodeMaxFrameBytes       = 128 << 10
	nodeMaxInflight         = 8
	nodeResponseQueue       = 32
	nodeResponseWindow      = 16
	nodeWriteTimeout        = 10 * time.Second
	nodeBrowserWriteTimeout = 30 * time.Second
	nodePongTimeout         = 60 * time.Second
	nodePingInterval        = 20 * time.Second
	nodeIDHeader            = "X-Connect-Bots-Node-ID"
)

var (
	errNodeBusy       = errors.New("node is busy; try again after an existing request completes")
	errNodeConnection = errors.New("node connection was interrupted")
	errNodeSlowReader = errors.New("node response could not be delivered")
)

// Frames are deliberately small. Request chunks are assembled with a strict
// body limit before a handler starts; response chunks flow immediately to the
// browser, including SSE flushes. Requests are never replayed after reconnect.
type nodeFrame struct {
	Kind          string       `json:"kind"`
	ID            string       `json:"id,omitempty"`
	Version       int          `json:"version,omitempty"`
	Metadata      NodeMetadata `json:"metadata,omitempty"`
	Method        string       `json:"method,omitempty"`
	URL           string       `json:"url,omitempty"`
	Headers       http.Header  `json:"headers,omitempty"`
	ContentLength int64        `json:"contentLength,omitempty"`
	Status        int          `json:"status,omitempty"`
	Data          []byte       `json:"data,omitempty"`
	Error         string       `json:"error,omitempty"`
}

type nodeFlight struct {
	ctx     context.Context
	frames  chan nodeFrame
	errors  chan error
	started bool // Owned by the WebSocket reader.
	sse     bool
	bytes   int64
}

type nodeSocket struct {
	ws        *websocket.Conn
	writeMu   sync.Mutex
	flightsMu sync.Mutex
	flights   map[string]*nodeFlight
	done      chan struct{}
	once      sync.Once
	seen      atomic.Int64
}

func newNodeSocket(ws *websocket.Conn) *nodeSocket {
	c := &nodeSocket{ws: ws, flights: make(map[string]*nodeFlight), done: make(chan struct{})}
	c.seen.Store(time.Now().UnixNano())
	ws.SetReadLimit(nodeMaxFrameBytes)
	return c
}

func (c *nodeSocket) configureHeartbeat() {
	c.ws.SetReadDeadline(time.Now().Add(nodePongTimeout))
	c.ws.SetPongHandler(func(string) error {
		c.seen.Store(time.Now().UnixNano())
		return c.ws.SetReadDeadline(time.Now().Add(nodePongTimeout))
	})
	go func() {
		ticker := time.NewTicker(nodePingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-c.done:
				return
			case <-ticker.C:
				if err := c.ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(nodeWriteTimeout)); err != nil {
					c.shutdown()
					return
				}
			}
		}
	}()
}

func (c *nodeSocket) send(frame nodeFrame) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.isClosed() {
		return errNodeConnection
	}
	if err := c.ws.SetWriteDeadline(time.Now().Add(nodeWriteTimeout)); err != nil {
		c.shutdown()
		return errNodeConnection
	}
	if err := c.ws.WriteJSON(frame); err != nil {
		c.shutdown()
		return errNodeConnection
	}
	return nil
}

func (c *nodeSocket) isClosed() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

func (c *nodeSocket) lastSeen() time.Time { return time.Unix(0, c.seen.Load()).UTC() }

func (c *nodeSocket) shutdown() {
	c.once.Do(func() {
		close(c.done)
		c.ws.Close()
	})
}

func (c *nodeSocket) closeWith(code int, reason string) {
	if !c.isClosed() {
		c.ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason), time.Now().Add(time.Second))
	}
	c.shutdown()
}

func (c *nodeSocket) closeGoingAway() { c.closeWith(websocket.CloseGoingAway, "host is restarting") }
func (c *nodeSocket) closeRevoked() {
	c.closeWith(websocket.ClosePolicyViolation, "node access revoked")
}

func (h *NodeHub) ConnectHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("node connection requires GET"))
		return
	}
	if !h.secureRequest(r) {
		writeError(w, http.StatusForbidden, fmt.Errorf("node connection requires HTTPS"))
		return
	}
	id := r.Header.Get(nodeIDHeader)
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !nodeIDPattern.MatchString(id) || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || !validNodeSecret(token, 32) || !h.nodeAuthenticated(id, nodeDigest(token)) {
		if !h.allowPublicAttempt(r) {
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusTooManyRequests, fmt.Errorf("too many node connection attempts"))
			return
		}
		writeError(w, http.StatusUnauthorized, fmt.Errorf("invalid node credential"))
		return
	}
	// Nodes are native clients. Browser-origin upgrades are intentionally refused
	// so a web page cannot turn a device credential into a workspace tunnel.
	upgrader := websocket.Upgrader{
		ReadBufferSize: 4096, WriteBufferSize: 4096,
		CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "" },
	}
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := newNodeSocket(ws)
	defer c.shutdown()
	ws.SetReadDeadline(time.Now().Add(10 * time.Second))
	var hello nodeFrame
	if err := ws.ReadJSON(&hello); err != nil || hello.Kind != "hello" || hello.Version != nodeProtocolVersion || validateNodeMetadata(hello.Metadata) != nil {
		c.closeWith(websocket.CloseProtocolError, "invalid node protocol")
		return
	}
	if err := h.registerConnection(id, nodeDigest(token), hello.Metadata, c); err != nil {
		if errors.Is(err, ErrNodeRevoked) {
			c.closeRevoked()
		} else {
			c.closeGoingAway()
		}
		return
	}
	defer h.unregisterConnection(id, c)
	if err := c.send(nodeFrame{Kind: "ready", Version: nodeProtocolVersion}); err != nil {
		return
	}
	c.configureHeartbeat()
	c.readHubFrames()
}

func (h *NodeHub) nodeAuthenticated(id, digest string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	node, exists := h.nodes[id]
	return !h.closed && exists && node.TokenDigest != "" && subtle.ConstantTimeCompare([]byte(node.TokenDigest), []byte(digest)) == 1
}

func (h *NodeHub) registerConnection(id, digest string, metadata NodeMetadata, c *nodeSocket) error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return errNodeConnection
	}
	node, exists := h.nodes[id]
	if !exists || node.TokenDigest == "" || subtle.ConstantTimeCompare([]byte(node.TokenDigest), []byte(digest)) != 1 {
		h.mu.Unlock()
		return ErrNodeRevoked
	}
	old := node
	node.Metadata, node.LastSeenAt = metadata, h.now()
	h.nodes[id] = node
	if err := h.saveLocked(); err != nil {
		h.nodes[id] = old
		h.mu.Unlock()
		return err
	}
	previous := h.connections[id]
	h.connections[id] = c
	h.mu.Unlock()
	if previous != nil {
		previous.closeWith(websocket.CloseNormalClosure, "node reconnected")
	}
	return nil
}

func (h *NodeHub) unregisterConnection(id string, c *nodeSocket) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || h.connections[id] != c {
		return
	}
	delete(h.connections, id)
	if node, exists := h.nodes[id]; exists {
		node.LastSeenAt = c.lastSeen()
		h.nodes[id] = node
		if err := h.saveLocked(); err != nil {
			slog.Warn("Could not persist node disconnect", "node", id)
		}
	}
}

func (c *nodeSocket) registerFlight(ctx context.Context) (string, *nodeFlight, error) {
	id, err := randomID("request_")
	if err != nil {
		return "", nil, err
	}
	c.flightsMu.Lock()
	defer c.flightsMu.Unlock()
	if c.isClosed() {
		return "", nil, ErrNodeOffline
	}
	if len(c.flights) >= nodeMaxInflight {
		return "", nil, errNodeBusy
	}
	flight := &nodeFlight{ctx: ctx, frames: make(chan nodeFrame, nodeResponseQueue), errors: make(chan error, 1)}
	c.flights[id] = flight
	return id, flight, nil
}

func (c *nodeSocket) removeFlight(id string) {
	c.flightsMu.Lock()
	delete(c.flights, id)
	c.flightsMu.Unlock()
}

func (c *nodeSocket) readHubFrames() {
	for {
		var frame nodeFrame
		if err := c.ws.ReadJSON(&frame); err != nil {
			return
		}
		c.seen.Store(time.Now().UnixNano())
		c.flightsMu.Lock()
		flight := c.flights[frame.ID]
		c.flightsMu.Unlock()
		if flight == nil {
			// The browser may have canceled while a final response was in flight.
			continue
		}
		valid := true
		switch frame.Kind {
		case "response":
			valid = !flight.started && frame.Status >= 200 && frame.Status <= 599 && len(frame.Data) == 0 && validNodeHeaders(frame.Headers)
			flight.started = true
			flight.sse = strings.HasPrefix(strings.ToLower(frame.Headers.Get("Content-Type")), "text/event-stream")
		case "chunk":
			flight.bytes += int64(len(frame.Data))
			valid = flight.started && len(frame.Data) > 0 && len(frame.Data) <= nodeMaxChunkBytes && (flight.sse || flight.bytes <= nodeMaxResponseBytes)
		case "flush", "done":
			valid = flight.started && len(frame.Data) == 0
		default:
			valid = false
		}
		if !valid {
			c.closeWith(websocket.CloseProtocolError, "invalid workspace response")
			return
		}
		select {
		case flight.frames <- frame:
		case <-flight.ctx.Done():
			c.removeFlight(frame.ID)
		case <-c.done:
			return
		default:
			// Bound memory for slow/disconnected browsers without stalling other
			// requests or the heartbeat reader on this node connection.
			select {
			case flight.errors <- errNodeSlowReader:
			default:
			}
			c.removeFlight(frame.ID)
			go c.send(nodeFrame{Kind: "cancel", ID: frame.ID})
		}
	}
}

func (h *NodeHub) connection(account Account, nodeID string) (*nodeSocket, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	node, exists := h.nodes[nodeID]
	if h.closed || !exists || node.AccountID != account.ID || !accountIDPattern.MatchString(account.ID) {
		return nil, ErrNodeNotFound
	}
	connection := h.connections[nodeID]
	if connection == nil || connection.isClosed() {
		return nil, ErrNodeOffline
	}
	return connection, nil
}

// Proxy sends one authenticated workspace request to its selected device. A
// disconnect terminates that request; reconnect never repeats a mutation.
func (h *NodeHub) Proxy(account Account, nodeID string, w http.ResponseWriter, r *http.Request) {
	connection, err := h.connection(account, nodeID)
	if err != nil {
		writeNodeProxyError(w, err)
		return
	}
	workspaceURL := *r.URL
	workspaceURL.Scheme, workspaceURL.Host, workspaceURL.User = "", "", nil
	uri, err := nodeWorkspaceURI(&workspaceURL)
	if err != nil || !nodeAllowedMethod(r.Method) {
		writeError(w, http.StatusForbidden, fmt.Errorf("only workspace requests can use a node"))
		return
	}
	if r.ContentLength > nodeMaxRequestBytes {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Errorf("node request is too large"))
		return
	}
	headers := filterNodeRequestHeaders(r.Header)
	if !validNodeHeaders(headers) {
		writeError(w, http.StatusRequestHeaderFieldsTooLarge, fmt.Errorf("node request headers are too large"))
		return
	}
	id, flight, err := connection.registerFlight(r.Context())
	if err != nil {
		writeNodeProxyError(w, err)
		return
	}
	finished := false
	defer func() {
		connection.removeFlight(id)
		if !finished && !connection.isClosed() {
			connection.send(nodeFrame{Kind: "cancel", ID: id})
		}
	}()
	body := []byte(nil)
	if r.Body != nil {
		body, err = io.ReadAll(io.LimitReader(r.Body, nodeMaxRequestBytes+1))
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("could not read node request"))
			return
		}
		if len(body) > nodeMaxRequestBytes {
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Errorf("node request is too large"))
			return
		}
	}
	if r.Context().Err() != nil {
		return
	}
	if err := connection.send(nodeFrame{Kind: "request", ID: id, Method: r.Method, URL: uri, Headers: headers, ContentLength: int64(len(body))}); err != nil {
		writeNodeProxyError(w, err)
		return
	}
	for len(body) > 0 {
		if r.Context().Err() != nil {
			return
		}
		size := min(len(body), nodeMaxChunkBytes)
		if err := connection.send(nodeFrame{Kind: "request_chunk", ID: id, Data: body[:size]}); err != nil {
			writeNodeProxyError(w, err)
			return
		}
		body = body[size:]
	}
	if err := connection.send(nodeFrame{Kind: "request_end", ID: id}); err != nil {
		writeNodeProxyError(w, err)
		return
	}
	deadline := nodeProxyDeadline{controller: http.NewResponseController(w), ctx: r.Context()}
	// Install only after all upload frames have been sent. A cancel interleaved
	// with an unfinished upload must not leave stray chunks on the node socket.
	stopCancel := context.AfterFunc(r.Context(), func() {
		deadline.cancel()
		if !connection.isClosed() {
			connection.send(nodeFrame{Kind: "cancel", ID: id})
		}
	})
	defer func() {
		stopCancel()
		deadline.reset()
	}()
	started, sse := false, false
	for {
		select {
		case <-r.Context().Done():
			return
		case <-connection.done:
			failNodeProxyResponse(w, r, started, errNodeConnection)
			return
		case err := <-flight.errors:
			failNodeProxyResponse(w, r, started, err)
			return
		case frame := <-flight.frames:
			switch frame.Kind {
			case "response":
				if !deadline.prepare() {
					return
				}
				for name, values := range filterNodeResponseHeaders(frame.Headers) {
					w.Header()[name] = values
				}
				w.WriteHeader(frame.Status)
				started = true
				sse = strings.HasPrefix(strings.ToLower(w.Header().Get("Content-Type")), "text/event-stream")
			case "chunk":
				if !deadline.prepare() {
					return
				}
				if _, err := w.Write(frame.Data); err != nil {
					return
				}
				if sse {
					if err := deadline.flush(); err != nil {
						return
					}
				}
			case "flush":
				if err := deadline.flush(); err != nil {
					return
				}
			case "done":
				if frame.Error != "" {
					failNodeProxyResponse(w, r, started, errNodeConnection)
					return
				}
				finished = true
				return
			}
			// Return credit only after the browser consumed this frame. Large
			// downloads cannot outrun a slow browser or exhaust the bounded queue.
			if err := connection.send(nodeFrame{Kind: "ack", ID: id}); err != nil {
				failNodeProxyResponse(w, r, started, err)
				return
			}
		}
	}
}

// Deadline changes are serialized with cancellation, without holding the lock
// during a potentially blocked browser write. A later refresh cannot undo the
// immediate deadline that unblocks logout/session cancellation.
type nodeProxyDeadline struct {
	mu         sync.Mutex
	controller *http.ResponseController
	ctx        context.Context
	canceled   bool
	finished   bool
}

func (d *nodeProxyDeadline) prepare() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.finished {
		return false
	}
	if d.canceled || d.ctx.Err() != nil {
		d.canceled = true
		d.controller.SetWriteDeadline(time.Now())
		return false
	}
	d.controller.SetWriteDeadline(time.Now().Add(nodeBrowserWriteTimeout))
	return true
}

func (d *nodeProxyDeadline) cancel() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.finished {
		return
	}
	d.canceled = true
	d.controller.SetWriteDeadline(time.Now())
}

func (d *nodeProxyDeadline) reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.finished {
		return
	}
	d.finished = true
	if d.canceled || d.ctx.Err() != nil {
		d.controller.SetWriteDeadline(time.Now())
	} else {
		d.controller.SetWriteDeadline(time.Time{})
	}
}

func (d *nodeProxyDeadline) flush() error {
	if !d.prepare() {
		if err := d.ctx.Err(); err != nil {
			return err
		}
		return errNodeConnection
	}
	err := d.controller.Flush()
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}

func failNodeProxyResponse(w http.ResponseWriter, r *http.Request, started bool, err error) {
	if r.Context().Err() != nil {
		return
	}
	if started {
		// An HTTP status can no longer change. Abort the stream so clients see an
		// incomplete response instead of accepting truncated JSON/files as success.
		panic(http.ErrAbortHandler)
	}
	writeNodeProxyError(w, err)
}

func writeNodeProxyError(w http.ResponseWriter, err error) {
	status, code, message := http.StatusBadGateway, "node_disconnected", "Node connection was interrupted. The request was not retried."
	switch {
	case errors.Is(err, ErrNodeNotFound):
		status, code, message = http.StatusNotFound, "node_not_found", "Node not found."
	case errors.Is(err, ErrNodeOffline):
		status, code, message = http.StatusServiceUnavailable, "node_offline", "This node is offline. Start Connect Bots Node on that device."
	case errors.Is(err, errNodeBusy):
		status, code, message = http.StatusServiceUnavailable, "node_busy", "This node has too many active requests. Try again shortly."
	}
	writeJSON(w, status, map[string]string{"code": code, "error": message})
}

// Restrict the tunnel to the workspace route families. Auth, registration, node
// administration, static assets and loopback runtime tools stay on the host.
func nodeWorkspaceURI(input *url.URL) (string, error) {
	if input == nil || input.IsAbs() || input.Host != "" || input.User != nil || input.Fragment != "" || len(input.RequestURI()) > 8192 {
		return "", fmt.Errorf("invalid workspace URL")
	}
	pathname := input.Path
	if !strings.HasPrefix(pathname, "/api/studio/") || strings.ContainsAny(pathname, "\\\x00\r\n") || strings.TrimSuffix(pathname, "/") != path.Clean(pathname) {
		return "", fmt.Errorf("invalid workspace path")
	}
	root := strings.Split(strings.TrimPrefix(pathname, "/api/studio/"), "/")[0]
	switch root {
	case "bots", "events", "capabilities", "user", "skills", "transcribe", "maintenance":
	default:
		return "", fmt.Errorf("not a workspace route")
	}
	copyURL := *input
	query, err := url.ParseQuery(copyURL.RawQuery)
	if err != nil {
		return "", fmt.Errorf("invalid workspace query")
	}
	for _, key := range []string{"expectedAccount", "node", "nodeId", "expectedNode"} {
		query.Del(key)
	}
	copyURL.RawQuery = query.Encode()
	copyURL.RawPath = ""
	return copyURL.RequestURI(), nil
}

func nodeAllowedMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions:
		return true
	default:
		return false
	}
}

func filterNodeRequestHeaders(input http.Header) http.Header {
	return nodeHeaderAllowlist(input, []string{"Content-Type", "Content-Length", "Accept", "Range", "If-Match", "If-None-Match", "If-Modified-Since", "If-Unmodified-Since", "If-Range", "Last-Event-ID"})
}

func filterNodeResponseHeaders(input http.Header) http.Header {
	return nodeHeaderAllowlist(input, []string{"Content-Type", "Content-Length", "Content-Disposition", "Cache-Control", "ETag", "Last-Modified", "Accept-Ranges", "Content-Range", "Vary", "Retry-After"})
}

func nodeHeaderAllowlist(input http.Header, names []string) http.Header {
	output := make(http.Header)
	hopHeaders := make(map[string]bool)
	for _, value := range input.Values("Connection") {
		for _, name := range strings.Split(value, ",") {
			hopHeaders[http.CanonicalHeaderKey(strings.TrimSpace(name))] = true
		}
	}
	for _, name := range names {
		if hopHeaders[http.CanonicalHeaderKey(name)] {
			continue
		}
		if values := input.Values(name); len(values) > 0 {
			output[http.CanonicalHeaderKey(name)] = append([]string(nil), values...)
		}
	}
	return output
}

func validNodeHeaders(headers http.Header) bool {
	if len(headers) > 64 {
		return false
	}
	length := 0
	for name, values := range headers {
		if strings.ContainsAny(name, "\x00\r\n") {
			return false
		}
		length += len(name)
		for _, value := range values {
			if strings.ContainsAny(value, "\x00\r\n") {
				return false
			}
			length += len(value)
		}
	}
	return length <= 16<<10
}

func nodeServerURL(raw string, allowInsecure bool) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") || parsed.Opaque != "" {
		return nil, fmt.Errorf("server URL must be an HTTP(S) origin without credentials, a path, or a query")
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return nil, fmt.Errorf("server URL must use HTTPS")
	}
	if parsed.Scheme == "http" && !allowInsecure && !nodeURLLoopback(parsed) {
		return nil, fmt.Errorf("server URL requires HTTPS; allow insecure nodes explicitly for a trusted LAN")
	}
	parsed.Path, parsed.RawPath = "", ""
	return parsed, nil
}

func nodeURLLoopback(parsed *url.URL) bool {
	host := parsed.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func PairNode(ctx context.Context, serverURL, code string, metadata NodeMetadata, allowInsecure bool) (NodeCredential, error) {
	base, err := nodeServerURL(serverURL, allowInsecure)
	if err != nil {
		return NodeCredential{}, err
	}
	if !validNodeSecret(code, 24) || validateNodeMetadata(metadata) != nil {
		return NodeCredential{}, fmt.Errorf("invalid node pairing code or metadata")
	}
	body, err := json.Marshal(struct {
		Code     string       `json:"code"`
		Metadata NodeMetadata `json:"metadata"`
	}{Code: code, Metadata: metadata})
	if err != nil {
		return NodeCredential{}, fmt.Errorf("encode node pairing request")
	}
	endpoint := *base
	endpoint.Path = "/api/studio/nodes/enroll"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return NodeCredential{}, fmt.Errorf("prepare node pairing request")
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return NodeCredential{}, ctx.Err()
		}
		return NodeCredential{}, fmt.Errorf("could not reach the Connect Bots server")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == http.StatusUnauthorized {
			return NodeCredential{}, ErrNodeEnrollmentInvalid
		}
		return NodeCredential{}, fmt.Errorf("server refused node pairing (HTTP %d)", response.StatusCode)
	}
	var credential NodeCredential
	if err := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&credential); err != nil || !nodeIDPattern.MatchString(credential.NodeID) || !validNodeSecret(credential.Token, 32) {
		return NodeCredential{}, fmt.Errorf("server returned an invalid node credential")
	}
	credential.ServerURL = base.String()
	return credential, nil
}

type NodeClientConfig struct {
	Credential    NodeCredential
	Metadata      NodeMetadata
	AllowInsecure bool
	OnConnected   func()
}

// RunNodeClient maintains one outbound connection. Reconnect restores access
// for future requests, while every interrupted request is canceled locally.
func RunNodeClient(ctx context.Context, config NodeClientConfig, handler http.Handler) error {
	base, err := nodeServerURL(config.Credential.ServerURL, config.AllowInsecure)
	if err != nil {
		return err
	}
	if !nodeIDPattern.MatchString(config.Credential.NodeID) || !validNodeSecret(config.Credential.Token, 32) || validateNodeMetadata(config.Metadata) != nil || handler == nil {
		return fmt.Errorf("invalid node client configuration")
	}
	delay := time.Second
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		connected, err := runNodeConnection(ctx, config, base, handler)
		if errors.Is(err, ErrNodeRevoked) {
			return ErrNodeRevoked
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if connected {
			delay = time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		delay = min(delay*2, 30*time.Second)
	}
}

func runNodeConnection(ctx context.Context, config NodeClientConfig, base *url.URL, handler http.Handler) (bool, error) {
	endpoint := *base
	if endpoint.Scheme == "https" {
		endpoint.Scheme = "wss"
	} else {
		endpoint.Scheme = "ws"
	}
	endpoint.Path = "/api/studio/nodes/connect"
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+config.Credential.Token)
	headers.Set(nodeIDHeader, config.Credential.NodeID)
	dialer := *websocket.DefaultDialer
	dialer.HandshakeTimeout = 15 * time.Second
	ws, response, err := dialer.DialContext(ctx, endpoint.String(), headers)
	if err != nil {
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		if response != nil && (response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusNotFound) {
			return false, ErrNodeRevoked
		}
		return false, errNodeConnection
	}
	c := newNodeSocket(ws)
	defer c.shutdown()
	connectionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-connectionCtx.Done():
			c.shutdown()
		case <-c.done:
		}
	}()
	if err := c.send(nodeFrame{Kind: "hello", Version: nodeProtocolVersion, Metadata: config.Metadata}); err != nil {
		return false, err
	}
	ws.SetReadDeadline(time.Now().Add(10 * time.Second))
	var ready nodeFrame
	if err := ws.ReadJSON(&ready); err != nil {
		return false, nodeClientReadError(err)
	}
	if ready.Kind != "ready" || ready.Version != nodeProtocolVersion {
		return false, errNodeConnection
	}
	c.configureHeartbeat()
	if config.OnConnected != nil {
		config.OnConnected()
	}
	return true, serveNodeFrames(connectionCtx, c, handler)
}

func nodeClientReadError(err error) error {
	var closeError *websocket.CloseError
	if errors.As(err, &closeError) && closeError.Code == websocket.ClosePolicyViolation {
		return ErrNodeRevoked
	}
	return errNodeConnection
}

type nodeIncomingRequest struct {
	frame   nodeFrame
	body    bytes.Buffer
	ctx     context.Context
	cancel  context.CancelFunc
	started bool
	credits chan struct{}
}

func serveNodeFrames(ctx context.Context, c *nodeSocket, handler http.Handler) error {
	var mu sync.Mutex
	var workers sync.WaitGroup
	requests := make(map[string]*nodeIncomingRequest)
	defer func() {
		mu.Lock()
		for _, request := range requests {
			request.cancel()
		}
		mu.Unlock()
		// Finish handlers before the CLI closes their stores/runtimes. A buggy
		// handler that ignores cancellation cannot prevent device shutdown forever.
		drained := make(chan struct{})
		go func() {
			workers.Wait()
			close(drained)
		}()
		timer := time.NewTimer(nodeWriteTimeout + 5*time.Second)
		defer timer.Stop()
		select {
		case <-drained:
		case <-timer.C:
			slog.Warn("Node workspace handlers did not finish after cancellation")
		}
	}()
	for {
		var frame nodeFrame
		if err := c.ws.ReadJSON(&frame); err != nil {
			return nodeClientReadError(err)
		}
		c.seen.Store(time.Now().UnixNano())
		if !strings.HasPrefix(frame.ID, "request_") || len(frame.ID) != 40 {
			return errNodeConnection
		}
		mu.Lock()
		incoming := requests[frame.ID]
		switch frame.Kind {
		case "request":
			parsed, err := url.ParseRequestURI(frame.URL)
			uri, validURI := nodeWorkspaceURI(parsed)
			if incoming != nil || err != nil || validURI != nil || uri != frame.URL || !nodeAllowedMethod(frame.Method) || !validNodeHeaders(frame.Headers) || frame.ContentLength < 0 || frame.ContentLength > nodeMaxRequestBytes || len(requests) >= nodeMaxInflight {
				mu.Unlock()
				return errNodeConnection
			}
			requestCtx, cancel := context.WithCancel(ctx)
			credits := make(chan struct{}, nodeResponseWindow)
			for range nodeResponseWindow {
				credits <- struct{}{}
			}
			requests[frame.ID] = &nodeIncomingRequest{frame: frame, ctx: requestCtx, cancel: cancel, credits: credits}
		case "request_chunk":
			if incoming == nil || incoming.started || len(frame.Data) == 0 || len(frame.Data) > nodeMaxChunkBytes || incoming.body.Len()+len(frame.Data) > nodeMaxRequestBytes || int64(incoming.body.Len()+len(frame.Data)) > incoming.frame.ContentLength {
				mu.Unlock()
				return errNodeConnection
			}
			incoming.body.Write(frame.Data)
		case "request_end":
			if incoming == nil || incoming.started || int64(incoming.body.Len()) != incoming.frame.ContentLength {
				mu.Unlock()
				return errNodeConnection
			}
			incoming.started = true
			workers.Add(1)
			go func(id string, incoming *nodeIncomingRequest) {
				defer workers.Done()
				serveNodeRequest(c, handler, incoming)
				incoming.cancel()
				mu.Lock()
				if requests[id] == incoming {
					delete(requests, id)
				}
				mu.Unlock()
			}(frame.ID, incoming)
		case "cancel":
			if incoming != nil {
				incoming.cancel()
				if !incoming.started {
					delete(requests, frame.ID)
				}
			}
		case "ack":
			if incoming != nil && incoming.started {
				select {
				case incoming.credits <- struct{}{}:
				default:
					mu.Unlock()
					return errNodeConnection
				}
			}
		default:
			mu.Unlock()
			return errNodeConnection
		}
		mu.Unlock()
	}
}

func serveNodeRequest(c *nodeSocket, handler http.Handler, incoming *nodeIncomingRequest) {
	writer := &nodeResponseWriter{socket: c, id: incoming.frame.ID, ctx: incoming.ctx, credits: incoming.credits, headers: make(http.Header)}
	defer func() {
		if recover() != nil {
			slog.Error("Node workspace handler failed")
			writer.fail()
		}
		writer.finish()
	}()
	req, err := http.NewRequestWithContext(incoming.ctx, incoming.frame.Method, incoming.frame.URL, bytes.NewReader(incoming.body.Bytes()))
	if err != nil {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	req.Header = filterNodeRequestHeaders(incoming.frame.Headers)
	req.ContentLength = incoming.frame.ContentLength
	req.Host = "connect-bots-node"
	req.RemoteAddr = "127.0.0.1:0"
	req.RequestURI = incoming.frame.URL
	handler.ServeHTTP(writer, req)
}

type nodeResponseWriter struct {
	mu      sync.Mutex
	socket  *nodeSocket
	id      string
	ctx     context.Context
	credits <-chan struct{}
	headers http.Header
	status  int
	bytes   int64
	sse     bool
	err     error
}

func (w *nodeResponseWriter) Header() http.Header { return w.headers }

func (w *nodeResponseWriter) WriteHeader(status int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writeHeaderLocked(status)
}

func (w *nodeResponseWriter) writeHeaderLocked(status int) {
	if w.status != 0 || w.err != nil {
		return
	}
	if status < 200 || status > 599 {
		status = http.StatusInternalServerError
	}
	w.status = status
	w.sse = strings.HasPrefix(strings.ToLower(w.headers.Get("Content-Type")), "text/event-stream")
	filtered := filterNodeResponseHeaders(w.headers)
	if !validNodeHeaders(filtered) {
		w.err = errNodeConnection
		return
	}
	w.err = w.sendLocked(nodeFrame{Kind: "response", ID: w.id, Status: status, Headers: filtered})
}

func (w *nodeResponseWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writeHeaderLocked(http.StatusOK)
	if w.err != nil {
		return 0, w.err
	}
	if !w.sse && w.bytes+int64(len(data)) > nodeMaxResponseBytes {
		w.err = fmt.Errorf("node response is too large")
		return 0, w.err
	}
	written := 0
	for len(data) > 0 {
		size := min(len(data), nodeMaxChunkBytes)
		if w.err = w.sendLocked(nodeFrame{Kind: "chunk", ID: w.id, Data: data[:size]}); w.err != nil {
			return written, w.err
		}
		written += size
		w.bytes += int64(size)
		data = data[size:]
	}
	return written, nil
}

func (w *nodeResponseWriter) Flush() { w.FlushError() }

func (w *nodeResponseWriter) FlushError() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writeHeaderLocked(http.StatusOK)
	if w.err == nil {
		w.err = w.sendLocked(nodeFrame{Kind: "flush", ID: w.id})
	}
	return w.err
}

func (w *nodeResponseWriter) sendLocked(frame nodeFrame) error {
	select {
	case <-w.ctx.Done():
		return w.ctx.Err()
	case <-w.socket.done:
		return errNodeConnection
	case <-w.credits:
	}
	if w.ctx.Err() != nil {
		return w.ctx.Err()
	}
	return w.socket.send(frame)
}

func (w *nodeResponseWriter) fail() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		w.writeHeaderLocked(http.StatusInternalServerError)
	}
	w.err = fmt.Errorf("workspace response failed")
}

func (w *nodeResponseWriter) finish() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writeHeaderLocked(http.StatusOK)
	errorMessage := ""
	if w.err != nil {
		errorMessage = "workspace response failed"
	}
	w.sendLocked(nodeFrame{Kind: "done", ID: w.id, Error: errorMessage})
}
