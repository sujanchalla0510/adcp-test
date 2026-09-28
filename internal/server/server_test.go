package server

import (
	"encoding/json"
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

// conformanceFakeSeller is a minimal synthetic seller for endpoint tests.
func conformanceFakeSeller() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		if req.Method == "tools/list" {
			resp["result"] = map[string]any{"tools": []any{}}
		} else {
			resp["error"] = map[string]any{"code": -32601, "message": "Method not found"}
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func postConformance(t *testing.T, srv *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/conformance/run", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestConformanceRunEndpoint(t *testing.T) {
	seller := conformanceFakeSeller()
	defer seller.Close()
	srv := newTestServer()

	rec := postConformance(t, srv, `{"target_url":`+quoteJSON(seller.URL)+`}`)
	res := rec.Result()
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	var rep struct {
		TargetURL string `json:"target_url"`
		Summary   struct {
			Total  int `json:"total"`
			Failed int `json:"failed"`
		} `json:"summary"`
		Checks []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"checks"`
	}
	if err := json.NewDecoder(res.Body).Decode(&rep); err != nil {
		t.Fatalf("response is not a report: %v", err)
	}
	if rep.TargetURL != seller.URL {
		t.Fatalf("target_url = %q, want %q", rep.TargetURL, seller.URL)
	}
	if rep.Summary.Total == 0 || len(rep.Checks) == 0 {
		t.Fatal("report has no checks")
	}
	if rep.Summary.Failed == 0 {
		t.Fatal("expected failures against a tool-less seller")
	}
}

func TestConformanceRunMethodNotAllowed(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/api/conformance/run", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Result().StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Result().StatusCode)
	}
}

func TestConformanceRunBadRequests(t *testing.T) {
	srv := newTestServer()
	for _, tc := range []struct {
		name string
		body string
	}{
		{"empty body", ``},
		{"not json", `hello`},
		{"missing target", `{}`},
		{"bad scheme", `{"target_url":"ftp://example.com/x"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := postConformance(t, srv, tc.body)
			if rec.Result().StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Result().StatusCode)
			}
			var out map[string]any
			body, _ := io.ReadAll(rec.Result().Body)
			if err := json.Unmarshal(body, &out); err != nil || out["status"] != "error" {
				t.Fatalf("expected JSON error body, got: %s", body)
			}
		})
	}
}

func TestConformanceRunTargetURLAlias(t *testing.T) {
	seller := conformanceFakeSeller()
	defer seller.Close()
	srv := newTestServer()
	rec := postConformance(t, srv, `{"targetUrl":`+quoteJSON(seller.URL)+`}`)
	if rec.Result().StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 for targetUrl alias", rec.Result().StatusCode)
	}
}

func quoteJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
