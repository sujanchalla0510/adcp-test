package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sujanchalla0510/adcp-test/internal/mcpclient"
)

// fakeSeller is a synthetic AdCP seller for tests. Behavior is controlled
// by mode; no real seller data is involved.
type fakeSeller struct {
	mode string // "", "missing-tools", "media-buy-only", "html", "accepts-unsigned", "no-list", "validation-only"
}

func fullToolList() []map[string]any {
	return []map[string]any{
		{"name": "get_adcp_capabilities", "inputSchema": map[string]any{"type": "object"}},
		{"name": "get_products", "inputSchema": map[string]any{"type": "object", "required": []string{"buying_mode"}}},
		{"name": "list_creative_formats", "inputSchema": map[string]any{"type": "object"}},
		{"name": "create_media_buy", "inputSchema": map[string]any{"type": "object", "required": []string{"idempotency_key", "account", "brand", "start_time", "end_time"}}},
		{"name": "update_media_buy", "inputSchema": map[string]any{"type": "object"}},
		{"name": "get_media_buys", "inputSchema": map[string]any{"type": "object"}},
		{"name": "sync_creatives", "inputSchema": map[string]any{"type": "object"}},
		{"name": "sync_catalogs", "inputSchema": map[string]any{"type": "object"}},
		{"name": "list_creatives", "inputSchema": map[string]any{"type": "object"}},
		{"name": "get_media_buy_delivery", "inputSchema": map[string]any{"type": "object"}},
		{"name": "provide_performance_feedback", "inputSchema": map[string]any{"type": "object"}},
		{"name": "get_signals", "inputSchema": map[string]any{"type": "object"}},
	}
}

// mediaBuyToolList is the media-buy agent profile surface: capabilities
// plus the six media-buy tasks (no creative/signals/aux tools).
func mediaBuyToolList() []map[string]any {
	return []map[string]any{
		{"name": "get_adcp_capabilities", "inputSchema": map[string]any{"type": "object"}},
		{"name": "get_products", "inputSchema": map[string]any{"type": "object", "required": []string{"buying_mode"}}},
		{"name": "create_media_buy", "inputSchema": map[string]any{"type": "object", "required": []string{"idempotency_key", "account", "brand", "start_time", "end_time"}}},
		{"name": "update_media_buy", "inputSchema": map[string]any{"type": "object"}},
		{"name": "get_media_buys", "inputSchema": map[string]any{"type": "object"}},
		{"name": "get_media_buy_delivery", "inputSchema": map[string]any{"type": "object"}},
		{"name": "provide_performance_feedback", "inputSchema": map[string]any{"type": "object"}},
	}
}

func (f *fakeSeller) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if f.mode == "html" {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html><body>502 Bad Gateway</body></html>"))
			return
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
		fail := func(code int, msg string) {
			resp["error"] = map[string]any{"code": code, "message": msg}
		}
		switch req.Method {
		case "tools/list":
			if f.mode == "no-list" {
				fail(-32601, "Method not found")
				break
			}
			tools := fullToolList()
			switch f.mode {
			case "missing-tools":
				tools = tools[:1] // only get_adcp_capabilities
			case "media-buy-only":
				tools = mediaBuyToolList()
			}
			resp["result"] = map[string]any{"tools": tools}
		case "tools/call":
			var p struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &p)
			switch p.Name {
			case "get_products":
				resp["result"] = map[string]any{"content": []map[string]any{{"type": "text", "text": `{"products":[]}`}}}
			case "create_media_buy":
				sigIn, sig := r.Header.Get("Signature-Input"), r.Header.Get("Signature")
				switch {
				case f.mode == "accepts-unsigned":
					resp["result"] = map[string]any{"content": []map[string]any{{"type": "text", "text": `{"media_buy_id":"mb_1"}`}}}
				case f.mode == "validation-only":
					// Rejects everything for argument reasons, never for
					// auth: the probes must FAIL here (they cannot
					// distinguish auth enforcement from validation).
					fail(-32602, "invalid params: account 'adcp-test-probe-account' is not a known account id")
				case sigIn == "" || sig == "":
					fail(-32001, "missing required Signature-Input/Signature headers: request must be RFC 9421 signed")
				case strings.Contains(sig, "!!!") || strings.Contains(sigIn, "notanumber"):
					fail(-32002, "malformed Signature-Input: could not parse signature parameters")
				default:
					resp["result"] = map[string]any{"content": []map[string]any{{"type": "text", "text": `{"media_buy_id":"mb_1"}`}}}
				}
			case probeUnknownTool:
				fail(-32601, "Method not found")
			default:
				fail(-32601, "Method not found")
			}
		default:
			fail(-32601, "Method not found")
		}
		_ = json.NewEncoder(w).Encode(resp)
	}
}

