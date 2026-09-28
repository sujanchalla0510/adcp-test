package cassette

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sujanchalla0510/adcp-test/internal/recorder"
	"github.com/sujanchalla0510/adcp-test/internal/session"
)

// upstreamFixture is a synthetic seller: deterministic per tool.
func upstreamFixture() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		switch req.Method {
		case "tools/list":
			resp["result"] = map[string]any{"tools": []any{
				map[string]any{"name": "get_products"},
				map[string]any{"name": "create_media_buy"},
			}}
		case "tools/call":
			var p struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(req.Params, &p)
			resp["result"] = map[string]any{"tool": p.Name, "ok": true}
		default:
			resp["error"] = map[string]any{"code": -32601, "message": "Method not found"}
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func rpcPost(t *testing.T, url, body string) map[string]any {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, b)
	}
	return out
}

// recordFixture proxies three calls through the recorder and returns the
// store, so tests exercise the real capture path.
func recordFixture(t *testing.T) *session.Store {
	t.Helper()
	up := upstreamFixture()
	t.Cleanup(up.Close)

	store := session.NewStore()
	rec, err := recorder.NewRecorder(up.URL, store)
	if err != nil {
		t.Fatal(err)
	}
	proxyURL, err := rec.Start("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rec.Close() })

	post := func(body, sid string) {
		req, _ := http.NewRequest(http.MethodPost, proxyURL, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer live-buyer-secret")
		req.Header.Set(recorder.SessionIDHeader, sid)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	post(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`, "fixture")
	post(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_products","arguments":{"brief":"shoes","token":"live-arg-secret"}}}`, "fixture")
	post(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"create_media_buy","arguments":{"account":"a","brand":"b","start_time":"x","end_time":"y"}}}`, "fixture")
	return store
}

func TestFromSession(t *testing.T) {
	store := recordFixture(t)
	sess, ok := store.Get("fixture")
	if !ok {
		t.Fatal("fixture session missing")
	}
	c := FromSession(sess)
	if c.Version != CassetteVersion {
		t.Fatalf("version = %d", c.Version)
	}
	if c.Target == "" {
		t.Fatal("target not carried over")
	}
	if len(c.Exchanges) != 3 {
		t.Fatalf("exchanges = %d, want 3", len(c.Exchanges))
	}
	ex := c.Exchanges[1]
	if ex.Method != "tools/call" || ex.Tool != "get_products" {
		t.Fatalf("exchange 1 = %s %s", ex.Method, ex.Tool)
	}
	if !bytes.Contains(ex.Arguments, []byte(`"brief":"shoes"`)) {
		t.Fatalf("arguments not extracted: %s", ex.Arguments)
	}
	if !bytes.Contains(ex.Response, []byte(`"tool":"get_products"`)) {
		t.Fatalf("response not recorded: %s", ex.Response)
	}
	// Secrets redacted before they ever reach the cassette.
	for _, secret := range []string{"live-buyer-secret", "live-arg-secret"} {
		if bytes.Contains(ex.Arguments, []byte(secret)) || bytes.Contains(ex.Response, []byte(secret)) {
			t.Fatalf("secret %q leaked into cassette", secret)
		}
	}
	if ex.DurationMs <= 0 {
		t.Fatal("duration not recorded")
	}
}

func TestWriteLoadRoundTrip(t *testing.T) {
	store := recordFixture(t)
	sess, _ := store.Get("fixture")
	c := FromSession(sess)

	path := filepath.Join(t.TempDir(), "s.cassette.json")
	if err := c.WriteFile(path); err != nil {
		t.Fatal(err)
	}
	// The file itself must not contain raw secrets.
	raw, _ := os.ReadFile(path)
	for _, secret := range []string{"live-buyer-secret", "live-arg-secret"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("secret %q in cassette file", secret)
		}
	}
	loaded, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Exchanges) != len(c.Exchanges) || loaded.Target != c.Target {
		t.Fatal("load round trip lost data")
	}

	if _, err := LoadFile(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("LoadFile of missing file succeeded")
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	_ = os.WriteFile(bad, []byte(`{"version":999,"exchanges":[]}`), 0o600)
	if _, err := LoadFile(bad); err == nil {
		t.Fatal("LoadFile accepted unknown version")
	}
}

func TestReplayIdenticalResponses(t *testing.T) {
	store := recordFixture(t)
	sess, _ := store.Get("fixture")
	c := FromSession(sess)

	srv := &Server{Cassette: c}
	replayURL, err := srv.Start("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	// Replay the same calls with DIFFERENT request ids: responses must be
	// identical apart from the rewritten id.
	cases := []struct{ body, wantTool string }{
		{`{"jsonrpc":"2.0","id":"a","method":"tools/list","params":{}}`, ""},
		{`{"jsonrpc":"2.0","id":"b","method":"tools/call","params":{"name":"get_products","arguments":{"brief":"shoes","token":"live-arg-secret","extra":"ignored"}}}`, "get_products"},
		{`{"jsonrpc":"2.0","id":"c","method":"tools/call","params":{"name":"create_media_buy","arguments":{"account":"a","brand":"b","start_time":"x","end_time":"y"}}}`, "create_media_buy"},
	}
	for _, tc := range cases {
		got := rpcPost(t, replayURL, tc.body)
		var wantID string
		_ = json.Unmarshal([]byte(tc.body), &struct {
			ID *string `json:"id"`
		}{ID: &wantID})
		if got["id"] != wantID {
			t.Fatalf("replay id = %v, want %q (id must be rewritten)", got["id"], wantID)
		}
		res, _ := got["result"].(map[string]any)
		if res == nil {
			t.Fatalf("no result for %s: %v", tc.body, got)
		}
		if tc.wantTool != "" && res["tool"] != tc.wantTool {
			t.Fatalf("result.tool = %v, want %q", res["tool"], tc.wantTool)
		}
	}

	// tools/list matches regardless of params shape in both modes.
	got := rpcPost(t, replayURL, `{"jsonrpc":"2.0","id":9,"method":"tools/list"}`)
	if _, ok := got["result"]; !ok {
		t.Fatalf("tools/list without params failed: %v", got)
	}
}

func TestReplayUnmatchedIsStructuredError(t *testing.T) {
	store := recordFixture(t)
	sess, _ := store.Get("fixture")

	for _, strict := range []bool{false, true} {
		srv := &Server{Cassette: FromSession(sess), Strict: strict}
		replayURL, err := srv.Start("127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		got := rpcPost(t, replayURL,
			`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"no_such_tool","arguments":{}}}`)
		errObj, _ := got["error"].(map[string]any)
		if errObj == nil {
			t.Fatalf("strict=%v: unmatched tool did not yield error envelope: %v", strict, got)
		}
		if code, _ := errObj["code"].(float64); code != -32000 {
			t.Fatalf("strict=%v: error code = %v, want -32000", strict, code)
		}
		if got["id"] != float64(1) {
			t.Fatalf("strict=%v: error envelope id = %v, want 1", strict, got["id"])
		}
		srv.Close()
	}
}

func TestReplayStrictVsLenient(t *testing.T) {
	c := &Cassette{
		Version: CassetteVersion,
		Exchanges: []Exchange{
			{
				Method:    "tools/call",
				Tool:      "get_products",
				Arguments: json.RawMessage(`{"brief":"shoes"}`),
				Response:  json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"v":"exact"}}`),
			},
			{
				Method:    "tools/call",
				Tool:      "get_products",
				Arguments: json.RawMessage(`{"brief":"shoes","color":"red"}`),
				Response:  json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"v":"red"}}`),
			},
		},
	}

	// Lenient: recorded args subset of incoming args -> first subset match.
	lsrv := &Server{Cassette: c}
	lurl, _ := lsrv.Start("127.0.0.1:0")
	defer lsrv.Close()
	got := rpcPost(t, lurl, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_products","arguments":{"brief":"shoes","color":"red","size":9}}}`)
	if res := got["result"].(map[string]any); res["v"] != "exact" {
		t.Fatalf("lenient subset match picked %v, want exact (first subset)", res["v"])
	}

	// Strict: only deep-equal arguments match.
	ssrv := &Server{Cassette: c, Strict: true}
	surl, _ := ssrv.Start("127.0.0.1:0")
	defer ssrv.Close()
	got = rpcPost(t, surl, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_products","arguments":{"brief":"shoes","color":"red"}}}`)
	if res, ok := got["result"].(map[string]any); !ok || res["v"] != "red" {
		t.Fatalf("strict exact match failed: %v", got)
	}
	got = rpcPost(t, surl, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_products","arguments":{"brief":"shoes","extra":1}}}`)
	if _, ok := got["error"]; !ok {
		t.Fatalf("strict mode matched on subset, want structured error: %v", got)
	}

	// Lenient fallback: no argument match at all -> first exchange for the tool.
	got = rpcPost(t, lurl, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_products","arguments":{"brief":"hats"}}}`)
	if res, ok := got["result"].(map[string]any); !ok || res["v"] != "exact" {
		t.Fatalf("lenient fallback failed: %v", got)
	}
}

func TestReplayRecordedError(t *testing.T) {
	c := &Cassette{
		Version: CassetteVersion,
		Exchanges: []Exchange{
			{Method: "tools/call", Tool: "boom", Error: "dial tcp: refused"},
		},
	}
	srv := &Server{Cassette: c}
	url, _ := srv.Start("127.0.0.1:0")
	defer srv.Close()
	got := rpcPost(t, url, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"boom","arguments":{}}}`)
	errObj, _ := got["error"].(map[string]any)
	if errObj == nil || !strings.Contains(errObj["message"].(string), "dial tcp") {
		t.Fatalf("recorded transport error not replayed as error: %v", got)
	}
}

func TestJsonSubset(t *testing.T) {
	cases := []struct {
		rec, inc string
		want     bool
	}{
		{`{"a":1}`, `{"a":1,"b":2}`, true},
		{`{"a":1,"b":2}`, `{"a":1}`, false},
		{`{"a":{"x":1}}`, `{"a":{"x":1,"y":2}}`, true},
		{`{"a":[1,2]}`, `{"a":[1,2]}`, true},
		{`{"a":[1,2]}`, `{"a":[1,2,3]}`, false}, // arrays compare by equality
		{`{}`, `{"a":1}`, true},
		{``, `{"a":1}`, true}, // empty recorded matches anything
		{`{"a":1}`, `{"a":2}`, false},
		{`{"a":1}`, `{"a":1.0}`, true}, // JSON number normalization
	}
	for _, tc := range cases {
		if got := jsonSubset(json.RawMessage(tc.rec), json.RawMessage(tc.inc)); got != tc.want {
			t.Fatalf("jsonSubset(%s, %s) = %v, want %v", tc.rec, tc.inc, got, tc.want)
		}
	}
	if !jsonDeepEqual(json.RawMessage(`{"a":1}`), json.RawMessage(`{"a":1.0}`)) {
		t.Fatal("jsonDeepEqual missed numeric equality")
	}
}

func TestFromSessionsMergesInTimeOrder(t *testing.T) {
	mk := func(tool string, delay time.Duration) *session.Session {
		s := session.NewStore().New("https://x.example/")
		time.Sleep(delay)
		s.Append(session.NewOutStep("tools/call", tool, "1", []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call"}`), nil))
		return s
	}
	a := mk("first", 0)
	b := mk("second", 5*time.Millisecond)
	merged := FromSessions([]*session.Session{b, a}) // input order shuffled on purpose
	if len(merged.Exchanges) != 2 {
		t.Fatalf("exchanges = %d", len(merged.Exchanges))
	}
	if merged.Exchanges[0].Tool != "first" || merged.Exchanges[1].Tool != "second" {
		t.Fatalf("merge order wrong: %v", merged.Exchanges)
	}
}
