package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeMCP is a synthetic MCP endpoint for tests. Never real seller data.
type fakeMCP struct {
	t              *testing.T
	requireBearer  string
	seenAuthHeader string
	seenSigInput   string
	seenSignature  string
	mode           string // "", "sse", "html", "slow", "rpc-error"
}

func (f *fakeMCP) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.seenAuthHeader = r.Header.Get("Authorization")
		f.seenSigInput = r.Header.Get("Signature-Input")
		f.seenSignature = r.Header.Get("Signature")
		if f.requireBearer != "" && f.seenAuthHeader != "Bearer "+f.requireBearer {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": 1,
				"error": map[string]any{"code": -32000, "message": "missing bearer token"},
			})
			return
		}
		switch f.mode {
		case "sse":
			w.Header().Set("Content-Type", "text/event-stream")
			envelope := `{"jsonrpc":"2.0","id":1,"result":` + toolsListResult() + `}`
			_, _ = w.Write([]byte(": keep-alive\n\ndata: " + envelope + "\n\n"))
			return
		case "html":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html><body>Bad Gateway</body></html>"))
			return
		case "slow":
			time.Sleep(2 * time.Second)
		}

		var req struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      any             `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		switch req.Method {
		case "tools/list":
			resp["result"] = json.RawMessage(toolsListResult())
		case "tools/call":
			var p struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &p)
			if p.Name == "no_such_tool" {
				resp["error"] = map[string]any{"code": -32601, "message": "Method not found"}
			} else {
				resp["result"] = map[string]any{
					"content": []map[string]any{{"type": "text", "text": "ok:" + p.Name}},
				}
			}
		default:
			resp["error"] = map[string]any{"code": -32601, "message": "Method not found"}
		}
		if f.mode == "rpc-error" {
			resp = map[string]any{"jsonrpc": "2.0", "id": req.ID,
				"error": map[string]any{"code": -32601, "message": "Method not found"}}
		}
		_ = json.NewEncoder(w).Encode(resp)
	}
}

func toolsListResult() string {
	return `{"tools":[{"name":"get_products","description":"discover","inputSchema":{"type":"object","required":["brief"]}},{"name":"create_media_buy","description":"buy","inputSchema":{"type":"object"}}]}`
}

func newClient(t *testing.T, f *fakeMCP) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	return &Client{Endpoint: srv.URL, Timeout: 5 * time.Second}, srv
}

func TestListTools(t *testing.T) {
	f := &fakeMCP{}
	c, _ := newClient(t, f)
	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 2 || tools[0].Name != "get_products" || tools[1].Name != "create_media_buy" {
		t.Fatalf("unexpected tools: %+v", tools)
	}
	var schema map[string]any
	if err := json.Unmarshal(tools[0].InputSchema, &schema); err != nil {
		t.Fatalf("inputSchema not JSON: %v", err)
	}
	if schema["type"] != "object" {
		t.Fatalf("schema type = %v, want object", schema["type"])
	}
}

func TestCallToolSuccess(t *testing.T) {
	f := &fakeMCP{}
	c, _ := newClient(t, f)
	res, err := c.CallTool(context.Background(), "get_products", map[string]any{"brief": "probe"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatal("IsError = true, want false")
	}
	if len(res.Content) != 1 || !strings.HasPrefix(res.Content[0].Text, "ok:get_products") {
		t.Fatalf("unexpected content: %+v", res.Content)
	}
}

func TestCallToolRPCError(t *testing.T) {
	f := &fakeMCP{}
	c, _ := newClient(t, f)
	_, err := c.CallTool(context.Background(), "no_such_tool", nil)
	var cerr *Error
	if !errors.As(err, &cerr) {
		t.Fatalf("err = %v (%T), want *Error", err, err)
	}
	if cerr.Kind != KindRPC {
		t.Fatalf("kind = %s, want rpc", cerr.Kind)
	}
	if cerr.RPC == nil || cerr.RPC.Code != -32601 {
		t.Fatalf("rpc error = %+v, want code -32601", cerr.RPC)
	}
}

func TestBearerTokenSent(t *testing.T) {
	f := &fakeMCP{requireBearer: "s3cret"}
	c, _ := newClient(t, f)
	c.BearerToken = "s3cret"
	if _, err := c.ListTools(context.Background()); err != nil {
		t.Fatalf("ListTools with bearer: %v", err)
	}
	if f.seenAuthHeader != "Bearer s3cret" {
		t.Fatalf("server saw Authorization %q", f.seenAuthHeader)
	}
}

func TestBearerTokenMissingRejected(t *testing.T) {
	f := &fakeMCP{requireBearer: "s3cret"}
	c, _ := newClient(t, f)
	_, err := c.ListTools(context.Background())
	var cerr *Error
	if !errors.As(err, &cerr) || cerr.Kind != KindRPC {
		t.Fatalf("err = %v, want rpc error for missing bearer", err)
	}
}

func TestCustomHeadersPassedThrough(t *testing.T) {
	f := &fakeMCP{}
	c, _ := newClient(t, f)
	_, err := c.CallTool(context.Background(), "get_products", nil,
		WithHeaders(map[string]string{
			"Signature-Input": `sig1=("@method" "@target-uri")`,
			"Signature":       `sig1=:aGVsbG8=:`,
		}))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if f.seenSigInput == "" || f.seenSignature == "" {
		t.Fatalf("signature headers not seen by server: input=%q sig=%q", f.seenSigInput, f.seenSignature)
	}
}

func TestTransportError(t *testing.T) {
	c := &Client{Endpoint: "http://127.0.0.1:1/nope", Timeout: time.Second}
	_, err := c.ListTools(context.Background())
	var cerr *Error
	if !errors.As(err, &cerr) || cerr.Kind != KindTransport {
		t.Fatalf("err = %v, want transport error", err)
	}
}

func TestProtocolErrorOnHTML(t *testing.T) {
	f := &fakeMCP{mode: "html"}
	c, _ := newClient(t, f)
	_, err := c.ListTools(context.Background())
	var cerr *Error
	if !errors.As(err, &cerr) || cerr.Kind != KindProtocol {
		t.Fatalf("err = %v, want protocol error for HTML response", err)
	}
}

func TestTimeout(t *testing.T) {
	f := &fakeMCP{mode: "slow"}
	c, _ := newClient(t, f)
	c.Timeout = 200 * time.Millisecond
	_, err := c.ListTools(context.Background())
	var cerr *Error
	if !errors.As(err, &cerr) || cerr.Kind != KindTransport {
		t.Fatalf("err = %v, want transport (timeout) error", err)
	}
}

func TestSSEResponseUnwrapped(t *testing.T) {
	f := &fakeMCP{mode: "sse"}
	c, _ := newClient(t, f)
	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools over SSE: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("tools = %d, want 2", len(tools))
	}
}

func TestRedactHeaders(t *testing.T) {
	h := http.Header{
		"Authorization":   {"Bearer s3cret"},
		"Signature":       {"sig1=:abc=:"},
		"Signature-Input": {"sig1=()"},
		"Content-Type":    {"application/json"},
	}
	red := RedactHeaders(h)
	for _, k := range []string{"Authorization", "Signature", "Signature-Input"} {
		if got := red.Get(k); got != "[REDACTED]" {
			t.Fatalf("%s = %q, want [REDACTED]", k, got)
		}
		if orig := h.Get(k); strings.Contains(orig, "[REDACTED]") {
			t.Fatalf("original header %s was mutated", k)
		}
	}
	if red.Get("Content-Type") != "application/json" {
		t.Fatalf("non-sensitive header was redacted: %q", red.Get("Content-Type"))
	}
}

func TestRedactJSON(t *testing.T) {
	in := []byte(`{"name":"x","auth_token":"s3cret","nested":{"password":"pw"},"list":[{"secret":"s"}]}`)
	out := RedactJSON(in)
	s := string(out)
	for _, leak := range []string{"s3cret", "pw"} {
		if strings.Contains(s, leak) {
			t.Fatalf("redacted JSON leaks %q: %s", leak, s)
		}
	}
	if strings.Count(s, "[REDACTED]") != 3 {
		t.Fatalf("expected 3 redactions, got: %s", s)
	}
	var v map[string]any
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("redacted output not JSON: %v", err)
	}
	if v["name"] != "x" {
		t.Fatalf("non-sensitive field altered: %s", s)
	}
}

func TestLoggingIsSecretSafe(t *testing.T) {
	var sb strings.Builder
	f := &fakeMCP{requireBearer: "s3cret"}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	c := &Client{Endpoint: srv.URL + "?apiKey=topsecret", Timeout: 5 * time.Second,
		BearerToken: "s3cret", Logger: log.New(&sb, "", 0)}
	if _, err := c.ListTools(context.Background()); err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	logged := sb.String()
	for _, leak := range []string{"s3cret", "topsecret"} {
		if strings.Contains(logged, leak) {
			t.Fatalf("log leaks secret %q:\n%s", leak, logged)
		}
	}
	if !strings.Contains(logged, "tools/list") {
		t.Fatalf("expected tools/list in log, got:\n%s", logged)
	}
}
