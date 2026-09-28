// Package recorder is an HTTP reverse proxy that sits between a buyer
// client and a real seller MCP endpoint. It forwards JSON-RPC traffic
// both ways and captures every exchange into a session.Store: one "out"
// step per request, one "in" step per response or failure, linked by the
// JSON-RPC request ID.
//
// Session selection is per request: an explicit X-Session-ID header pins
// traffic to that session (creating it on first use); otherwise the
// recorder keeps one session per client connection (RemoteAddr).
//
// Recording is secret-safe: payloads and headers are redacted at capture
// time by the session package, so tokens and signatures never persist raw.
// The proxy never alters the forwarded bytes (except stripping its own
// X-Session-ID header); recording failures never break forwarding.
package recorder

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sujanchalla0510/adcp-test/internal/conformance"
	"github.com/sujanchalla0510/adcp-test/internal/session"
)

// maxBody bounds a single request/response body the recorder buffers.
const maxBody = 8 << 20

// SessionIDHeader pins proxied traffic to a named session. The header is
// stripped before forwarding upstream; it is the recorder's own control
// channel, not part of the seller protocol.
const SessionIDHeader = "X-Session-ID"

// maxSessionIDLen bounds accepted X-Session-ID values.
const maxSessionIDLen = 64

// Recorder forwards MCP JSON-RPC POSTs to an upstream seller and records
// each exchange into Store.
type Recorder struct {
	// Upstream is the seller's MCP endpoint (the full POST target).
	Upstream *url.URL
	// Store receives the recorded sessions.
	Store *session.Store
	// HTTPClient performs upstream calls; nil selects a default client
	// with a 60s timeout.
	HTTPClient *http.Client

	mu     sync.Mutex
	byAddr map[string]string // client RemoteAddr -> session ID
	srv    *http.Server
	ln     net.Listener

	anonID atomic.Int64
}

// NewRecorder builds a Recorder for upstream, which must be an http(s) URL.
func NewRecorder(upstream string, store *session.Store) (*Recorder, error) {
	u, err := url.Parse(strings.TrimSpace(upstream))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("recorder: upstream %q is not a valid http(s) URL", upstream)
	}
	if store == nil {
		store = session.NewStore()
	}
	return &Recorder{Upstream: u, Store: store, byAddr: map[string]string{}}, nil
}

// Start listens on addr (use "127.0.0.1:0" for an ephemeral port) and
// serves the proxy in the background. It returns the proxy base URL.
func (r *Recorder) Start(addr string) (string, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", fmt.Errorf("recorder: listen %s: %w", addr, err)
	}
	r.mu.Lock()
	r.ln = ln
	r.srv = &http.Server{Handler: r}
	r.mu.Unlock()
	go r.srv.Serve(ln) //nolint:errcheck // Close reports shutdown
	return "http://" + ln.Addr().String(), nil
}

// Close shuts the proxy listener down.
func (r *Recorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.srv == nil {
		return nil
	}
	return r.srv.Close()
}

// ProxyURL returns the proxy base URL after Start, or "" before.
func (r *Recorder) ProxyURL() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ln == nil {
		return ""
	}
	return "http://" + r.ln.Addr().String()
}

func (r *Recorder) httpClient() *http.Client {
	if r.HTTPClient != nil {
		return r.HTTPClient
	}
	return &http.Client{Timeout: 60 * time.Second}
}

