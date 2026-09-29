package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sujanchalla0510/adcp-test/internal/config"
	"github.com/sujanchalla0510/adcp-test/internal/snapshots"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	s := New(&config.Config{Port: config.DefaultPort})
	// Isolate snapshots per test.
	s.snapshotStore = snapshots.New(t.TempDir())
	return s
}

func postJSON(t *testing.T, s *Server, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(http.MethodPost, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestSigndebugAPIRejectsGarbage(t *testing.T) {
	s := testServer(t)
	rec := postJSON(t, s, "/api/signdebug/verify", map[string]any{
		"request": map[string]any{"method": "POST", "url": "http://x/", "headers": map[string]string{}},
		"key_pem": "not-a-key",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var rep map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if rep["verdict"] != "invalid" {
		t.Errorf("expected an invalid verdict, got %v", rep["verdict"])
	}
}

func TestLifecycleAPIRequiresTarget(t *testing.T) {
	s := testServer(t)
	rec := postJSON(t, s, "/api/lifecycle/run", map[string]any{"target_url": ""})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for an empty target, got %d", rec.Code)
	}
}

func TestFuzzAPIRefusesRemote(t *testing.T) {
	s := testServer(t)
	rec := postJSON(t, s, "/api/fuzz/run", map[string]any{
		"target_url": "https://example.com:1/", "iterations": 5,
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for a remote target, got %d", rec.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if !strings.Contains(strings.ToLower(body["error"].(string)), "non-localhost") {
		t.Errorf("expected a localhost-guard error, got %v", body)
	}
}

func TestWebhookAPILifecycle(t *testing.T) {
	s := testServer(t)
	// Listen.
	rec := postJSON(t, s, "/api/webhooks/listen", map[string]any{"port": 0})
	if rec.Code != http.StatusOK {
		t.Fatalf("listen: %d %s", rec.Code, rec.Body.String())
	}
	var listenRes map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &listenRes); err != nil {
		t.Fatal(err)
	}
	url, _ := listenRes["url"].(string)
	if !strings.HasPrefix(url, "http://127.0.0.1:") {
		t.Fatalf("listener url %q", url)
	}
	// Deliver something.
	resp, err := http.Post(url, "application/json", strings.NewReader(`{"event":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	// Read back.
	req := httptest.NewRequest(http.MethodGet, "/api/webhooks/deliveries", nil)
	rec2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec2, req)
	var got map[string]any
	if err := json.Unmarshal(rec2.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	deliveries, _ := got["deliveries"].([]any)
	if len(deliveries) != 1 {
		t.Fatalf("expected 1 delivery, got %v", got)
	}
	// Clear.
	rec3 := postJSON(t, s, "/api/webhooks/clear", map[string]any{})
	if rec3.Code != http.StatusOK {
		t.Fatalf("clear: %d", rec3.Code)
	}
	req4 := httptest.NewRequest(http.MethodGet, "/api/webhooks/deliveries", nil)
	rec4 := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec4, req4)
	var got4 map[string]any
	_ = json.Unmarshal(rec4.Body.Bytes(), &got4)
	if deliveries, _ := got4["deliveries"].([]any); len(deliveries) != 0 {
		t.Errorf("expected 0 deliveries after clear, got %d", len(deliveries))
	}
	// Stop.
	rec5 := postJSON(t, s, "/api/webhooks/stop", map[string]any{})
	if rec5.Code != http.StatusOK {
		t.Fatalf("stop: %d", rec5.Code)
	}
}

func TestSnapshotAPIRoundTrip(t *testing.T) {
	s := testServer(t)
	report := map[string]any{
		"checks": []any{map[string]any{"name": "a", "status": "pass"}},
	}
	rec := postJSON(t, s, "/api/snapshots/save", map[string]any{
		"kind": "conformance", "name": "api test", "report": report,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	req := httptest.NewRequest(http.MethodGet, "/api/snapshots", nil)
	rec2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec2, req)
	var list map[string]any
	if err := json.Unmarshal(rec2.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	snaps, _ := list["snapshots"].([]any)
	if len(snaps) != 1 {
		t.Fatalf("expected 1 snapshot, got %v", list)
	}
	// Diff against itself: no changes.
	req3 := httptest.NewRequest(http.MethodGet, "/api/snapshots/diff?before=api-test&after=api-test", nil)
	rec3 := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Fatalf("diff: %d %s", rec3.Code, rec3.Body.String())
	}
	var d map[string]any
	_ = json.Unmarshal(rec3.Body.Bytes(), &d)
	if changed, _ := d["changed"].(float64); changed != 0 {
		t.Errorf("expected no changes, got %v", d)
	}
	// Delete.
	rec4 := postJSON(t, s, "/api/snapshots/delete", map[string]any{"name": "api-test"})
	if rec4.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec4.Code, rec4.Body.String())
	}
}

func TestReportAPIBuilds(t *testing.T) {
	s := testServer(t)
	conf := map[string]any{
		"checks": []any{map[string]any{"name": "a", "status": "pass"}},
	}
	// HTML.
	rec := postJSON(t, s, "/api/reports/build", map[string]any{
		"title": "api evidence", "format": "html", "conformance": conf,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("html: %d %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("content type %q", ct)
	}
	if !strings.Contains(rec.Body.String(), "api evidence") {
		t.Error("title missing from HTML")
	}
	// PDF.
	rec2 := postJSON(t, s, "/api/reports/build", map[string]any{
		"format": "pdf", "conformance": conf,
	})
	if rec2.Code != http.StatusOK {
		t.Fatalf("pdf: %d %s", rec2.Code, rec2.Body.String())
	}
	if ct := rec2.Header().Get("Content-Type"); ct != "application/pdf" {
		t.Errorf("content type %q", ct)
	}
	if !bytes.HasPrefix(rec2.Body.Bytes(), []byte("%PDF")) {
		t.Error("missing PDF magic")
	}
	// No inputs -> 400.
	rec3 := postJSON(t, s, "/api/reports/build", map[string]any{"format": "html"})
	if rec3.Code != http.StatusBadRequest {
		t.Errorf("expected 400 with no inputs, got %d", rec3.Code)
	}
}
