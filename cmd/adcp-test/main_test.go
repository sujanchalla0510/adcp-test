package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	if code := runConformanceCI(&buf, ""); code != 1 {
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
	if code := runConformanceCI(&buf, srv.URL); code != 1 {
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
	if code := runConformanceCI(&buf, "http://127.0.0.1:1/nope"); code != 1 {
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
