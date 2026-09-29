package fuzz

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// rpcErrorServer answers every request with a well-formed JSON-RPC
// error: the well-behaved target.
func rpcErrorServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"invalid request"}}`))
	}))
}

func TestRefusesNonLocalhost(t *testing.T) {
	_, err := Run(context.Background(), Options{Target: "https://example.com:1/"})
	if err == nil || !strings.Contains(err.Error(), "non-localhost") {
		t.Fatalf("expected a localhost-guard refusal, got %v", err)
	}
}

func TestAllowsLocalhostLoopback(t *testing.T) {
	srv := rpcErrorServer()
	defer srv.Close()
	rep, err := Run(context.Background(), Options{Target: srv.URL, Iterations: 20, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Passed() {
		t.Errorf("well-behaved target should produce no findings, got %+v", rep.Findings)
	}
	if rep.Seed == 0 {
		t.Error("seed should be recorded")
	}
	if rep.SessionID == "" {
		t.Error("session id should be recorded")
	}
}

func TestRecordsCrashAndNonJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("<html><body>boom</body></html>"))
	}))
	defer srv.Close()
	rep, err := Run(context.Background(), Options{Target: srv.URL, Iterations: 5, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Passed() {
		t.Fatal("expected findings from a 500/HTML target")
	}
	if len(rep.Findings) != 5 {
		t.Fatalf("expected 5 findings, got %d", len(rep.Findings))
	}
	for _, f := range rep.Findings {
		if f.Kind != "non-json" && f.Kind != "crash" {
			t.Errorf("unexpected finding kind %q", f.Kind)
		}
		if f.Payload == "" {
			t.Error("finding should carry the payload")
		}
	}
}

func TestRecordsHang(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3 * time.Second)
	}))
	defer srv.Close()
	rep, err := Run(context.Background(), Options{
		Target: srv.URL, Iterations: 2, Timeout: 300 * time.Millisecond, Seed: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Findings) != 2 {
		t.Fatalf("expected 2 hang findings, got %d", len(rep.Findings))
	}
	for _, f := range rep.Findings {
		if f.Kind != "hang" {
			t.Errorf("expected hang, got %q (%s)", f.Kind, f.Detail)
		}
	}
}

func TestGeneratorDeterministic(t *testing.T) {
	a, b := newGenerator(1234), newGenerator(1234)
	for i := 0; i < 50; i++ {
		if string(a.next()) != string(b.next()) {
			t.Fatalf("generator not deterministic at payload %d", i)
		}
	}
	c := newGenerator(9999)
	if string(a.next()) == string(c.next()) {
		t.Error("different seeds should diverge")
	}
}

func TestGeneratorVariety(t *testing.T) {
	g := newGenerator(42)
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		p := g.next()
		if len(p) == 0 {
			seen["empty"] = true
		} else if p[0] == '{' || p[0] == '[' {
			seen["jsonish"] = true
		} else {
			seen["garbage"] = true
		}
	}
	for _, k := range []string{"empty", "jsonish", "garbage"} {
		if !seen[k] {
			t.Errorf("expected %q payloads in 200 draws", k)
		}
	}
}

func TestEmptyTarget(t *testing.T) {
	if _, err := Run(context.Background(), Options{}); err == nil {
		t.Error("expected an error for an empty target")
	}
}
