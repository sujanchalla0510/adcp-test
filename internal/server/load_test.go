package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// loadFakeSeller answers tools/call with {"ok": true}.
func loadFakeSeller() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID any `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"ok": true},
		})
	}))
}

func TestLoadPresetsEndpoint(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/api/load/presets", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	var body struct {
		Presets []struct {
			ID   string `json:"id"`
			YAML string `json:"yaml"`
		} `json:"presets"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Presets) == 0 {
		t.Fatal("no presets returned")
	}
	for _, p := range body.Presets {
		if p.YAML == "" {
			t.Errorf("preset %q has empty yaml", p.ID)
		}
	}
}

func TestLoadRunEndpointStreamsResult(t *testing.T) {
	fake := loadFakeSeller()
	defer fake.Close()

	srv := newTestServer()
	reqBody, _ := json.Marshal(map[string]any{
		"target_url":  fake.URL,
		"tool":        "get_products",
		"concurrency": 2,
		"iterations":  10,
		"thresholds":  map[string]float64{"error_rate_lt": 0.01},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/load/run", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("status = %d, want 200: %s", res.StatusCode, body)
	}
	events := sseEvents(t, res.Body)
	if len(events["progress"]) == 0 {
		t.Error("no progress events streamed")
	}
	if len(events["result"]) != 1 {
		t.Fatalf("result events = %d, want 1", len(events["result"]))
	}
	var payload struct {
		ID     string `json:"id"`
		Result struct {
			TotalRequests int64   `json:"total_requests"`
			P99Ms         float64 `json:"p99_ms"`
			Passed        bool    `json:"passed"`
		} `json:"result"`
	}
	if err := json.Unmarshal(events["result"][0], &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ID == "" {
		t.Error("result event missing id")
	}
	if payload.Result.TotalRequests != 10 || !payload.Result.Passed {
		t.Errorf("unexpected result: %+v", payload.Result)
	}

	// The stored result must be retrievable.
	getReq := httptest.NewRequest(http.MethodGet, "/api/load/results/"+payload.ID, nil)
	getRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(getRec, getReq)
	getRes := getRec.Result()
	defer getRes.Body.Close()
	if getRes.StatusCode != http.StatusOK {
		t.Fatalf("GET result status = %d, want 200", getRes.StatusCode)
	}
	var stored struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(getRes.Body).Decode(&stored); err != nil {
		t.Fatal(err)
	}
	if stored.ID != payload.ID {
		t.Errorf("stored id = %q, want %q", stored.ID, payload.ID)
	}
}

func TestLoadRunEndpointRejectsRemote(t *testing.T) {
	srv := newTestServer()
	reqBody, _ := json.Marshal(map[string]any{
		"target_url":  "https://seller.example.com/mcp",
		"tool":        "get_products",
		"concurrency": 1,
		"iterations":  1,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/load/run", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (remote guard)", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), "non-localhost") {
		t.Errorf("error body should mention the guard: %s", body)
	}
}

func TestLoadResultEndpointUnknownID(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/api/load/results/nope", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	res := rec.Result()
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", res.StatusCode)
	}
}
