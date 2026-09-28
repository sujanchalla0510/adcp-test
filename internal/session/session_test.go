package session

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStoreNewGetList(t *testing.T) {
	st := NewStore()
	a := st.New("https://seller.example/mcp")
	b := st.New("https://other.example/mcp")
	if a.ID == "" || a.ID == b.ID {
		t.Fatalf("session IDs not unique: %q vs %q", a.ID, b.ID)
	}
	got, ok := st.Get(a.ID)
	if !ok || got != a {
		t.Fatal("Get did not return the created session")
	}
	if _, ok := st.Get("nope"); ok {
		t.Fatal("Get returned a session for an unknown ID")
	}
	list := st.List()
	if len(list) != 2 || list[0] != a || list[1] != b {
		t.Fatalf("List order wrong: %v", list)
	}
	// NewWithID is idempotent.
	again := st.NewWithID(a.ID, "https://changed.example/")
	if again != a {
		t.Fatal("NewWithID did not return the existing session")
	}
	if a.TargetURL != "https://seller.example/mcp" {
		t.Fatalf("NewWithID overwrote target URL: %q", a.TargetURL)
	}
}

func TestAppendAndRedaction(t *testing.T) {
	st := NewStore()
	sess := st.New("https://seller.example/mcp")

	h := http.Header{}
	h.Set("Authorization", "Bearer super-secret")
	h.Set("Signature", "sig-raw-value")
	h.Set("Content-Type", "application/json")
	out := NewOutStep("tools/call", "get_products",
		"req-1",
		[]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_products","arguments":{"brief":"shoes","token":"abc123","nested":{"api_key":"k"}}}}`),
		h)
	sess.Append(out)

	in := NewInStep("tools/call", "get_products", "req-1",
		[]byte(`{"jsonrpc":"2.0","id":1,"result":{"products":[],"session":"sess-secret"}}`),
		25*time.Millisecond)
	sess.Append(in)

	if sess.Len() != 2 {
		t.Fatalf("Len = %d, want 2", sess.Len())
	}
	steps := sess.Snapshot()
	if string(steps[0].Request) == "" || steps[0].Direction != DirectionOut {
		t.Fatal("out step malformed")
	}
	for _, raw := range []string{string(steps[0].Request), string(steps[1].Response)} {
		for _, secret := range []string{"super-secret", "abc123", "sess-secret", "sig-raw-value"} {
			if strings.Contains(raw, secret) {
				t.Fatalf("secret %q leaked into stored payload: %s", secret, raw)
			}
		}
	}
	if !strings.Contains(string(steps[0].Request), "[REDACTED]") {
		t.Fatal("expected redaction markers in stored request")
	}
	if got := steps[0].Headers["Authorization"]; got != "[REDACTED]" {
		t.Fatalf("Authorization header = %q, want [REDACTED]", got)
	}
	if got := steps[0].Headers["Signature"]; got != "[REDACTED]" {
		t.Fatalf("Signature header = %q, want [REDACTED]", got)
	}
	if got := steps[0].Headers["Content-Type"]; got != "application/json" {
		t.Fatalf("Content-Type header = %q, want preserved", got)
	}
	if steps[1].DurationMs <= 0 {
		t.Fatalf("DurationMs = %v, want > 0", steps[1].DurationMs)
	}

	errStep := NewErrorStep("tools/call", "get_products", "req-2", "dial tcp: refused", time.Millisecond)
	if errStep.Error == "" || errStep.Direction != DirectionIn {
		t.Fatal("error step malformed")
	}
}

func TestJSONRoundTrip(t *testing.T) {
	st := NewStore()
	sess := st.New("https://seller.example/mcp")
	sess.Append(NewOutStep("tools/list", "", "1", []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`), http.Header{}))
	sess.Append(NewInStep("tools/list", "", "1", []byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`), time.Millisecond))

	b, err := json.Marshal(sess)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back Session
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.ID != sess.ID || back.TargetURL != sess.TargetURL || len(back.Steps) != 2 {
		t.Fatalf("round trip lost data: id=%q steps=%d", back.ID, len(back.Steps))
	}
	if back.Steps[0].Method != "tools/list" || back.Steps[1].Direction != DirectionIn {
		t.Fatalf("steps corrupted: %+v", back.Steps)
	}
}

func TestConcurrentAppend(t *testing.T) {
	st := NewStore()
	sess := st.New("https://seller.example/mcp")
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				id := fmt.Sprintf("g%d-%d", g, i)
				sess.Append(NewOutStep("tools/call", "get_products", id, []byte(`{"id":1}`), http.Header{}))
				sess.Append(NewInStep("tools/call", "get_products", id, []byte(`{"id":1}`), time.Millisecond))
				_ = sess.Snapshot()
				_, _ = json.Marshal(sess)
			}
		}(g)
	}
	wg.Wait()
	if got := sess.Len(); got != 800 {
		t.Fatalf("Len = %d, want 800", got)
	}
}
