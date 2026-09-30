package recorder

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/sujanchalla0510/adcp-test/internal/session"
)

// fakeSeller echoes a canned result per method and records what it saw.
type fakeSeller struct {
	t       *testing.T
	mu      sync.Mutex
	sawAuth []string
	sawSID  []string // X-Session-ID values that leaked upstream (must stay empty)
}

func (f *fakeSeller) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.Unmarshal(body, &req)
		f.mu.Lock()
		f.sawAuth = append(f.sawAuth, r.Header.Get("Authorization"))
		f.sawSID = append(f.sawSID, r.Header.Get(SessionIDHeader))
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		switch req.Method {
		case "tools/list":
			resp["result"] = map[string]any{"tools": []any{
				map[string]any{"name": "get_products"},
			}}
		case "tools/call":
			resp["result"] = map[string]any{"echo": json.RawMessage(req.Params)}
		default:
			resp["error"] = map[string]any{"code": -32601, "message": "Method not found"}
		}
		_ = json.NewEncoder(w).Encode(resp)
	})
}

func startRecorder(t *testing.T, upstream string) (*Recorder, string) {
	t.Helper()
	rec, err := NewRecorder(upstream, session.NewStore())
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	proxyURL, err := rec.Start("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = rec.Close() })
	return rec, proxyURL
}

func postRPC(t *testing.T, url, body string, headers map[string]string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func TestProxyRoundTrip(t *testing.T) {
	seller := &fakeSeller{t: t}
	up := httptest.NewServer(seller.handler())
	defer up.Close()
	rec, proxyURL := startRecorder(t, up.URL)

	callBody := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"get_products","arguments":{"buying_mode":"brief","brief":"running shoes","token":"live-secret-token"}}}`
	code, respBody := postRPC(t, proxyURL, callBody, map[string]string{
		"Authorization": "Bearer buyer-secret",
		SessionIDHeader: "pinned-1",
	})
	if code != http.StatusOK {
		t.Fatalf("proxy status = %d, body: %s", code, respBody)
	}
	var resp struct {
		ID     any `json:"id"`
		Result struct {
			Echo json.RawMessage `json:"echo"`
		} `json:"result"`
	}
	if err := json.Unmarshal(respBody, &resp); err != nil {
		t.Fatalf("proxy response not JSON-RPC: %v\n%s", err, respBody)
	}
	if fmt.Sprintf("%v", resp.ID) != "7" {
		t.Fatalf("response id = %v, want 7 (id must round-trip)", resp.ID)
	}

	// Upstream saw the auth header but never our control header.
	seller.mu.Lock()
	auth, sids := seller.sawAuth, seller.sawSID
	seller.mu.Unlock()
	if len(auth) != 1 || auth[0] != "Bearer buyer-secret" {
		t.Fatalf("upstream Authorization = %v, want forwarded", auth)
	}
	for _, s := range sids {
		if s != "" {
			t.Fatalf("X-Session-ID leaked upstream: %q", s)
		}
	}

	// The pinned session holds one out step and one in step.
	sess, ok := rec.Store.Get("pinned-1")
	if !ok {
		t.Fatal("pinned session not created")
	}
	steps := sess.Snapshot()
	if len(steps) != 2 {
		t.Fatalf("steps = %d, want 2 (out + in)", len(steps))
	}
	out, in := steps[0], steps[1]
	if out.Direction != session.DirectionOut || in.Direction != session.DirectionIn {
		t.Fatalf("directions = %q, %q", out.Direction, in.Direction)
	}
	if out.Tool != "get_products" || in.Tool != "get_products" || out.RequestID != in.RequestID {
		t.Fatalf("out/in pairing broken: %+v / %+v", out, in)
	}
	if in.DurationMs <= 0 {
		t.Fatal("in step has no duration")
	}
	// Secrets redacted at capture.
	for _, raw := range []string{string(out.Request), string(in.Response)} {
		for _, secret := range []string{"live-secret-token", "buyer-secret"} {
			if strings.Contains(raw, secret) {
				t.Fatalf("secret %q persisted in session", secret)
			}
		}
	}
	if out.Headers["Authorization"] != "[REDACTED]" {
		t.Fatalf("stored Authorization header = %q", out.Headers["Authorization"])
	}
	// get_products with buying_mode (the required arg) satisfies expectations: no arg problems.
	if len(out.ArgProblems) != 0 {
		t.Fatalf("ArgProblems = %v, want none", out.ArgProblems)
	}
}

func TestArgValidationBadge(t *testing.T) {
	seller := &fakeSeller{t: t}
	up := httptest.NewServer(seller.handler())
	defer up.Close()
	rec, proxyURL := startRecorder(t, up.URL)

	// create_media_buy without required fields -> problems recorded.
	postRPC(t, proxyURL,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"create_media_buy","arguments":{"account":"a"}}}`,
		map[string]string{SessionIDHeader: "s2"})
	sess, _ := rec.Store.Get("s2")
	steps := sess.Snapshot()
	if len(steps) != 2 {
		t.Fatalf("steps = %d, want 2", len(steps))
	}
	if len(steps[0].ArgProblems) == 0 {
		t.Fatal("expected arg problems for incomplete create_media_buy")
	}
}