func runAgainst(t *testing.T, mode string) *Report {
	return runAgainstProfile(t, mode, "")
}

func runAgainstProfile(t *testing.T, mode, profile string) *Report {
	t.Helper()
	srv := httptest.NewServer((&fakeSeller{mode: mode}).handler())
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rep, err := Run(ctx, srv.URL, Options{Timeout: 5 * time.Second, Profile: profile})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return rep
}

func checkByName(rep *Report, name string) CheckResult {
	for _, c := range rep.Checks {
		if c.Name == name {
			return c
		}
	}
	return CheckResult{Name: name, Status: "MISSING"}
}

func TestRunAllPass(t *testing.T) {
	rep := runAgainst(t, "")
	if !rep.AllPassed() {
		for _, c := range rep.Checks {
			if c.Status != StatusPass {
				t.Errorf("check %s = %s: %s", c.Name, c.Status, c.Detail)
			}
		}
		t.Fatalf("expected all checks to pass; summary=%+v", rep.Summary)
	}
	// 1 surface + 18 schema (11 required + 7 optional) + 3 auth + 2 error = 24 checks.
	if rep.Summary.Total != 24 {
		t.Fatalf("total checks = %d, want 24", rep.Summary.Total)
	}
	if c := checkByName(rep, "auth:unsigned-mutating-call-rejected"); c.Status != StatusPass {
		t.Fatalf("unsigned probe = %s: %s", c.Status, c.Detail)
	} else if !strings.Contains(strings.ToLower(c.Detail), "auth") && !strings.Contains(strings.ToLower(c.Detail), "sign") {
		t.Fatalf("unsigned probe detail should note auth enforcement: %s", c.Detail)
	}
	if c := checkByName(rep, "auth:malformed-signature-rejected"); c.Status != StatusPass {
		t.Fatalf("malformed signature probe = %s: %s", c.Status, c.Detail)
	}
	if c := checkByName(rep, "auth:get-products-behavior"); c.Status != StatusPass {
		t.Fatalf("get_products behavior = %s: %s", c.Status, c.Detail)
	}
	if c := checkByName(rep, "errors:unknown-tool-structured"); c.Status != StatusPass {
		t.Fatalf("unknown tool = %s: %s", c.Status, c.Detail)
	}
}

func TestRunMissingTools(t *testing.T) {
	rep := runAgainst(t, "missing-tools")
	if c := checkByName(rep, "tool-surface"); c.Status != StatusFail {
		t.Fatalf("tool-surface = %s, want fail: %s", c.Status, c.Detail)
	} else if !strings.Contains(c.Detail, "create_media_buy") {
		t.Fatalf("surface detail should name missing tools: %s", c.Detail)
	}
	for _, n := range []string{"auth:unsigned-mutating-call-rejected", "auth:malformed-signature-rejected", "schema:create_media_buy"} {
		if c := checkByName(rep, n); c.Status != StatusSkip {
			t.Fatalf("%s = %s, want skip: %s", n, c.Status, c.Detail)
		}
	}
	if rep.AllPassed() {
		t.Fatal("AllPassed = true despite missing tools")
	}
}

