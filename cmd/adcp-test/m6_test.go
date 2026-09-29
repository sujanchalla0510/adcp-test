package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRunSigndebugMissingFlags(t *testing.T) {
	var buf bytes.Buffer
	if code := runSigndebug([]string{}, &buf); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if code := runSigndebug([]string{"--request", "x.json"}, &buf); code != 2 {
		t.Fatalf("exit code = %d, want 2 without --key", code)
	}
}

func TestRunSigndebugInvalidKey(t *testing.T) {
	dir := t.TempDir()
	reqPath := filepath.Join(dir, "req.json")
	_ = os.WriteFile(reqPath, []byte(`{"method":"POST","url":"http://x/","headers":{},"body":"{}"}`), 0o644)
	keyPath := filepath.Join(dir, "key.pem")
	_ = os.WriteFile(keyPath, []byte("not a pem key"), 0o644)
	var buf bytes.Buffer
	if code := runSigndebug([]string{"--request", reqPath, "--key", keyPath}, &buf); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	var rep map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rep); err != nil {
		t.Fatalf("report is not JSON: %v", err)
	}
	if rep["verdict"] != "invalid" {
		t.Errorf("verdict = %v, want invalid", rep["verdict"])
	}
}

// lifecycleCLIFake is a stateful seller implementing the media-buy
// lifecycle state machine.
func lifecycleCLIFake(t *testing.T) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	state := map[string]string{}
	allowed := map[string][]string{
		"draft":     {"active", "cancelled"},
		"active":    {"paused", "cancelled"},
		"paused":    {"active", "cancelled"},
		"cancelled": {},
	}
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
		mu.Lock()
		defer mu.Unlock()
		switch req.Params.Name {
		case "create_media_buy":
			state["mb-1"] = "draft"
			resp["result"] = map[string]any{"media_buy_id": "mb-1", "status": "draft"}
		case "update_media_buy":
			id, _ := req.Params.Arguments["media_buy_id"].(string)
			want, _ := req.Params.Arguments["status"].(string)
			cur := state[id]
			ok := false
			for _, s := range allowed[cur] {
				if s == want {
					ok = true
				}
			}
			if !ok {
				resp["error"] = map[string]any{"code": -32001, "message": "illegal transition"}
				break
			}
			state[id] = want
			resp["result"] = map[string]any{"media_buy_id": id, "status": want}
		default:
			resp["error"] = map[string]any{"code": -32601, "message": "no such tool"}
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRunLifecycleMissingTarget(t *testing.T) {
	var buf bytes.Buffer
	if code := runLifecycle([]string{}, &buf); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
}

func TestRunLifecycleAgainstFake(t *testing.T) {
	fake := lifecycleCLIFake(t)
	var buf bytes.Buffer
	if code := runLifecycle([]string{"--target", fake.URL}, &buf); code != 0 {
		t.Fatalf("exit code = %d, want 0: %s", code, buf.String())
	}
	var rep struct {
		Checks []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
			Detail string `json:"detail"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(buf.Bytes(), &rep); err != nil {
		t.Fatalf("report is not JSON: %v", err)
	}
	if len(rep.Checks) != 6 {
		t.Fatalf("expected 6 checks, got %d", len(rep.Checks))
	}
	for _, c := range rep.Checks {
		if c.Status != "pass" {
			t.Errorf("check %q: %s (%s)", c.Name, c.Status, c.Detail)
		}
	}
	if !strings.Contains(rep.Checks[5].Detail, "-32001") {
		t.Errorf("illegal transition should cite -32001: %q", rep.Checks[5].Detail)
	}
}

func TestRunFuzzFlags(t *testing.T) {
	var buf bytes.Buffer
	if code := runFuzz([]string{}, &buf); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	// Remote target refused without --allow-remote.
	if code := runFuzz([]string{"--target", "https://example.com:1/", "--iterations", "1"}, &buf); code != 1 {
		t.Fatalf("exit code = %d, want 1 for refused remote", code)
	}
}

func TestRunFuzzCleanAndFindings(t *testing.T) {
	// Well-behaved fake: every request gets a JSON-RPC error.
	clean := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"bad"}}`))
	}))
	defer clean.Close()
	var buf bytes.Buffer
	if code := runFuzz([]string{"--target", clean.URL, "--iterations", "20", "--seed", "5"}, &buf); code != 0 {
		t.Fatalf("exit code = %d, want 0: %s", code, buf.String())
	}
	var rep struct {
		Seed     int64 `json:"seed"`
		Findings []any `json:"findings"`
	}
	if err := json.Unmarshal(buf.Bytes(), &rep); err != nil {
		t.Fatalf("report is not JSON: %v", err)
	}
	if rep.Seed != 5 {
		t.Errorf("seed = %d, want 5", rep.Seed)
	}

	// Crashing fake: 500 + HTML.
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("<html>boom</html>"))
	}))
	defer broken.Close()
	buf.Reset()
	if code := runFuzz([]string{"--target", broken.URL, "--iterations", "3", "--seed", "5"}, &buf); code != 1 {
		t.Fatalf("exit code = %d, want 1 for findings", code)
	}
}

