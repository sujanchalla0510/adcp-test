package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sujanchalla0510/adcp-test/internal/scenarios"
)

// scenarioFakeSeller answers the happy-path pack's tools with canned
// synthetic results.
func scenarioFakeSeller() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
}

// sseEvents reads an SSE stream into event name -> decoded data payloads.
func sseEvents(t *testing.T, body io.Reader) map[string][]json.RawMessage {
	t.Helper()
	out := map[string][]json.RawMessage{}
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	var event string
	var data strings.Builder
	flush := func() {
		if event != "" {
			out[event] = append(out[event], json.RawMessage(strings.TrimSpace(data.String())))
		}
		event = ""
		data.Reset()
	}
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			data.WriteString(strings.TrimPrefix(line, "data:"))
		}
	}
	flush()
	if err := sc.Err(); err != nil {
		t.Fatalf("scan SSE: %v", err)
	}
	return out
}

func TestScenariosListEndpoint(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/api/scenarios", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	var body struct {
		Packs []struct {
			ID            string `json:"id"`
			ScenarioCount int    `json:"scenario_count"`
		} `json:"packs"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	metas, err := scenarios.BuiltinPacks()
	if err != nil {
		t.Fatal(err)
	}
	if len(body.Packs) != len(metas) {
		t.Fatalf("packs = %d, want %d (builtin pack count)", len(body.Packs), len(metas))
	}
	for _, p := range body.Packs {
		if p.ScenarioCount == 0 {
			t.Errorf("pack %q has no scenarios", p.ID)
		}
	}
}

func TestScenarioRunEndpointStreamsReport(t *testing.T) {
	fake := scenarioFakeSeller()
	defer fake.Close()

	srv := newTestServer()
	reqBody, _ := json.Marshal(map[string]any{
		"pack":       "happy-path-media-buy",
		"target_url": fake.URL,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/scenarios/run", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("status = %d, want 200: %s", res.StatusCode, body)
	}
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}
	events := sseEvents(t, res.Body)
	if len(events["step_finished"]) != 4 {
		t.Errorf("step_finished events = %d, want 4", len(events["step_finished"]))
	}
	if len(events["report"]) != 1 {
		t.Fatalf("report events = %d, want 1", len(events["report"]))
	}
	var report struct {
		Pack    string `json:"pack"`
		Summary struct {
			Total  int `json:"total"`
			Passed int `json:"passed"`
			Failed int `json:"failed"`
		} `json:"summary"`
		Scenarios []struct {
			Name      string `json:"name"`
			Passed    bool   `json:"passed"`
			SessionID string `json:"session_id"`
		} `json:"scenarios"`
	}
	if err := json.Unmarshal(events["report"][0], &report); err != nil {
		t.Fatal(err)
	}
	if report.Pack != "happy-path-media-buy" || report.Summary.Failed != 0 || report.Summary.Passed != 1 {
		t.Errorf("unexpected report summary: %+v", report.Summary)
	}
	// Scenario traffic must land in the Inspect session store.
	sess, ok := srv.sessions.Get(report.Scenarios[0].SessionID)
	if !ok {
		t.Fatal("scenario session missing from server session store")
	}
	if sess.Len() == 0 {
		t.Error("scenario session has no recorded steps")
	}
}

func TestScenarioRunEndpointBadPack(t *testing.T) {
	srv := newTestServer()
	reqBody, _ := json.Marshal(map[string]any{
		"pack":       "no-such-pack",
		"target_url": "http://127.0.0.1:1",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/scenarios/run", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if res := rec.Result(); res.StatusCode != http.StatusBadRequest {
		res.Body.Close()
		t.Fatalf("status = %d, want 400", res.StatusCode)
	} else {
		res.Body.Close()
	}
}

// TestScenarioRunEndpointChaosGuard: a chaos run against a non-localhost
// target is refused by the API with an SSE error event; with allow_remote
// the guard is bypassed (the run then fails on transport, not the guard).
func TestScenarioRunEndpointChaosGuard(t *testing.T) {
	srv := newTestServer()
	run := func(body map[string]any) map[string][]json.RawMessage {
		reqBody, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/scenarios/run", bytes.NewReader(reqBody))
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		res := rec.Result()
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", res.StatusCode)
		}
		return sseEvents(t, res.Body)
	}

	events := run(map[string]any{
		"pack": "happy-path-media-buy", "target_url": "https://seller.example/mcp",
		"chaos": true, "chaos_seed": 1,
	})
	if len(events["error"]) != 1 {
		t.Fatalf("error events = %d, want 1", len(events["error"]))
	}
	if !strings.Contains(string(events["error"][0]), "non-localhost") {
		t.Errorf("expected a localhost-guard error, got: %s", events["error"][0])
	}
	if len(events["report"]) != 0 {
		t.Error("guarded run must not produce a report")
	}

	events = run(map[string]any{
		"pack": "happy-path-media-buy", "target_url": "https://seller.example/mcp",
		"chaos": true, "chaos_seed": 1, "allow_remote": true,
	})
	for _, raw := range events["error"] {
		if strings.Contains(string(raw), "non-localhost") {
			t.Errorf("guard fired despite allow_remote: %s", raw)
		}
	}
	if len(events["report"]) != 1 {
		t.Fatalf("report events = %d, want 1 (run proceeds past the guard)", len(events["report"]))
	}
}
