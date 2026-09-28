package mockserver

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sujanchalla0510/adcp-test/internal/mockcfg"
)

// newTestRand is a deterministic RNG for distribution tests.
func newTestRand(seed int64) *rand.Rand {
	return rand.New(rand.NewSource(seed))
}

// startTestServer builds a mock Server from svc and starts it on an
// ephemeral localhost port.
func startTestServer(t *testing.T, svc mockcfg.MockService, opts Options) *Server {
	t.Helper()
	svc.Listen = ":0"
	srv, err := New(svc, opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

// rpcResult is a decoded JSON-RPC response envelope.
type rpcResult struct {
	Result any
	Err    map[string]any
	Raw    []byte
}

// call posts one JSON-RPC request and decodes the envelope.
func call(t *testing.T, url, method string, params any) rpcResult {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 7, "method": method, "params": params,
	})
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	raw := new(bytes.Buffer)
	_, _ = raw.ReadFrom(resp.Body)
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  map[string]any  `json:"error"`
	}
	if err := json.Unmarshal(raw.Bytes(), &env); err != nil {
		return rpcResult{Raw: raw.Bytes()}
	}
	var res any
	if len(env.Result) > 0 {
		_ = json.Unmarshal(env.Result, &res)
	}
	return rpcResult{Result: res, Err: env.Error, Raw: raw.Bytes()}
}

func callTool(t *testing.T, url, tool string, args map[string]any) rpcResult {
	t.Helper()
	return call(t, url, "tools/call", map[string]any{"name": tool, "arguments": args})
}

func mustNoError(t *testing.T, r rpcResult) any {
	t.Helper()
	if r.Err != nil {
		t.Fatalf("unexpected RPC error: %v", r.Err)
	}
	return r.Result
}

func TestInlineAndNoMatch(t *testing.T) {
	srv := startTestServer(t, mockcfg.MockService{
		Name: "s",
		Routes: []mockcfg.Route{{
			Match:   mockcfg.Match{Tool: "get_products"},
			Respond: mockcfg.Respond{Inline: map[string]any{"products": []any{"p1"}}},
		}},
	}, Options{RandSeed: 1})

	res := mustNoError(t, callTool(t, srv.URL(), "get_products", nil))
	m := res.(map[string]any)
	if m["products"].([]any)[0] != "p1" {
		t.Fatalf("result = %v", res)
	}

	r := callTool(t, srv.URL(), "nope", nil)
	if r.Err == nil {
		t.Fatalf("expected error for unknown tool")
	}
	if int(r.Err["code"].(float64)) != -32601 {
		t.Fatalf("code = %v, want -32601", r.Err["code"])
	}
}

func TestMatchingSemantics(t *testing.T) {
	svc := mockcfg.MockService{Name: "s", Routes: []mockcfg.Route{
		{
			Match:   mockcfg.Match{Tool: "buy", Args: map[string]any{"buyer_ref": "acme*"}},
			Respond: mockcfg.Respond{Inline: map[string]any{"route": "acme"}},
		},
		{
			Match:   mockcfg.Match{Tool: "buy", Args: map[string]any{"creative": map[string]any{"id": "c-??"}}},
			Respond: mockcfg.Respond{Inline: map[string]any{"route": "creative"}},
		},
		{
			Match:   mockcfg.Match{Tool: "buy", Args: map[string]any{"flight.budget": 100}},
			Respond: mockcfg.Respond{Inline: map[string]any{"route": "dotted"}},
		},
		{
			Match:   mockcfg.Match{Tool: "buy"},
			Respond: mockcfg.Respond{Inline: map[string]any{"route": "default"}},
		},
	}}
	srv := startTestServer(t, svc, Options{RandSeed: 1})

	cases := []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"buyer_ref": "acme-corp"}, "acme"},
		{map[string]any{"buyer_ref": "other"}, "default"}, // glob miss falls through
		{map[string]any{"creative": map[string]any{"id": "c-42"}}, "creative"},
		{map[string]any{"creative": map[string]any{"id": "c-421"}}, "default"}, // ?? = exactly 2
		{map[string]any{"flight": map[string]any{"budget": 100}}, "dotted"},
		{map[string]any{"flight": map[string]any{"budget": 100.0}}, "dotted"}, // int pattern vs float arg
		{map[string]any{"flight": map[string]any{"budget": 99}}, "default"},
		{nil, "default"},
	}
	for i, c := range cases {
		res := mustNoError(t, callTool(t, srv.URL(), "buy", c.args))
		got := res.(map[string]any)["route"]
		if got != c.want {
			t.Errorf("case %d: route = %v, want %s (args %v)", i, got, c.want, c.args)
		}
	}
}

