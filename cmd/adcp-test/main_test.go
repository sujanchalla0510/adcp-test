package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// minimalFakeSeller answers just enough JSON-RPC for CI-mode wiring tests.
func minimalFakeSeller(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		if req.Method == "tools/list" {
			resp["result"] = map[string]any{"tools": []any{}}
		} else {
			resp["error"] = map[string]any{"code": -32601, "message": "Method not found"}
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRunConformanceCIEmptyTarget(t *testing.T) {
	var buf bytes.Buffer
	if code := runConformanceCI(&buf, "", "", ""); code != 1 {
		t.Fatalf("exit code = %d, want 1 for empty target", code)
	}
	var out map[string]any
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, buf.String())
	}
	if out["status"] != "error" {
		t.Fatalf("status = %v, want error", out["status"])
	}
}

func TestRunConformanceCIFailingSeller(t *testing.T) {
	srv := minimalFakeSeller(t) // advertises no tools: surface check fails
	var buf bytes.Buffer
	if code := runConformanceCI(&buf, srv.URL, "", ""); code != 1 {
		t.Fatalf("exit code = %d, want 1 for failing seller", code)
	}
	var rep struct {
		TargetURL string `json:"target_url"`
		Summary   struct {
			Total  int `json:"total"`
			Failed int `json:"failed"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(buf.Bytes(), &rep); err != nil {
		t.Fatalf("stdout is not a report: %v\n%.200s", err, buf.String())
	}
	if rep.TargetURL != srv.URL {
		t.Fatalf("target_url = %q, want %q", rep.TargetURL, srv.URL)
	}
	if rep.Summary.Total == 0 || rep.Summary.Failed == 0 {
		t.Fatalf("expected failing checks, got %+v", rep.Summary)
	}
}

func TestRunConformanceCIUnreachable(t *testing.T) {
	// Point at a port that is (almost certainly) closed; the run must still
	// emit a JSON report and exit 1, not crash.
	var buf bytes.Buffer
	if code := runConformanceCI(&buf, "http://127.0.0.1:1/nope", "", ""); code != 1 {
		t.Fatalf("exit code = %d, want 1 for unreachable target", code)
	}
	if !strings.Contains(buf.String(), `"target_url"`) {
		t.Fatalf("expected a report on stdout, got: %.200s", buf.String())
	}
}

func TestRunRecordMissingFlags(t *testing.T) {
	// Missing --upstream / --out must fail fast (exit 2), never start a proxy.
	if code := runRecord([]string{}); code != 2 {
		t.Fatalf("exit code = %d, want 2 for missing flags", code)
	}
	if code := runRecord([]string{"--upstream", "http://127.0.0.1:9/x"}); code != 2 {
		t.Fatalf("exit code = %d, want 2 for missing --out", code)
	}
	if code := runRecord([]string{"--upstream", "ftp://bad/scheme", "--out", "x.json"}); code == 0 {
		t.Fatalf("exit code = %d, want non-zero for bad upstream", code)
	}
}

func TestRunReplayMissingFlags(t *testing.T) {
	if code := runReplay([]string{}); code != 2 {
		t.Fatalf("exit code = %d, want 2 for missing --cassette", code)
	}
	if code := runReplay([]string{"--cassette", "/nonexistent/cassette.json"}); code != 2 && code != 1 {
		t.Fatalf("exit code = %d, want non-zero for missing cassette file", code)
	}
}

func TestRunMockMissingFlags(t *testing.T) {
	// Missing --config must fail fast (exit 2), never start a server.
	if code := runMock([]string{}); code != 2 {
		t.Fatalf("exit code = %d, want 2 for missing --config", code)
	}
	if code := runMock([]string{"--config", "/nonexistent/mock.yaml"}); code == 0 {
		t.Fatalf("exit code = %d, want non-zero for missing config file", code)
	}
}

// scenarioFakeSeller answers the happy-path pack's tools with canned
// synthetic results (same shape as the server package's test fake).
func scenarioCLIFake(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		switch req.Params.Name {
		case "get_products":
			resp["result"] = map[string]any{"products": []any{map[string]any{"product_id": "prod-web-banner"}}}
		case "create_media_buy":
			resp["result"] = map[string]any{"media_buy_id": "mb-1", "status": "draft"}
		case "sync_creatives":
			resp["result"] = map[string]any{"status": "approved"}
		case "get_media_buy_delivery":
			resp["result"] = map[string]any{"media_buy_id": req.Params.Arguments["media_buy_id"]}
		case "update_media_buy":
			resp["result"] = map[string]any{"status": req.Params.Arguments["status"]}
		default:
			resp["error"] = map[string]any{"code": -32601, "message": "no such tool"}
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRunScenarioList(t *testing.T) {
	var buf bytes.Buffer
	if code := runScenario([]string{"--list"}, &buf); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(buf.String(), "happy-path-media-buy") {
		t.Errorf("--list output missing packs: %q", buf.String())
	}
}

func TestRunScenarioMissingFlags(t *testing.T) {
	var buf bytes.Buffer
	if code := runScenario([]string{}, &buf); code != 2 {
		t.Fatalf("exit code = %d, want 2 for missing flags", code)
	}
	if code := runScenario([]string{"--pack", "no-such-pack", "--target", "http://127.0.0.1:1"}, &buf); code == 0 {
		t.Fatalf("exit code = %d, want non-zero for unknown pack", code)
	}
}

func TestRunScenarioPassAndFail(t *testing.T) {
	fake := scenarioCLIFake(t)

	// Happy path against the fake passes.
	var buf bytes.Buffer
	code := runScenario([]string{"--pack", "happy-path-media-buy", "--target", fake.URL}, &buf)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0: %s", code, buf.String())
	}
	var rep struct {
		Summary struct {
			Passed int `json:"passed"`
			Failed int `json:"failed"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(buf.Bytes(), &rep); err != nil {
		t.Fatalf("report is not JSON: %v", err)
	}
	if rep.Summary.Passed != 1 || rep.Summary.Failed != 0 {
		t.Errorf("summary = %+v, want 1 passed 0 failed", rep.Summary)
	}

	// Creative-rejection pack fails against the fake (no rejection route).
	buf.Reset()
	if code := runScenario([]string{"--pack", "creative-rejection", "--target", fake.URL}, &buf); code != 1 {
		t.Fatalf("exit code = %d, want 1 for failing pack", code)
	}
}

func TestRunLoadMissingFlags(t *testing.T) {
	var buf bytes.Buffer
	if code := runLoad([]string{}, &buf); code != 2 {
		t.Fatalf("exit code = %d, want 2 for missing --config", code)
	}
	if code := runLoad([]string{"--config", "/nonexistent/load.yaml"}, &buf); code == 0 {
		t.Fatalf("exit code = %d, want non-zero for missing config", code)
	}
}

func TestRunLoadHeadless(t *testing.T) {
	fake := scenarioCLIFake(t)
	dir := t.TempDir()
	cfgPath := dir + "/load.yaml"
	cfgYAML := "target_url: " + fake.URL + "\ntool: get_products\nconcurrency: 2\niterations: 10\nthresholds:\n  p99_ms_lt: 5000\n  error_rate_lt: 0.01\n"
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if code := runLoad([]string{"--config", cfgPath}, &buf); code != 0 {
		t.Fatalf("exit code = %d, want 0: %s", code, buf.String())
	}
	var res struct {
		TotalRequests int64   `json:"total_requests"`
		P50Ms         float64 `json:"p50_ms"`
		Passed        bool    `json:"passed"`
	}
	if err := json.Unmarshal(buf.Bytes(), &res); err != nil {
		t.Fatalf("report is not JSON: %v\n%s", err, buf.String())
	}
	if res.TotalRequests != 10 || !res.Passed {
		t.Errorf("result = %+v, want 10 requests passing", res)
	}
}

func TestRunLoadRemoteGuard(t *testing.T) {
	dir := t.TempDir()
	cfgPath := dir + "/load.yaml"
	cfgYAML := "target_url: https://seller.example.com/mcp\ntool: get_products\nconcurrency: 1\niterations: 1\n"
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if code := runLoad([]string{"--config", cfgPath}, &buf); code == 0 {
		t.Error("remote target without --allow-remote should fail")
	}
}