// ServeHTTP implements the proxy: forward the request, record both halves.
func (r *Recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "recorder proxies MCP POST requests only", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, maxBody))
	req.Body.Close()
	if err != nil {
		http.Error(w, "recorder: read request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	method, tool, reqID, idRaw, args := parseJSONRPC(body, r.nextAnonID())
	sess := r.sessionFor(req)

	out := session.NewOutStep(method, tool, reqID, body, req.Header)
	if method == "tools/call" && tool != "" {
		out.ArgProblems = conformance.ValidateArgs(tool, args)
	}
	sess.Append(out)

	start := time.Now()
	upReq, err := http.NewRequestWithContext(req.Context(), http.MethodPost, r.Upstream.String(), bytes.NewReader(body))
	if err != nil {
		r.recordError(sess, method, tool, reqID, start, err.Error())
		writeRPCError(w, idRaw, -32000, "recorder: build upstream request: "+err.Error())
		return
	}
	copyHeaders(upReq.Header, req.Header)

	resp, err := r.httpClient().Do(upReq)
	if err != nil {
		r.recordError(sess, method, tool, reqID, start, err.Error())
		writeRPCError(w, idRaw, -32000, "recorder: upstream request failed: "+err.Error())
		return
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		r.recordError(sess, method, tool, reqID, start, "read upstream response: "+err.Error())
		writeRPCError(w, idRaw, -32000, "recorder: read upstream response: "+err.Error())
		return
	}

	recorded := respBody
	if isSSE(resp.Header) {
		if frame := firstSSEData(respBody); frame != nil {
			recorded = frame
		}
	}
	sess.Append(session.NewInStep(method, tool, reqID, recorded, time.Since(start)))

	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(respBody)
}

func (r *Recorder) recordError(sess *session.Session, method, tool, reqID string, start time.Time, msg string) {
	sess.Append(session.NewErrorStep(method, tool, reqID, msg, time.Since(start)))
}

func (r *Recorder) nextAnonID() string {
	return fmt.Sprintf("rec-%d", r.anonID.Add(1))
}

// sessionFor selects the session for req: X-Session-ID wins, otherwise
// one session per client connection (RemoteAddr).
func (r *Recorder) sessionFor(req *http.Request) *session.Session {
	if sid := sanitizeSessionID(req.Header.Get(SessionIDHeader)); sid != "" {
		return r.Store.NewWithID(sid, r.Upstream.String())
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if sid, ok := r.byAddr[req.RemoteAddr]; ok {
		if sess, found := r.Store.Get(sid); found {
			return sess
		}
	}
	sess := r.Store.New(r.Upstream.String())
	r.byAddr[req.RemoteAddr] = sess.ID
	return sess
}

// sanitizeSessionID accepts [A-Za-z0-9_-] up to maxSessionIDLen; anything
// else falls back to connection-based session selection.
func sanitizeSessionID(v string) string {
	if v == "" || len(v) > maxSessionIDLen {
		return ""
	}
	for _, c := range v {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return ""
		}
	}
	return v
}

// parseJSONRPC extracts the method, tool name, normalized request ID,
// raw request ID (for id-preserving error envelopes), and tools/call
// arguments from a JSON-RPC request body. Unparseable bodies yield method
// "unknown" so the exchange is still recorded.
func parseJSONRPC(body []byte, anonID string) (method, tool, reqID string, idRaw json.RawMessage, args json.RawMessage) {
	var env struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(body, &env); err != nil || env.Method == "" {
		return "unknown", "", anonID, nil, nil
	}
	method = env.Method
	reqID = normalizeID(env.ID, anonID)
	idRaw = env.ID
	if method == "tools/call" {
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(env.Params, &p); err == nil {
			tool, args = p.Name, p.Arguments
		}
	}
	return method, tool, reqID, idRaw, args
}

// normalizeID renders a JSON-RPC id (string, number, or absent) as a
// string for step pairing.
func normalizeID(raw json.RawMessage, anonID string) string {
	if len(raw) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return anonID
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return string(raw)
}

// hopByHop lists headers that must not be forwarded through the proxy.
var hopByHop = map[string]bool{
	"Connection": true, "Transfer-Encoding": true, "Te": true,
	"Trailer": true, "Upgrade": true, "Proxy-Authenticate": true,
	"Proxy-Authorization": true, "Keep-Alive": true,
}

// copyHeaders copies headers except hop-by-hop ones (Host and
// Content-Length are managed by net/http itself).
func copyHeaders(dst, src http.Header) {
	for k, vs := range src {
		if hopByHop[http.CanonicalHeaderKey(k)] {
			continue
		}
		if strings.EqualFold(k, SessionIDHeader) {
			continue // recorder control header: never forward upstream
		}
		cp := make([]string, len(vs))
		copy(cp, vs)
		dst[k] = cp
	}
}

func isSSE(h http.Header) bool {
	return strings.HasPrefix(h.Get("Content-Type"), "text/event-stream")
}

// firstSSEData extracts the first "data:" frame payload from an SSE body.
func firstSSEData(body []byte) []byte {
	for _, line := range bytes.Split(body, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
			continue
		}
		return data
	}
	return nil
}

// writeRPCError writes a JSON-RPC error envelope, preserving the
// request's original id value when it was valid JSON.
func writeRPCError(w http.ResponseWriter, idRaw json.RawMessage, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	var id any
	if len(idRaw) == 0 || json.Unmarshal(idRaw, &id) != nil {
		id = nil
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": msg},
	})
}