func TestConnectionBasedSessions(t *testing.T) {
	seller := &fakeSeller{t: t}
	up := httptest.NewServer(seller.handler())
	defer up.Close()
	rec, proxyURL := startRecorder(t, up.URL)

	// Two requests without X-Session-ID over separate connections land
	// in sessions keyed by RemoteAddr; both must be recorded.
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	postRPC(t, proxyURL, body, nil)
	postRPC(t, proxyURL, body, nil)
	if n := len(rec.Store.List()); n == 0 {
		t.Fatal("no sessions recorded")
	}
	total := 0
	for _, s := range rec.Store.List() {
		total += s.Len()
	}
	if total != 4 {
		t.Fatalf("total steps = %d, want 4", total)
	}
}

func TestUpstreamDown(t *testing.T) {
	rec, err := NewRecorder("http://127.0.0.1:1/unreachable", session.NewStore())
	if err != nil {
		t.Fatal(err)
	}
	proxyURL, err := rec.Start("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer rec.Close()

	code, body := postRPC(t, proxyURL,
		`{"jsonrpc":"2.0","id":99,"method":"tools/call","params":{"name":"get_products","arguments":{}}}`,
		map[string]string{SessionIDHeader: "s-down"})
	if code != http.StatusOK {
		t.Fatalf("proxy status = %d, want 200 with JSON-RPC error envelope", code)
	}
	var env struct {
		ID    any `json:"id"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil || env.Error == nil {
		t.Fatalf("expected JSON-RPC error envelope, got: %s", body)
	}
	sess, _ := rec.Store.Get("s-down")
	steps := sess.Snapshot()
	if len(steps) != 2 || steps[1].Error == "" {
		t.Fatalf("expected out + error in-step, got %+v", steps)
	}
}

func TestNonJSONRPCPassthrough(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer up.Close()
	rec, proxyURL := startRecorder(t, up.URL)

	code, _ := postRPC(t, proxyURL, `this is not json`, map[string]string{SessionIDHeader: "s-raw"})
	if code != http.StatusOK {
		t.Fatalf("proxy status = %d for non-JSON body", code)
	}
	sess, _ := rec.Store.Get("s-raw")
	steps := sess.Snapshot()
	if len(steps) != 2 || steps[0].Method != "unknown" {
		t.Fatalf("non-JSON body not recorded as unknown: %+v", steps)
	}
}

func TestConcurrentProxy(t *testing.T) {
	seller := &fakeSeller{t: t}
	up := httptest.NewServer(seller.handler())
	defer up.Close()
	rec, proxyURL := startRecorder(t, up.URL)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/list","params":{}}`, i)
			postRPC(t, proxyURL, body, map[string]string{SessionIDHeader: "s-conc"})
		}(i)
	}
	wg.Wait()
	sess, _ := rec.Store.Get("s-conc")
	if got := sess.Len(); got != 40 {
		t.Fatalf("steps = %d, want 40", got)
	}
}

func TestNewRecorderBadUpstream(t *testing.T) {
	for _, u := range []string{"", "ftp://x/y", "not a url", "http://"} {
		if _, err := NewRecorder(u, nil); err == nil {
			t.Fatalf("NewRecorder(%q) accepted", u)
		}
	}
}

func TestSanitizeSessionID(t *testing.T) {
	if sanitizeSessionID("abc-123_X") != "abc-123_X" {
		t.Fatal("valid session ID rejected")
	}
	for _, bad := range []string{"", "has space", "semi;colon", strings.Repeat("x", 65), "dot.name"} {
		if sanitizeSessionID(bad) != "" {
			t.Fatalf("invalid session ID %q accepted", bad)
		}
	}
}

func TestSSEResponseRecordedUnwrapped(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID any `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"id\":%v,\"result\":{\"ok\":true}}\n\n", req.ID)
	}))
	defer up.Close()
	rec, proxyURL := startRecorder(t, up.URL)

	code, body := postRPC(t, proxyURL,
		`{"jsonrpc":"2.0","id":5,"method":"tools/list","params":{}}`,
		map[string]string{SessionIDHeader: "s-sse"})
	if code != 200 || !bytes.Contains(body, []byte("data:")) {
		t.Fatalf("SSE body not forwarded raw: %d %s", code, body)
	}
	sess, _ := rec.Store.Get("s-sse")
	steps := sess.Snapshot()
	if len(steps) != 2 {
		t.Fatalf("steps = %d", len(steps))
	}
	var env struct {
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal(steps[1].Response, &env); err != nil || env.Result["ok"] != true {
		t.Fatalf("recorded response not the unwrapped frame: %s", steps[1].Response)
	}
}
