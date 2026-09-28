package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sujanchalla0510/adcp-test/internal/config"
)

func newMockTestServer(t *testing.T) *Server {
	t.Helper()
	return New(&config.Config{Port: 0})
}

func doJSON(t *testing.T, h http.Handler, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestMockConfigGetPutRoundTrip(t *testing.T) {
	s := newMockTestServer(t)
	h := s.Handler()

	code, out := doJSON(t, h, "GET", "/api/mock/config", nil)
	if code != 200 {
		t.Fatalf("GET status = %d", code)
	}
	yamlText, ok := out["yaml"].(string)
	if !ok || !strings.Contains(yamlText, "get_products") {
		t.Fatalf("default config missing: %v", out["yaml"])
	}
	if out["model"] == nil {
		t.Fatalf("model missing")
	}

	// PUT new YAML; GET returns it verbatim (comments preserved).
	custom := "# hello\nmocks:\n  - name: x\n    listen: :0\n    routes:\n      - match: { tool: t }\n        respond: { inline: { ok: true } }\n"
	code, out = doJSON(t, h, "PUT", "/api/mock/config", map[string]any{"yaml": custom})
	if code != 200 {
		t.Fatalf("PUT status = %d: %v", code, out)
	}
	code, out = doJSON(t, h, "GET", "/api/mock/config", nil)
	if out["yaml"] != custom {
		t.Fatalf("round trip mismatch:\n%s", out["yaml"])
	}

	// PUT invalid YAML is rejected and the stored config is untouched.
	code, _ = doJSON(t, h, "PUT", "/api/mock/config", map[string]any{"yaml": "mocks:\n  - name: x\n"})
	if code != 400 {
		t.Fatalf("PUT invalid: status = %d, want 400", code)
	}
	_, out = doJSON(t, h, "GET", "/api/mock/config", nil)
	if out["yaml"] != custom {
		t.Fatalf("stored config changed after rejected PUT")
	}
}

func TestMockValidate(t *testing.T) {
	s := newMockTestServer(t)
	h := s.Handler()

	code, out := doJSON(t, h, "POST", "/api/mock/validate", map[string]any{"yaml": defaultMockConfig})
	if code != 200 || out["valid"] != true {
		t.Fatalf("valid config: %d %v", code, out)
	}

	code, out = doJSON(t, h, "POST", "/api/mock/validate", map[string]any{
		"yaml": "mocks:\n  - name: s\n    listen: :0\n    routes:\n      - match: { tool: t }\n        respond: { inline: {} }\n        faults: { error_rate: 9 }\n",
	})
	if code != 200 || out["valid"] != false {
		t.Fatalf("invalid config: %d %v", code, out)
	}
	if errs, ok := out["errors"].([]any); !ok || len(errs) == 0 {
		t.Fatalf("errors missing: %v", out)
	}

	// Model JSON validates and returns canonical YAML.
	model := map[string]any{"mocks": []any{map[string]any{
		"name": "m", "listen": ":0",
		"routes": []any{map[string]any{
			"match":   map[string]any{"tool": "t"},
			"respond": map[string]any{"inline": map[string]any{"ok": true}},
		}},
	}}}
	code, out = doJSON(t, h, "POST", "/api/mock/validate", map[string]any{"model": model})
	if code != 200 || out["valid"] != true {
		t.Fatalf("model: %d %v", code, out)
	}
	if _, ok := out["yaml"].(string); !ok {
		t.Fatalf("canonical yaml missing: %v", out)
	}
}

func TestMockStartStop(t *testing.T) {
	s := newMockTestServer(t)
	h := s.Handler()

	code, out := doJSON(t, h, "POST", "/api/mock/start", map[string]any{})
	if code != 200 {
		t.Fatalf("start: %d %v", code, out)
	}
	started, ok := out["started"].([]any)
	if !ok || len(started) != 1 {
		t.Fatalf("started = %v", out)
	}
	url := started[0].(map[string]any)["url"].(string)

	// The mock actually serves: hit its tools/list over HTTP.
	resp, err := http.Post(url, "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var env struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	if len(env.Result.Tools) != 2 || env.Result.Tools[0].Name != "get_products" {
		t.Fatalf("tools = %+v", env.Result.Tools)
	}

	code, out = doJSON(t, h, "GET", "/api/mock/config", nil)
	if running, ok := out["running"].([]any); !ok || len(running) != 1 {
		t.Fatalf("running = %v", out["running"])
	}

	code, out = doJSON(t, h, "POST", "/api/mock/stop", map[string]any{})
	if code != 200 || out["stopped"] != true {
		t.Fatalf("stop: %d %v", code, out)
	}

	// Restart works after stop (idempotent start).
	code, _ = doJSON(t, h, "POST", "/api/mock/start", map[string]any{})
	if code != 200 {
		t.Fatalf("restart: %d", code)
	}
	_, _ = doJSON(t, h, "POST", "/api/mock/stop", map[string]any{})
}

func TestMockStartInvalidConfig(t *testing.T) {
	s := newMockTestServer(t)
	h := s.Handler()
	// Bypass PUT validation by writing a bad config directly.
	s.mu.Lock()
	s.mockCfg = "mocks: []\n"
	s.mu.Unlock()
	code, out := doJSON(t, h, "POST", "/api/mock/start", map[string]any{})
	if code != 400 {
		t.Fatalf("start with invalid config: %d %v", code, out)
	}
}

func TestMockRecordFlow(t *testing.T) {
	// Fake upstream seller.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"products":[{"id":"p1"}]}}`))
	}))
	defer upstream.Close()

	s := newMockTestServer(t)
	h := s.Handler()

	code, out := doJSON(t, h, "POST", "/api/mock/record", map[string]any{"upstream_url": upstream.URL})
	if code != 200 {
		t.Fatalf("record start: %d %v", code, out)
	}
	recordID, _ := out["record_id"].(string)
	proxyURL, _ := out["proxy_url"].(string)
	if recordID == "" || proxyURL == "" {
		t.Fatalf("record start: %v", out)
	}

	// Drive traffic through the proxy.
	rpcBody := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_products","arguments":{"q":"x"}}}`
	resp, err := http.Post(proxyURL, "application/json", strings.NewReader(rpcBody))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("proxy status = %d", resp.StatusCode)
	}

	code, out = doJSON(t, h, "POST", "/api/mock/record/finish", map[string]any{"record_id": recordID})
	if code != 200 {
		t.Fatalf("record finish: %d %v", code, out)
	}
	yamlText, _ := out["yaml"].(string)
	if !strings.Contains(yamlText, "get_products") || !strings.Contains(yamlText, "p1") {
		t.Fatalf("generated yaml:\n%s", yamlText)
	}
	if out["exchanges"] != float64(1) {
		t.Fatalf("exchanges = %v", out["exchanges"])
	}
	// The generated config validates and can be stored.
	code, _ = doJSON(t, h, "PUT", "/api/mock/config", map[string]any{"yaml": yamlText})
	if code != 200 {
		t.Fatalf("PUT generated: %d", code)
	}

	// Finishing twice fails: the recording is gone.
	code, _ = doJSON(t, h, "POST", "/api/mock/record/finish", map[string]any{"record_id": recordID})
	if code != 404 {
		t.Fatalf("double finish: %d, want 404", code)
	}
}

func TestMockRecordNoTraffic(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer upstream.Close()

	s := newMockTestServer(t)
	h := s.Handler()
	_, out := doJSON(t, h, "POST", "/api/mock/record", map[string]any{"upstream_url": upstream.URL})
	code, _ := doJSON(t, h, "POST", "/api/mock/record/finish", map[string]any{"record_id": out["record_id"]})
	if code != 400 {
		t.Fatalf("finish with no traffic: %d, want 400", code)
	}
}

func TestMockRecordBadUpstream(t *testing.T) {
	s := newMockTestServer(t)
	h := s.Handler()
	code, _ := doJSON(t, h, "POST", "/api/mock/record", map[string]any{"upstream_url": "not-a-url"})
	if code != 400 {
		t.Fatalf("bad upstream: %d, want 400", code)
	}
	code, _ = doJSON(t, h, "POST", "/api/mock/record", map[string]any{})
	if code != 400 {
		t.Fatalf("missing upstream: %d, want 400", code)
	}
}