func TestRunHTMLResponse(t *testing.T) {
	rep := runAgainst(t, "html")
	if c := checkByName(rep, "tool-surface"); c.Status != StatusFail {
		t.Fatalf("tool-surface = %s, want fail", c.Status)
	} else if !strings.Contains(strings.ToLower(c.Detail), "protocol") {
		t.Fatalf("surface detail should mention protocol failure: %s", c.Detail)
	}
	if c := checkByName(rep, "errors:unknown-tool-structured"); c.Status != StatusFail {
		t.Fatalf("unknown-tool taxonomy = %s, want fail on HTML response", c.Status)
	}
}

func TestRunAcceptsUnsignedMutation(t *testing.T) {
	rep := runAgainst(t, "accepts-unsigned")
	c := checkByName(rep, "auth:unsigned-mutating-call-rejected")
	if c.Status != StatusFail {
		t.Fatalf("unsigned probe = %s, want fail when seller accepts unsigned mutations: %s", c.Status, c.Detail)
	}
}

func TestRunValidationOnlyAuthProbesFail(t *testing.T) {
	// A seller that rejects everything for argument-validation reasons
	// must NOT pass the auth probes: the probes cannot distinguish auth
	// enforcement from validation.
	rep := runAgainst(t, "validation-only")
	for _, n := range []string{"auth:unsigned-mutating-call-rejected", "auth:malformed-signature-rejected"} {
		c := checkByName(rep, n)
		if c.Status != StatusFail {
			t.Fatalf("%s = %s, want fail: %s", n, c.Status, c.Detail)
		}
		if !strings.Contains(strings.ToLower(c.Detail), "cannot confirm") {
			t.Fatalf("%s detail should explain the ambiguity: %s", n, c.Detail)
		}
	}
	if rep.AllPassed() {
		t.Fatal("AllPassed = true despite ambiguous auth probes")
	}
}

func TestProfileMediaBuy(t *testing.T) {
	// A media-buy-profile seller passes the media-buy profile.
	rep := runAgainstProfile(t, "media-buy-only", "media-buy")
	if c := checkByName(rep, "tool-surface"); c.Status != StatusPass {
		t.Fatalf("media-buy profile tool-surface = %s, want pass: %s", c.Status, c.Detail)
	}
	// The same seller fails the full profile (missing the rest).
	rep = runAgainstProfile(t, "media-buy-only", "full")
	if c := checkByName(rep, "tool-surface"); c.Status != StatusFail {
		t.Fatalf("full profile tool-surface = %s, want fail: %s", c.Status, c.Detail)
	} else if !strings.Contains(c.Detail, "list_creative_formats") {
		t.Fatalf("full profile detail should name missing tools: %s", c.Detail)
	}
	// And fails the signals profile (missing get_signals).
	rep = runAgainstProfile(t, "media-buy-only", "signals")
	if c := checkByName(rep, "tool-surface"); c.Status != StatusFail {
		t.Fatalf("signals profile tool-surface = %s, want fail: %s", c.Status, c.Detail)
	} else if !strings.Contains(c.Detail, "get_signals") {
		t.Fatalf("signals profile detail should name get_signals: %s", c.Detail)
	}
}

func TestUnknownProfile(t *testing.T) {
	srv := httptest.NewServer((&fakeSeller{mode: ""}).handler())
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := Run(ctx, srv.URL, Options{Profile: "bogus"}); err == nil {
		t.Fatal("Run with unknown profile: expected error, got nil")
	}
}

func TestRunInvalidTarget(t *testing.T) {
	for _, target := range []string{"", "not-a-url", "ftp://example.com/x", "http://"} {
		if _, err := Run(context.Background(), target, Options{}); err == nil {
			t.Fatalf("Run(%q): expected error, got nil", target)
		}
	}
}

