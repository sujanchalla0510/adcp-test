package load

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// fakeSeller is a controllable JSON-RPC endpoint: optional latency,
// optional RPC-error mode, optional hang mode (for timeout tests).
type fakeSeller struct {
	latency atomic.Int64 // nanoseconds of artificial delay
	errMode atomic.Bool  // answer tools/call with -32000
	hang    atomic.Bool  // sleep 5s regardless of latency
	calls   atomic.Int64
}

func (f *fakeSeller) handler(w http.ResponseWriter, r *http.Request) {
	f.calls.Add(1)
	var req struct {
		ID     any    `json:"id"`
		Method string `json:"method"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if d := time.Duration(f.latency.Load()); d > 0 {
		time.Sleep(d)
	}
	if f.hang.Load() {
		time.Sleep(5 * time.Second)
	}
	w.Header().Set("Content-Type", "application/json")
	resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
	if f.errMode.Load() && req.Method == "tools/call" {
		resp["error"] = map[string]any{"code": -32000, "message": "boom"}
	} else {
		resp["result"] = map[string]any{"ok": true}
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func startFake(t *testing.T, f *fakeSeller) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestPercentiles(t *testing.T) {
	samples := make([]float64, 100)
	for i := range samples {
		samples[i] = float64(i + 1)
	}
	got := Percentiles(samples, 50, 95, 99)
	want := []float64{50, 95, 99}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("p[%d] = %v, want %v", i, got[i], want[i])
		}
	}
	if z := Percentiles(nil, 50, 99); z[0] != 0 || z[1] != 0 {
		t.Errorf("empty percentiles = %v, want zeros", z)
	}
	if s := Percentiles([]float64{7}, 50, 99); s[0] != 7 || s[1] != 7 {
		t.Errorf("single-sample percentiles = %v, want [7 7]", s)
	}
}

func TestEngineToolMode(t *testing.T) {
	f := &fakeSeller{}
	f.latency.Store(int64(5 * time.Millisecond))
	url := startFake(t, f)

	cfg := &Config{
		TargetURL:   url,
		Tool:        "get_products",
		Concurrency: 4,
		Iterations:  20,
		Thresholds:  Thresholds{P99MsLt: 2000, ErrorRateLt: 0.01},
	}
	eng, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	res, err := eng.Run(context.Background(), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.TotalRequests != 20 || res.Successes != 20 {
		t.Errorf("total=%d successes=%d, want 20/20", res.TotalRequests, res.Successes)
	}
	if res.Errors != 0 || res.Timeouts != 0 {
		t.Errorf("errors=%d timeouts=%d, want 0/0", res.Errors, res.Timeouts)
	}
	if res.P50Ms < 5 || res.P99Ms > 2000 {
		t.Errorf("p50=%.1f p99=%.1f, want p50>=5ms and p99 sane", res.P50Ms, res.P99Ms)
	}
	if res.ThroughputRPS <= 0 {
		t.Errorf("throughput = %v, want > 0", res.ThroughputRPS)
	}
	if !res.Passed {
		t.Errorf("expected pass, thresholds = %+v", res.Thresholds)
	}
	if f.calls.Load() != 20 {
		t.Errorf("fake saw %d calls, want 20", f.calls.Load())
	}
}

func TestEngineThresholdFailure(t *testing.T) {
	f := &fakeSeller{}
	f.errMode.Store(true)
	url := startFake(t, f)

	cfg := &Config{
		TargetURL:   url,
		Tool:        "get_products",
		Concurrency: 2,
		Iterations:  6,
		Thresholds:  Thresholds{ErrorRateLt: 0.01},
	}
	eng, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	res, err := eng.Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Errors != 6 || res.ErrorRate != 1 {
		t.Errorf("errors=%d rate=%v, want 6 and 1.0", res.Errors, res.ErrorRate)
	}
	if res.Passed {
		t.Error("expected threshold failure, got pass")
	}
	var sawGate bool
	for _, tr := range res.Thresholds {
		if tr.Name == "error_rate_lt" && !tr.Passed {
			sawGate = true
		}
	}
	if !sawGate {
		t.Errorf("missing failed error_rate_lt gate: %+v", res.Thresholds)
	}
}

func TestEngineTimeouts(t *testing.T) {
	f := &fakeSeller{}
	f.hang.Store(true)
	url := startFake(t, f)

	cfg := &Config{
		TargetURL:      url,
		Tool:           "get_products",
		Concurrency:    2,
		Iterations:     4,
		RequestTimeout: 100 * time.Millisecond,
		Thresholds:     Thresholds{TimeoutRateLt: 0.5},
	}
	eng, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	res, err := eng.Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Timeouts != 4 {
		t.Errorf("timeouts = %d, want 4", res.Timeouts)
	}
	if res.TimeoutRate != 1 {
		t.Errorf("timeout_rate = %v, want 1.0", res.TimeoutRate)
	}
	if res.Passed {
		t.Error("expected timeout_rate gate failure, got pass")
	}
}

func TestEngineScenarioMode(t *testing.T) {
	f := &fakeSeller{}
	url := startFake(t, f)

	packYAML := "name: mini\ndescription: mini\nscenarios:\n  - name: one\n    steps:\n      - {tool: get_products, assertions: [{status: ok}]}\n"
	dir := t.TempDir()
	path := dir + "/mini.yaml"
	if err := writeFile(path, packYAML); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{
		TargetURL:   url,
		Scenario:    path,
		Concurrency: 2,
		Iterations:  6,
	}
	eng, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	res, err := eng.Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.TotalRequests != 6 || res.Successes != 6 {
		t.Errorf("total=%d successes=%d, want 6/6", res.TotalRequests, res.Successes)
	}
}

func TestProgressChannel(t *testing.T) {
	f := &fakeSeller{}
	url := startFake(t, f)

	cfg := &Config{
		TargetURL:   url,
		Tool:        "get_products",
		Concurrency: 2,
		Duration:    1200 * time.Millisecond,
	}
	eng, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ch := make(chan Progress, 64)
	res, err := eng.Run(context.Background(), ch)
	if err != nil {
		t.Fatal(err)
	}
	var snaps []Progress
	for p := range ch {
		snaps = append(snaps, p)
	}
	if len(snaps) == 0 {
		t.Fatal("no progress snapshots")
	}
	last := snaps[len(snaps)-1]
	if !last.Done {
		t.Error("final snapshot not marked done")
	}
	if last.Completed != res.TotalRequests {
		t.Errorf("final completed=%d, result total=%d", last.Completed, res.TotalRequests)
	}
}

func TestRemoteGuard(t *testing.T) {
	base := Config{Tool: "get_products", Concurrency: 1, Iterations: 1}
	remote := base
	remote.TargetURL = "https://seller.example.com/mcp"
	if _, err := New(&remote); err == nil {
		t.Error("remote target without allow_remote should be refused")
	}
	okCfg := base
	okCfg.TargetURL = "https://seller.example.com/mcp"
	okCfg.AllowRemote = true
	if _, err := New(&okCfg); err != nil {
		t.Errorf("allow_remote should bypass guard: %v", err)
	}
	for _, local := range []string{
		"http://localhost:8080",
		"http://127.0.0.1:8080",
		"http://127.0.0.5:8080",
		"http://[::1]:8080",
	} {
		lc := base
		lc.TargetURL = local
		if _, err := New(&lc); err != nil {
			t.Errorf("localhost target %q refused: %v", local, err)
		}
	}
}

func TestConfigValidation(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{"no target", Config{Tool: "t", Concurrency: 1, Iterations: 1}},
		{"no workload", Config{TargetURL: "http://127.0.0.1:1", Concurrency: 1, Iterations: 1}},
		{"both workloads", Config{TargetURL: "http://127.0.0.1:1", Tool: "t", Scenario: "s", Concurrency: 1, Iterations: 1}},
		{"no concurrency", Config{TargetURL: "http://127.0.0.1:1", Tool: "t", Iterations: 1}},
		{"no stop condition", Config{TargetURL: "http://127.0.0.1:1", Tool: "t", Concurrency: 1}},
		{"bad scenario name", Config{TargetURL: "http://127.0.0.1:1", Scenario: "no-such-pack", Concurrency: 1, Iterations: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng, err := New(&tc.cfg)
			if err == nil && tc.name != "bad scenario name" {
				t.Errorf("New should fail for %+v", tc.cfg)
				return
			}
			if err != nil {
				return
			}
			// bad scenario name passes validation but must fail at run time.
			if _, err := eng.Run(context.Background(), nil); err == nil {
				t.Error("Run should fail for unknown scenario pack")
			}
		})
	}
}

func TestParseConfig(t *testing.T) {
	yamlDoc := `
target_url: http://127.0.0.1:8080
tool: get_products
arguments: {currency: USD}
concurrency: 5
ramp_up: 10s
duration: 1m
thresholds:
  p99_ms_lt: 2000
  error_rate_lt: 0.01
`
	cfg, err := ParseConfig([]byte(yamlDoc))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.Concurrency != 5 || cfg.RampUp != 10*time.Second || cfg.Duration != time.Minute {
		t.Errorf("parsed config = %+v", cfg)
	}
	if cfg.Thresholds.P99MsLt != 2000 || cfg.Thresholds.ErrorRateLt != 0.01 {
		t.Errorf("thresholds = %+v", cfg.Thresholds)
	}
}

func writeFile(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o644)
}