func TestRunSnapshotCycle(t *testing.T) {
	// runSnapshot uses ./snapshots relative to the working directory:
	// isolate it.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(cwd) }()

	reportPath := filepath.Join(dir, "report.json")
	_ = os.WriteFile(reportPath, []byte(`{"checks":[{"name":"a","status":"pass"}]}`), 0o644)

	var buf bytes.Buffer
	if code := runSnapshot([]string{"save", "--kind", "conformance", "--name", "base", "--file", reportPath}, &buf); code != 0 {
		t.Fatalf("save: exit %d: %s", code, buf.String())
	}
	buf.Reset()
	if code := runSnapshot([]string{"list"}, &buf); code != 0 {
		t.Fatalf("list: exit %d", code)
	}
	if !strings.Contains(buf.String(), "base") {
		t.Errorf("list missing snapshot: %s", buf.String())
	}
	buf.Reset()
	if code := runSnapshot([]string{"diff", "--before", "base", "--after", "base"}, &buf); code != 0 {
		t.Fatalf("diff: exit %d: %s", code, buf.String())
	}
	var d struct {
		Changed int `json:"changed"`
	}
	if err := json.Unmarshal(buf.Bytes(), &d); err != nil {
		t.Fatalf("diff is not JSON: %v", err)
	}
	if d.Changed != 0 {
		t.Errorf("changed = %d, want 0", d.Changed)
	}
	buf.Reset()
	if code := runSnapshot([]string{"delete", "--name", "base"}, &buf); code != 0 {
		t.Fatalf("delete: exit %d: %s", code, buf.String())
	}
	buf.Reset()
	if code := runSnapshot([]string{"list"}, &buf); code != 0 {
		t.Fatalf("list: exit %d", code)
	}
	if strings.Contains(buf.String(), "base") {
		t.Errorf("snapshot still listed after delete: %s", buf.String())
	}
}

func TestRunReportBuilds(t *testing.T) {
	dir := t.TempDir()
	confPath := filepath.Join(dir, "c.json")
	_ = os.WriteFile(confPath, []byte(`{"checks":[{"name":"a","status":"pass","detail":"ok"}]}`), 0o644)
	htmlPath := filepath.Join(dir, "evidence.html")
	pdfPath := filepath.Join(dir, "evidence.pdf")
	var buf bytes.Buffer
	code := runReport([]string{
		"--title", "cli test", "--conformance", confPath,
		"--out", htmlPath, "--pdf", pdfPath,
	}, &buf)
	if code != 0 {
		t.Fatalf("exit code = %d: %s", code, buf.String())
	}
	htmlBytes, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(htmlBytes), "cli test") {
		t.Error("title missing from HTML")
	}
	pdfBytes, err := os.ReadFile(pdfPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(pdfBytes, []byte("%PDF")) {
		t.Error("missing PDF magic")
	}
	// No inputs at all -> failure.
	buf.Reset()
	if code := runReport([]string{"--out", filepath.Join(dir, "x.html")}, &buf); code != 1 {
		t.Fatalf("exit code = %d, want 1 with no inputs", code)
	}
}

func TestRunScenarioChaosFlag(t *testing.T) {
	fake := scenarioCLIFake(t)
	var buf bytes.Buffer
	code := runScenario([]string{
		"--pack", "cancel-pause", "--target", fake.URL, "--chaos", "--chaos-seed", "1",
	}, &buf)
	if code != 0 && code != 1 {
		t.Fatalf("exit code = %d, want 0 or 1", code)
	}
	var rep struct {
		Chaos *struct {
			Seed int64 `json:"seed"`
		} `json:"chaos"`
	}
	if err := json.Unmarshal(buf.Bytes(), &rep); err != nil {
		t.Fatalf("report is not JSON: %v", err)
	}
	if rep.Chaos == nil {
		t.Fatal("expected a chaos section in the report")
	}
	if rep.Chaos.Seed != 1 {
		t.Errorf("chaos seed = %d, want 1", rep.Chaos.Seed)
	}
}

// TestRunScenarioChaosGuardRefusesRemote: the CLI refuses a chaos run
// against a non-localhost target unless --allow-remote is passed.
func TestRunScenarioChaosGuardRefusesRemote(t *testing.T) {
	var buf bytes.Buffer
	code := runScenario([]string{
		"--pack", "cancel-pause", "--target", "https://seller.example/mcp", "--chaos",
	}, &buf)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (guard refusal)", code)
	}
	if !strings.Contains(buf.String(), "non-localhost") {
		t.Errorf("expected a localhost-guard error, got: %s", buf.String())
	}
}