func TestRunUnreachableTarget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rep, err := Run(ctx, "http://127.0.0.1:1/nope", Options{Timeout: time.Second})
	if err != nil {
		t.Fatalf("Run against unreachable target should return a report, got err: %v", err)
	}
	if c := checkByName(rep, "tool-surface"); c.Status != StatusFail {
		t.Fatalf("tool-surface = %s, want fail", c.Status)
	} else if !strings.Contains(strings.ToLower(c.Detail), "transport") {
		t.Fatalf("detail should mention transport failure: %s", c.Detail)
	}
	if rep.AllPassed() {
		t.Fatal("AllPassed = true for unreachable target")
	}
}

func TestReportJSONRoundTrip(t *testing.T) {
	rep := runAgainst(t, "")
	data, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	var decoded struct {
		TargetURL string `json:"target_url"`
		Summary   struct {
			Total  int `json:"total"`
			Passed int `json:"passed"`
			Failed int `json:"failed"`
		} `json:"summary"`
		Checks []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	if decoded.TargetURL == "" || decoded.Summary.Total == 0 || len(decoded.Checks) == 0 {
		t.Fatalf("report JSON missing fields: %s", data[:200])
	}
	if decoded.Summary.Failed != 0 {
		t.Fatalf("failed = %d, want 0", decoded.Summary.Failed)
	}
}

func TestValidateInputSchema(t *testing.T) {
	exp := ExpectedTool{Name: "get_products", Required: true, ExpectedRequired: []string{"buying_mode"}}
	if probs := validateInputSchema(exp, []byte(`{"type":"object","required":["buying_mode"]}`)); len(probs) != 0 {
		t.Fatalf("valid schema rejected: %v", probs)
	}
	if probs := validateInputSchema(exp, []byte(`{"type":"object"}`)); len(probs) == 0 {
		t.Fatal("schema missing required field accepted")
	} else if !strings.Contains(probs[0], `"buying_mode"`) {
		t.Fatalf("problem should name the missing field: %v", probs)
	}
	if probs := validateInputSchema(exp, []byte(`{"type":"array"}`)); len(probs) == 0 {
		t.Fatal("non-object schema accepted")
	}
	if probs := validateInputSchema(exp, []byte(`not json`)); len(probs) == 0 {
		t.Fatal("invalid JSON schema accepted")
	}
	// Tool with no expected required fields and no schema is fine.
	exp2 := ExpectedTool{Name: "list_creatives", Required: true}
	if probs := validateInputSchema(exp2, nil); len(probs) != 0 {
		t.Fatalf("empty schema for unconstrained tool rejected: %v", probs)
	}
}

func TestExpectedSurfaceCoversCore(t *testing.T) {
	required := map[string]bool{}
	for _, e := range CoreTools {
		if e.Required {
			required[e.Name] = true
		}
	}
	for _, want := range []string{
		"get_adcp_capabilities", "get_products", "list_creative_formats",
		"create_media_buy", "update_media_buy", "get_media_buys",
		"sync_creatives", "sync_catalogs", "list_creatives",
		"get_media_buy_delivery", "provide_performance_feedback",
	} {
		if !required[want] {
			t.Fatalf("core tool %s not marked required", want)
		}
	}
}

func TestDescribeClientError(t *testing.T) {
	rpcErr := &mcpclient.Error{Kind: mcpclient.KindRPC, Op: "CallTool",
		Err: &mcpclient.RPCError{Code: -32601, Message: "Method not found"},
		RPC: &mcpclient.RPCError{Code: -32601, Message: "Method not found"}}
	if got := describeClientError("probe", rpcErr); !strings.Contains(got, "-32601") {
		t.Fatalf("rpc error not described: %s", got)
	}
	var notClient = errors.New("boom")
	if got := describeClientError("probe", notClient); !strings.Contains(got, "boom") {
		t.Fatalf("plain error not described: %s", got)
	}
}