func TestSequenceOrder(t *testing.T) {
	srv := startTestServer(t, mockcfg.MockService{
		Name: "s",
		Routes: []mockcfg.Route{{
			Match: mockcfg.Match{Tool: "next"},
			Respond: mockcfg.Respond{Sequence: []mockcfg.SequenceItem{
				{Inline: map[string]any{"n": 1}},
				{Inline: map[string]any{"n": 2}},
			}},
		}},
	}, Options{RandSeed: 1})

	for i, want := range []float64{1, 2, 2, 2} { // nth call -> nth response, then clamp
		res := mustNoError(t, callTool(t, srv.URL(), "next", nil))
		if got := res.(map[string]any)["n"]; got != want {
			t.Fatalf("call %d: n = %v, want %v", i+1, got, want)
		}
	}
}

func TestFileResponse(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "products.json"), []byte(`{"products":[{"id":"p9"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := startTestServer(t, mockcfg.MockService{
		Name: "s",
		Routes: []mockcfg.Route{{
			Match:   mockcfg.Match{Tool: "get_products"},
			Respond: mockcfg.Respond{File: "products.json"},
		}},
	}, Options{RandSeed: 1, BaseDir: dir})

	res := mustNoError(t, callTool(t, srv.URL(), "get_products", nil))
	prods := res.(map[string]any)["products"].([]any)
	if prods[0].(map[string]any)["id"] != "p9" {
		t.Fatalf("result = %v", res)
	}
}

func TestTemplateEcho(t *testing.T) {
	dir := t.TempDir()
	tmpl := `{"buyer":"{{args.buyer_ref}}","tool":"{{tool}}","call":{{call}},"at":"{{now}}","creative":"{{args.creative.id}}","count":{{args.count}},"id":{{id}}}`
	if err := os.WriteFile(filepath.Join(dir, "buy.json"), []byte(tmpl), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := startTestServer(t, mockcfg.MockService{
		Name: "s",
		Routes: []mockcfg.Route{{
			Match:   mockcfg.Match{Tool: "create_media_buy"},
			Respond: mockcfg.Respond{Template: "buy.json"},
		}},
	}, Options{RandSeed: 1, BaseDir: dir})

	args := map[string]any{"buyer_ref": "acme", "creative": map[string]any{"id": "c1"}, "count": 3}
	res := mustNoError(t, callTool(t, srv.URL(), "create_media_buy", args)).(map[string]any)
	if res["buyer"] != "acme" || res["tool"] != "create_media_buy" || res["creative"] != "c1" {
		t.Fatalf("echo mismatch: %v", res)
	}
	if res["call"] != float64(1) {
		t.Fatalf("call = %v, want 1", res["call"])
	}
	if res["count"] != float64(3) {
		t.Fatalf("count = %v, want 3 (number preserved)", res["count"])
	}
	if res["id"] != float64(7) {
		t.Fatalf("id = %v, want 7 (request id echoed raw)", res["id"])
	}
	if _, err := time.Parse(time.RFC3339, res["at"].(string)); err != nil {
		t.Fatalf("at = %v: %v", res["at"], err)
	}
	// Second call: the counter advances, everything else stays coherent.
	res2 := mustNoError(t, callTool(t, srv.URL(), "create_media_buy", args)).(map[string]any)
	if res2["call"] != float64(2) {
		t.Fatalf("call = %v, want 2", res2["call"])
	}
}

func TestToolsList(t *testing.T) {
	srv := startTestServer(t, mockcfg.MockService{
		Name: "s",
		Routes: []mockcfg.Route{
			{Match: mockcfg.Match{Tool: "a"}, Respond: mockcfg.Respond{Inline: map[string]any{}}},
			{Match: mockcfg.Match{Tool: "b"}, Respond: mockcfg.Respond{Inline: map[string]any{}}},
		},
	}, Options{RandSeed: 1})
	res := mustNoError(t, call(t, srv.URL(), "tools/list", nil))
	tools := res.(map[string]any)["tools"].([]any)
	if len(tools) != 2 || tools[0].(map[string]any)["name"] != "a" {
		t.Fatalf("tools = %v", res)
	}
}

func TestLatencyFixed(t *testing.T) {
	srv := startTestServer(t, mockcfg.MockService{
		Name: "s",
		Routes: []mockcfg.Route{{
			Match:   mockcfg.Match{Tool: "slow"},
			Respond: mockcfg.Respond{Inline: map[string]any{"ok": true}},
			Latency: &mockcfg.Latency{Fixed: "120ms"},
		}},
	}, Options{RandSeed: 1})
	start := time.Now()
	mustNoError(t, callTool(t, srv.URL(), "slow", nil))
	if el := time.Since(start); el < 100*time.Millisecond || el > 5*time.Second {
		t.Fatalf("elapsed = %v, want ~120ms", el)
	}
}

func TestLatencyDistribution(t *testing.T) {
	lm, err := buildLatencyModel(&mockcfg.Latency{P50: "40ms", P99: "250ms"})
	if err != nil {
		t.Fatal(err)
	}
	rng := newTestRand(42)
	const n = 4000
	samples := make([]time.Duration, n)
	for i := range samples {
		samples[i] = lm.sample(rng, 0)
	}
	if med := percentile(samples, 50); med < 30*time.Millisecond || med > 55*time.Millisecond {
		t.Fatalf("median = %v, want ~40ms", med)
	}
	if p99 := percentile(samples, 99); p99 < 180*time.Millisecond || p99 > 400*time.Millisecond {
		t.Fatalf("p99 = %v, want ~250ms", p99)
	}
}

func TestLatencySpike(t *testing.T) {
	lm, err := buildLatencyModel(&mockcfg.Latency{
		Fixed: "10ms",
		Spike: &mockcfg.SpikeProfile{Every: "100ms", For: "50ms", Add: "1s"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rng := newTestRand(1)
	if got := lm.sample(rng, 10*time.Millisecond); got != 1010*time.Millisecond {
		t.Fatalf("in spike: got %v, want 1010ms", got)
	}
	if got := lm.sample(rng, 60*time.Millisecond); got != 10*time.Millisecond {
		t.Fatalf("out of spike: got %v, want 10ms", got)
	}
}

func percentile(samples []time.Duration, p float64) time.Duration {
	sorted := append([]time.Duration(nil), samples...)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j] < sorted[j-1]; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	return sorted[int(p/100*float64(len(sorted)))]
}

func TestFaultInjection(t *testing.T) {
	newFaulty := func(f *mockcfg.Faults) *Server {
		return startTestServer(t, mockcfg.MockService{
			Name: "s",
			Routes: []mockcfg.Route{{
				Match:   mockcfg.Match{Tool: "flaky"},
				Respond: mockcfg.Respond{Inline: map[string]any{"ok": true}},
				Faults:  f,
			}},
		}, Options{RandSeed: 7, TimeoutHang: 300 * time.Millisecond})
	}

	// error_rate = 1: every call fails with -32000.
	srv := newFaulty(&mockcfg.Faults{ErrorRate: 1})
	for i := 0; i < 3; i++ {
		r := callTool(t, srv.URL(), "flaky", nil)
		if r.Err == nil || int(r.Err["code"].(float64)) != -32000 {
			t.Fatalf("call %d: err = %v, want -32000", i, r.Err)
		}
	}

	// malformed_rate = 1: every response is garbage.
	srv = newFaulty(&mockcfg.Faults{MalformedRate: 1})
	r := callTool(t, srv.URL(), "flaky", nil)
	if r.Err != nil || !strings.Contains(string(r.Raw), "not JSON") {
		t.Fatalf("malformed: raw = %q err = %v", r.Raw, r.Err)
	}

	// timeout_rate = 1: the server hangs past the (short test) hang.
	srv = newFaulty(&mockcfg.Faults{TimeoutRate: 1})
	start := time.Now()
	mustNoError(t, callTool(t, srv.URL(), "flaky", nil))
	if el := time.Since(start); el < 250*time.Millisecond {
		t.Fatalf("timeout fault: elapsed %v, want >= hang 300ms", el)
	}

	// error_rate = 0.5: statistically about half fail.
	srv = newFaulty(&mockcfg.Faults{ErrorRate: 0.5})
	failures := 0
	const n = 200
	for i := 0; i < n; i++ {
		if r := callTool(t, srv.URL(), "flaky", nil); r.Err != nil {
			failures++
		}
	}
	if failures < 60 || failures > 140 {
		t.Fatalf("error_rate=0.5: %d/%d failed, want ~100", failures, n)
	}
}

func TestStateMachineOverHTTP(t *testing.T) {
	mkRoute := func(tool string) mockcfg.Route {
		return mockcfg.Route{
			Match:        mockcfg.Match{Tool: tool},
			Respond:      mockcfg.Respond{Inline: map[string]any{"ok": true}},
			StateMachine: mockcfg.StateMachineMediaBuyLifecycle,
		}
	}
	srv := startTestServer(t, mockcfg.MockService{
		Name:   "s",
		Routes: []mockcfg.Route{mkRoute("create_media_buy"), mkRoute("update_media_buy")},
	}, Options{RandSeed: 1})

	// Create -> draft.
	mustNoError(t, callTool(t, srv.URL(), "create_media_buy", map[string]any{"id": "mb1"}))
	// draft -> active (legal, across tools).
	mustNoError(t, callTool(t, srv.URL(), "update_media_buy", map[string]any{"id": "mb1", "status": "active"}))
	// active -> paused (legal).
	mustNoError(t, callTool(t, srv.URL(), "update_media_buy", map[string]any{"id": "mb1", "status": "paused"}))
	// paused -> draft (illegal): structured -32001 like a real seller.
	r := callTool(t, srv.URL(), "update_media_buy", map[string]any{"id": "mb1", "status": "draft"})
	if r.Err == nil {
		t.Fatalf("expected illegal-transition error")
	}
	if int(r.Err["code"].(float64)) != -32001 {
		t.Fatalf("code = %v, want -32001", r.Err["code"])
	}
	data := r.Err["data"].(map[string]any)
	if data["from"] != "paused" || data["to"] != "draft" {
		t.Fatalf("error data = %v", data)
	}
	if _, ok := data["allowed"]; !ok {
		t.Fatalf("error data missing allowed transitions: %v", data)
	}
	// Missing id: structured error, not a panic.
	r = callTool(t, srv.URL(), "update_media_buy", map[string]any{"status": "active"})
	if r.Err == nil || int(r.Err["code"].(float64)) != -32002 {
		t.Fatalf("missing id: err = %v, want -32002", r.Err)
	}
}

func TestCompose(t *testing.T) {
	cfg, err := mockcfg.Parse([]byte(`
mocks:
  - name: seller
    listen: :0
    routes:
      - match: { tool: get_products }
        respond: { inline: { who: seller } }
  - name: signals
    listen: :0
    routes:
      - match: { tool: get_signals }
        respond: { inline: { who: signals } }
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	var srvs []*Server
	for _, svc := range cfg.Services() {
		srv, err := New(svc, Options{RandSeed: 1})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := srv.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = srv.Close() })
		srvs = append(srvs, srv)
	}
	if len(srvs) != 2 || srvs[0].URL() == srvs[1].URL() {
		t.Fatalf("compose: %v", srvs)
	}
	res := mustNoError(t, callTool(t, srvs[0].URL(), "get_products", nil))
	if res.(map[string]any)["who"] != "seller" {
		t.Fatalf("seller: %v", res)
	}
	res = mustNoError(t, callTool(t, srvs[1].URL(), "get_signals", nil))
	if res.(map[string]any)["who"] != "signals" {
		t.Fatalf("signals: %v", res)
	}
	// Cross-talk fails: the signals mock knows no get_products route.
	r := callTool(t, srvs[1].URL(), "get_products", nil)
	if r.Err == nil {
		t.Fatalf("expected no-route error on signals mock")
	}
}

func TestNewRejectsBadConfig(t *testing.T) {
	_, err := New(mockcfg.MockService{Name: "s"}, Options{}) // no listen
	if err == nil {
		t.Fatalf("expected validation error for missing listen")
	}
	dir := t.TempDir()
	_, err = New(mockcfg.MockService{
		Name:   "s",
		Listen: ":0",
		Routes: []mockcfg.Route{{
			Match:   mockcfg.Match{Tool: "t"},
			Respond: mockcfg.Respond{File: "missing.json"},
		}},
	}, Options{BaseDir: dir})
	if err == nil || !strings.Contains(err.Error(), "missing.json") {
		t.Fatalf("expected missing-file error, got %v", err)
	}
}

func TestInitializeAndNotification(t *testing.T) {
	srv := startTestServer(t, mockcfg.MockService{Name: "s"}, Options{RandSeed: 1})
	res := mustNoError(t, call(t, srv.URL(), "initialize", nil))
	if res.(map[string]any)["protocolVersion"] == nil {
		t.Fatalf("initialize: %v", res)
	}
	// Notification (no id) -> 202, empty body.
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	resp, err := http.Post(srv.URL(), "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("notification status = %d, want 202", resp.StatusCode)
	}
}
