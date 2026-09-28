// Package mcpclient is a minimal JSON-RPC 2.0 client for a seller agent's
// MCP endpoint, transported over HTTP POST.
//
// POST is the required core transport. A read path for SSE / streamable-HTTP
// responses is included: responses whose Content-Type is text/event-stream
// are unwrapped from their data: frames before JSON-RPC decoding.
//
// All logging is secret-safe: Authorization, Signature, and Signature-Input
// headers, plus any token-like JSON fields, are redacted before anything
// reaches the log.
package mcpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// DefaultTimeout is the per-request timeout when the caller sets none.
const DefaultTimeout = 30 * time.Second

// ErrorKind classifies a client failure.
type ErrorKind string

const (
	// KindTransport means the HTTP request itself failed (DNS, refused,
	// timeout, TLS).
	KindTransport ErrorKind = "transport"
	// KindProtocol means the server answered but not with JSON-RPC 2.0
	// (HTML error page, plaintext, empty body, malformed envelope).
	KindProtocol ErrorKind = "protocol"
	// KindRPC means the server returned a structured JSON-RPC error object.
	KindRPC ErrorKind = "rpc"
)

// RPCError is a structured JSON-RPC 2.0 error object.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message)
}

// Error is a structured client failure.
type Error struct {
	Kind ErrorKind
	Op   string // ListTools or CallTool
	Err  error
	RPC  *RPCError // non-nil when Kind == KindRPC
}

func (e *Error) Error() string {
	if e.RPC != nil {
		return fmt.Sprintf("mcpclient %s: %s", e.Op, e.RPC)
	}
	return fmt.Sprintf("mcpclient %s [%s]: %v", e.Op, e.Kind, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

// Tool is one entry from tools/list.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

// ContentItem is one element of a tools/call result content array.
type ContentItem struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// CallResult is the decoded result of tools/call.
type CallResult struct {
	IsError bool
	Content []ContentItem
	Raw     json.RawMessage
}

// Client talks JSON-RPC 2.0 to one MCP endpoint.
type Client struct {
	// Endpoint is the seller's MCP URL (the full POST target).
	Endpoint string
	// Timeout is the per-request HTTP timeout; DefaultTimeout when <= 0.
	Timeout time.Duration
	// BearerToken, when non-empty, is sent as Authorization: Bearer.
	BearerToken string
	// HTTPClient overrides the default client (used by tests).
	HTTPClient *http.Client
	// Logger receives secret-safe debug lines; nil disables logging.
	Logger *log.Logger

	nextID atomic.Int64
}

// CallOption tweaks a single CallTool invocation.
type CallOption func(*callConfig)

type callConfig struct {
	headers map[string]string
}

// WithHeaders adds per-call HTTP headers (e.g. Signature, Signature-Input
// for RFC 9421 signing probes).
func WithHeaders(h map[string]string) CallOption {
	return func(c *callConfig) {
		for k, v := range h {
			c.headers[k] = v
		}
	}
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &http.Client{Timeout: timeout}
}

// ListTools calls tools/list and returns the advertised tool surface.
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	raw, err := c.doRPC(ctx, "ListTools", "tools/list", map[string]any{}, nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Tools []Tool `json:"tools"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, &Error{Kind: KindProtocol, Op: "ListTools", Err: fmt.Errorf("decode tools/list result: %w", err)}
	}
	return out.Tools, nil
}

// CallTool calls tools/call for name with args and returns the result.
// A structured JSON-RPC error from the server comes back as *Error with
// Kind == KindRPC (not as a Go error from the tool itself).
func (c *Client) CallTool(ctx context.Context, name string, args any, opts ...CallOption) (*CallResult, error) {
	cfg := &callConfig{headers: map[string]string{}}
	for _, o := range opts {
		o(cfg)
	}
	if args == nil {
		args = map[string]any{}
	}
	params := map[string]any{"name": name, "arguments": args}
	raw, err := c.doRPC(ctx, "CallTool", "tools/call", params, cfg.headers)
	if err != nil {
		return nil, err
	}
	var out struct {
		IsError bool          `json:"isError"`
		Content []ContentItem `json:"content"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, &Error{Kind: KindProtocol, Op: "CallTool", Err: fmt.Errorf("decode tools/call result: %w", err)}
	}
	return &CallResult{IsError: out.IsError, Content: out.Content, Raw: raw}, nil
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *RPCError       `json:"error"`
}

func (c *Client) doRPC(ctx context.Context, op, method string, params any, headers map[string]string) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		return nil, &Error{Kind: KindProtocol, Op: op, Err: fmt.Errorf("encode request: %w", err)}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, &Error{Kind: KindTransport, Op: op, Err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if c.BearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.BearerToken)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	start := time.Now()
	c.logf("%s %s -> %s (id=%d)", op, method, redactURL(c.Endpoint), id)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		c.logf("%s %s transport failure after %s: %v", op, method, time.Since(start).Round(time.Millisecond), err)
		return nil, &Error{Kind: KindTransport, Op: op, Err: err}
	}
	defer resp.Body.Close()

	rawBody, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, &Error{Kind: KindTransport, Op: op, Err: fmt.Errorf("read response body: %w", err)}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		c.logf("%s %s HTTP %d after %s", op, method, resp.StatusCode, time.Since(start).Round(time.Millisecond))
		return nil, &Error{
			Kind: KindTransport, Op: op,
			Err: fmt.Errorf("HTTP %d from endpoint (expected 2xx)", resp.StatusCode),
		}
	}

	payload := rawBody
	if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "text/event-stream") {
		payload = firstSSEData(rawBody)
		if payload == nil {
			return nil, &Error{Kind: KindProtocol, Op: op, Err: fmt.Errorf("SSE response contained no data frame")}
		}
	}

	var rr rpcResponse
	dec := json.NewDecoder(bytes.NewReader(payload))
	if err := dec.Decode(&rr); err != nil {
		c.logf("%s %s non-JSON-RPC response after %s (content-type=%q, %d bytes)",
			op, method, time.Since(start).Round(time.Millisecond), resp.Header.Get("Content-Type"), len(payload))
		return nil, &Error{
			Kind: KindProtocol, Op: op,
			Err: fmt.Errorf("response is not JSON-RPC 2.0 (content-type %q)", resp.Header.Get("Content-Type")),
		}
	}
	if rr.JSONRPC != "2.0" {
		return nil, &Error{Kind: KindProtocol, Op: op, Err: fmt.Errorf("response jsonrpc=%q, want \"2.0\"", rr.JSONRPC)}
	}
	if rr.Error != nil {
		c.logf("%s %s rpc error %d after %s", op, method, rr.Error.Code, time.Since(start).Round(time.Millisecond))
		return nil, &Error{Kind: KindRPC, Op: op, Err: rr.Error, RPC: rr.Error}
	}
	if len(rr.Result) == 0 {
		return nil, &Error{Kind: KindProtocol, Op: op, Err: fmt.Errorf("JSON-RPC response has neither result nor error")}
	}
	c.logf("%s %s ok after %s", op, method, time.Since(start).Round(time.Millisecond))
	return rr.Result, nil
}

