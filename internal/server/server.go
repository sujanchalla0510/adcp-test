// Package server implements the adcp-test local web server.
//
// The server binds to localhost only and serves the embedded UI plus a
// small JSON API. Local-first: it never makes outbound network calls and
// collects no telemetry.
package server

import (
	"fmt"
	"io/fs"
	"net"
	"net/http"

	"github.com/sujanchalla0510/adcp-test/internal/config"
	adcpweb "github.com/sujanchalla0510/adcp-test/web"
)

// Server is the adcp-test local web server.
type Server struct {
	cfg  *config.Config
	http *http.Server
	mux  *http.ServeMux
}

// New builds a Server from cfg.
func New(cfg *config.Config) *Server {
	sub, err := fs.Sub(adcpweb.FS, ".")
	if err != nil {
		// web/ is embedded at build time; this is unreachable in practice.
		panic(fmt.Sprintf("adcp-test: embedded web assets missing: %v", err))
	}
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(sub)))
	mux.HandleFunc("/api/health", healthHandler)
	return &Server{cfg: cfg, http: &http.Server{Handler: mux}, mux: mux}
}

// Addr returns the localhost address the server binds to.
func (s *Server) Addr() string {
	return fmt.Sprintf("127.0.0.1:%d", s.cfg.Port)
}

// ListenAndServe binds to the localhost address and serves until the
// process is stopped.
func (s *Server) ListenAndServe() error {
	ln, err := net.Listen("tcp", s.Addr())
	if err != nil {
		return fmt.Errorf("adcp-test: listen %s: %w", s.Addr(), err)
	}
	return s.http.Serve(ln)
}

// Handler exposes the mux so tests can exercise routes without binding.
func (s *Server) Handler() http.Handler {
	return s.mux
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok","service":"adcp-test"}`))
}
