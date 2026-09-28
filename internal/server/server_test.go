package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sujanchalla0510/adcp-test/internal/config"
)

func newTestServer() *Server {
	return New(&config.Config{Port: config.DefaultPort})
}

func TestHealthEndpoint(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), `"status":"ok"`) {
		t.Fatalf("unexpected health body: %s", body)
	}
}

func TestIndexServesHTML(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), "adcp-test") {
		t.Fatalf("index page does not mention adcp-test")
	}
}

func TestAddrIsLocalhost(t *testing.T) {
	srv := newTestServer()
	if got := srv.Addr(); !strings.HasPrefix(got, "127.0.0.1:") {
		t.Fatalf("Addr() = %q, want 127.0.0.1 prefix (localhost only)", got)
	}
}
