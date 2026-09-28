package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// inspectFakeSeller is a synthetic seller for the record/replay flow.
func inspectFakeSeller() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		if req.Method == "tools/list" {
			resp["result"] = map[string]any{"tools": []any{map[string]any{"name": "get_products"}}}
		} else {
			resp["result"] = map[string]any{"ok": true}
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func doAPI(t *testing.T, srv *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestRecordStartStopFlow(t *testing.T) {
	seller := inspectFakeSeller()
	defer seller.Close()
	srv := newTestServer()

	// Start a recording session.
	rec := doAPI(t, srv, http.MethodPost, "/api/record/start",
		`{"upstream_url":`+quoteJSON(seller.URL)+`}`)
	if rec.Result().StatusCode != http.StatusOK {
		t.Fatalf("record/start status = %d", rec.Result().StatusCode)
	}
	var started struct {
		SessionID string `json:"session_id"`
		ProxyURL  string `json:"proxy_url"`
	}
	if err := json.NewDecoder(rec.Result().Body).Decode(&started); err != nil {
		t.Fatalf("record/start not JSON: %v", err)
	}
	if started.SessionID == "" || !strings.HasPrefix(started.ProxyURL, "http://127.0.0.1:") {
		t.Fatalf("bad record/start response: %+v", started)
	}

	// Send traffic through the proxy, pinned to the session.
	rpcBody := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	req, _ := http.NewRequest(http.MethodPost, started.ProxyURL, strings.NewReader(rpcBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Session-ID", started.SessionID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("proxy status = %d", resp.StatusCode)
	}

	// The session now appears in the list.
	rec = doAPI(t, srv, http.MethodGet, "/api/sessions", "")
	var sessions []struct {
		ID    string `json:"id"`
		Steps int    `json:"steps"`
	}
	if err := json.NewDecoder(rec.Result().Body).Decode(&sessions); err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ID != started.SessionID || sessions[0].Steps != 2 {
		t.Fatalf("sessions = %+v", sessions)
	}

	// Session detail carries the ordered steps with payloads.
	rec = doAPI(t, srv, http.MethodGet, "/api/sessions/"+started.SessionID, "")
	var detail struct {
		ID    string `json:"id"`
		Steps []struct {
			Direction string `json:"direction"`
			Method    string `json:"method"`
			Request   any    `json:"request"`
			Response  any    `json:"response"`
		} `json:"steps"`
	}
	if err := json.NewDecoder(rec.Result().Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.Steps) != 2 || detail.Steps[0].Direction != "out" || detail.Steps[1].Direction != "in" {
		t.Fatalf("detail steps = %+v", detail.Steps)
	}

	// Stop the recording: cassette saved under a temp working dir.
	cassetteHome := t.TempDir()
	cwd, _ := os.Getwd()
	if err := os.Chdir(cassetteHome); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	rec = doAPI(t, srv, http.MethodPost, "/api/record/stop",
		`{"session_id":`+quoteJSON(started.SessionID)+`}`)
	if rec.Result().StatusCode != http.StatusOK {
		body, _ := io.ReadAll(rec.Result().Body)
		t.Fatalf("record/stop status = %d: %s", rec.Result().StatusCode, body)
	}
	var stopped struct {
		Cassette  string `json:"cassette"`
		Path      string `json:"path"`
		Exchanges int    `json:"exchanges"`
	}
	if err := json.NewDecoder(rec.Result().Body).Decode(&stopped); err != nil {
		t.Fatal(err)
	}
	if stopped.Exchanges != 1 || stopped.Cassette == "" {
		t.Fatalf("record/stop = %+v", stopped)
	}
	if _, err := os.Stat(filepath.Join(cassetteHome, stopped.Path)); err != nil {
		t.Fatalf("cassette file not written: %v", err)
	}

	// The cassette is listed and replayable.
	rec = doAPI(t, srv, http.MethodGet, "/api/cassettes", "")
	var cassettes []struct {
		Name      string `json:"name"`
		Exchanges int    `json:"exchanges"`
	}
	if err := json.NewDecoder(rec.Result().Body).Decode(&cassettes); err != nil {
		t.Fatal(err)
	}
	if len(cassettes) != 1 || cassettes[0].Name != stopped.Cassette {
		t.Fatalf("cassettes = %+v", cassettes)
	}

	rec = doAPI(t, srv, http.MethodPost, "/api/replay/start",
		`{"cassette":`+quoteJSON(stopped.Cassette)+`}`)
	if rec.Result().StatusCode != http.StatusOK {
		t.Fatalf("replay/start status = %d", rec.Result().StatusCode)
	}
	var replay struct {
		ReplayID  string `json:"replay_id"`
		ReplayURL string `json:"replay_url"`
	}
	if err := json.NewDecoder(rec.Result().Body).Decode(&replay); err != nil {
		t.Fatal(err)
	}
	if replay.ReplayID == "" || !strings.HasPrefix(replay.ReplayURL, "http://127.0.0.1:") {
		t.Fatalf("replay/start = %+v", replay)
	}

	// The replay server answers the recorded call with a rewritten id.
	resp2, err := http.Post(replay.ReplayURL, "application/json",
		strings.NewReader(`{"jsonrpc":"2.0","id":"xyz","method":"tools/list","params":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		ID     any            `json:"id"`
		Result map[string]any `json:"result"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if got.ID != "xyz" || got.Result == nil {
		t.Fatalf("replay response = %+v", got)
	}

	rec = doAPI(t, srv, http.MethodPost, "/api/replay/stop",
		`{"replay_id":`+quoteJSON(replay.ReplayID)+`}`)
	if rec.Result().StatusCode != http.StatusOK {
		t.Fatalf("replay/stop status = %d", rec.Result().StatusCode)
	}
}

func TestInspectEndpointsBadRequests(t *testing.T) {
	srv := newTestServer()
	cases := []struct {
		name   string
		method string
		path   string
		body   string
		want   int
	}{
		{"record start no body", "POST", "/api/record/start", ``, 400},
		{"record start bad url", "POST", "/api/record/start", `{"upstream_url":"ftp://x/y"}`, 400},
		{"record stop unknown session", "POST", "/api/record/stop", `{"session_id":"nope"}`, 404},
		{"session unknown", "GET", "/api/sessions/nope", ``, 404},
		{"replay start unknown cassette", "POST", "/api/replay/start", `{"cassette":"nope"}`, 404},
		{"replay stop unknown", "POST", "/api/replay/stop", `{"replay_id":"r-999"}`, 404},
		{"replay start no body", "POST", "/api/replay/start", `{}`, 400},
		{"sessions wrong method", "POST", "/api/sessions", ``, 404}, // mux: no method match -> not routed
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doAPI(t, srv, tc.method, tc.path, tc.body)
			if rec.Result().StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", rec.Result().StatusCode, tc.want)
			}
		})
	}
}

func TestRecordStartEmptySessionStopFails(t *testing.T) {
	seller := inspectFakeSeller()
	defer seller.Close()
	srv := newTestServer()

	rec := doAPI(t, srv, http.MethodPost, "/api/record/start",
		`{"upstream_url":`+quoteJSON(seller.URL)+`}`)
	var started struct {
		SessionID string `json:"session_id"`
	}
	_ = json.NewDecoder(rec.Result().Body).Decode(&started)

	// Stopping a session with no traffic is a 400: there is no cassette.
	rec = doAPI(t, srv, http.MethodPost, "/api/record/stop",
		`{"session_id":`+quoteJSON(started.SessionID)+`}`)
	if rec.Result().StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for empty session", rec.Result().StatusCode)
	}
}

func TestSanitizeFileName(t *testing.T) {
	if sanitizeFileName("s-abc123") != "s-abc123" {
		t.Fatal("valid name mangled")
	}
	if sanitizeFileName("a/b\\c:d") != "a_b_c_d" {
		t.Fatalf("unsafe chars not replaced: %q", sanitizeFileName("a/b\\c:d"))
	}
	if sanitizeFileName("") != "session" {
		t.Fatal("empty name fallback wrong")
	}
}