// firstSSEData extracts the first "data:" frame payload from an
// SSE/text-event-stream body. Returns nil when no data frame exists.
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

func (c *Client) logf(format string, args ...any) {
	if c.Logger != nil {
		c.Logger.Printf(format, args...)
	}
}

// ---------------------------------------------------------------------------
// Secret-safe logging helpers
// ---------------------------------------------------------------------------

// sensitiveHeader reports whether a header value must never be logged raw.
func sensitiveHeader(name string) bool {
	switch strings.ToLower(name) {
	case "authorization", "proxy-authorization", "signature", "signature-input", "signature-agent":
		return true
	}
	l := strings.ToLower(name)
	return strings.Contains(l, "token") || strings.Contains(l, "api-key") || strings.Contains(l, "apikey")
}

// RedactHeaders returns a copy of h with sensitive values replaced.
func RedactHeaders(h http.Header) http.Header {
	out := make(http.Header, len(h))
	for k, vs := range h {
		if sensitiveHeader(k) {
			out[k] = []string{"[REDACTED]"}
			continue
		}
		cp := make([]string, len(vs))
		copy(cp, vs)
		out[k] = cp
	}
	return out
}

// redactURL strips the query string so embedded credentials (e.g.
// ?apiKey=...) never reach the log.
func redactURL(raw string) string {
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		return raw[:i] + "?[REDACTED]"
	}
	return raw
}

var sensitiveJSONKeys = []string{
	"token", "secret", "password", "authorization", "signature",
	"api_key", "apikey", "access_key", "private_key", "session",
}

// RedactJSON recursively replaces values of token-like keys in a JSON
// document with "[REDACTED]". Non-JSON input is returned unchanged.
func RedactJSON(b []byte) []byte {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return b
	}
	redactValue(v)
	out, err := json.Marshal(v)
	if err != nil {
		return b
	}
	return out
}

func redactValue(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			lk := strings.ToLower(k)
			sensitive := false
			for _, s := range sensitiveJSONKeys {
				if strings.Contains(lk, s) {
					sensitive = true
					break
				}
			}
			if sensitive {
				t[k] = "[REDACTED]"
			} else {
				redactValue(val)
			}
		}
	case []any:
		for _, item := range t {
			redactValue(item)
		}
	}
}
