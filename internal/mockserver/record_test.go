package mockserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sujanchalla0510/adcp-test/internal/mockcfg"
)

func svcFromRecord(t *testing.T, upstream string) mockcfg.MockService {
	t.Helper()
	dir := t.TempDir()
	return mockcfg.MockService{
		Name:   "rec",
		Listen: ":0",
		Record: &mockcfg.Record{Upstream: upstream, CaptureTo: filepath.Join(dir, "out.yaml")},
	}
}

func parseConfigBytes(data []byte) (*mockcfg.Config, error) {
	c, err := mockcfg.Parse(data)
	if err != nil {
		return nil, err
	}
	return c, c.Validate()
}

func readFileBytes(path string) ([]byte, error) { return os.ReadFile(path) }

// fakeUpstream is a canned MCP seller for record-mode tests.
func fakeUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		time.Sleep(15 * time.Millisecond) // measurable latency
		var result any
		switch req.Params.Name {
		case "get_products":
			result = map[string]any{"products": []any{map[string]any{"id": "p1"}}}
		case "create_media_buy":
			// Vary the payload per call so the generator builds a sequence.
			result = map[string]any{"status": "created"}
		default:
			result = map[string]any{"ok": true}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": json.RawMessage(req.ID), "result": result,
		})
	}))
}

func postRPC(t *testing.T, url, tool string, args any) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args},
	})
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

func TestRecordProxyAndGenerate(t *testing.T) {
	up := fakeUpstream(t)
	defer up.Close()

	rec, err := NewRecorder(up.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(rec)
	defer proxy.Close()

	postRPC(t, proxy.URL, "get_products", map[string]any{"q": "x"})
	postRPC(t, proxy.URL, "create_media_buy", map[string]any{"buyer_ref": "acme"})
	postRPC(t, proxy.URL, "get_products", map[string]any{"q": "y"})

	ex := rec.Exchanges()
	if len(ex) != 3 {
		t.Fatalf("exchanges = %d, want 3", len(ex))
	}
	if ex[0].Tool != "get_products" || ex[1].Tool != "create_media_buy" {
		t.Fatalf("tools = %q %q", ex[0].Tool, ex[1].Tool)
	}
	if ex[0].Latency < 10*time.Millisecond {
		t.Fatalf("latency = %v, want >= 10ms", ex[0].Latency)
	}

	cfg := GenerateConfig(ex, "recorded", ":8080")
	if err := cfg.Validate(); err != nil {
		t.Fatalf("generated config invalid: %v", err)
	}
	svcs := cfg.Services()
	if len(svcs) != 1 || svcs[0].Name != "recorded" {
		t.Fatalf("services = %+v", svcs)
	}
	if len(svcs[0].Routes) != 2 {
		t.Fatalf("routes = %d, want 2 (one per tool)", len(svcs[0].Routes))
	}
	// get_products saw the same payload twice -> inline.
	r0 := svcs[0].Routes[0]
	if r0.Match.Tool != "get_products" || r0.Respond.Inline == nil {
		t.Fatalf("route0 = %+v", r0)
	}
	// Latency estimated from observed timings.
	if r0.Latency == nil || r0.Latency.P50 == "" {
		t.Fatalf("route0 latency = %+v", r0.Latency)
	}
}

func TestGenerateConfigSequence(t *testing.T) {
	ex := []RecordedExchange{
		{Tool: "t", Result: map[string]any{"v": 1}, Latency: 5 * time.Millisecond},
		{Tool: "t", Result: map[string]any{"v": 2}, Latency: 7 * time.Millisecond},
		{Tool: "t", Result: map[string]any{"v": 1}, Latency: 6 * time.Millisecond}, // dup
	}
	cfg := GenerateConfig(ex, "s", ":8080")
	r := cfg.Services()[0].Routes[0]
	if len(r.Respond.Sequence) != 2 {
		t.Fatalf("sequence = %+v, want 2 distinct payloads", r.Respond.Sequence)
	}
	if r.Latency.Fixed != "" || r.Latency.P50 == "" {
		t.Fatalf("latency = %+v, want p50/p99", r.Latency)
	}
}

func TestGenerateConfigRoundTrip(t *testing.T) {
	ex := []RecordedExchange{
		{Tool: "get_products", Result: map[string]any{"products": []any{}}, Latency: 20 * time.Millisecond},
	}
	cfg := GenerateConfig(ex, "s", ":8080")
	data, err := cfg.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseConfigBytes(data); err != nil {
		t.Fatalf("generated YAML does not re-parse: %v\n%s", err, data)
	}
}

func TestRecordServerEndToEnd(t *testing.T) {
	up := fakeUpstream(t)
	defer up.Close()

	svc := svcFromRecord(t, up.URL)
	srv, err := New(svc, Options{RandSeed: 1})
	if err != nil {
		t.Fatal(err)
	}
	url, err := srv.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	if !srv.IsRecordMode() {
		t.Fatalf("IsRecordMode = false")
	}

	postRPC(t, url, "get_products", nil)

	path, err := srv.WriteCapturedConfig()
	if err != nil {
		t.Fatal(err)
	}
	data, err := readFileBytes(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := parseConfigBytes(data)
	if err != nil {
		t.Fatalf("captured config invalid: %v\n%s", err, data)
	}
	routes := cfg.Services()[0].Routes
	if len(routes) != 1 || routes[0].Match.Tool != "get_products" {
		t.Fatalf("captured routes = %+v", routes)
	}
}
