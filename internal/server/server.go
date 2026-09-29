// Package server implements the adcp-test local web server.
//
// The server binds to localhost only and serves the embedded UI plus a
// small JSON API. Local-first: it collects no telemetry and makes no
// outbound network calls except when the user explicitly runs a
// conformance check against a seller URL they configured.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sujanchalla0510/adcp-test/internal/config"
	"github.com/sujanchalla0510/adcp-test/internal/conformance"
	"github.com/sujanchalla0510/adcp-test/internal/load"
	"github.com/sujanchalla0510/adcp-test/internal/recorder"
	"github.com/sujanchalla0510/adcp-test/internal/session"
	"github.com/sujanchalla0510/adcp-test/internal/snapshots"
	"github.com/sujanchalla0510/adcp-test/internal/webhooks"
	adcpweb "github.com/sujanchalla0510/adcp-test/web"
)

// maxConformanceRun bounds how long one conformance run may take when
// triggered through the API.
const maxConformanceRun = 2 * time.Minute

// Server is the adcp-test local web server.
type Server struct {
	cfg  *config.Config
	http *http.Server
	mux  *http.ServeMux

	// sessions accumulates recorded MCP traffic for the Inspect screen.
	sessions *session.Store

	mu        sync.Mutex
	recorder  *recorder.Recorder
	cassettes map[string]*storedCassette
	replays   map[string]*runningReplay
	replaySeq int64

	// Mock builder (M4) state.
	mockCfg        string
	mockRunning    []*runningMock
	mockRecordings []*recordingProxy
	mockSeq        int64

	// Load results (M5) state.
	loadMu      sync.Mutex
	loadResults map[string]*load.Result
	loadSeq     atomic.Int64

	// Webhook listener (M6) state: at most one listener at a time.
	webhookMu       sync.Mutex
	webhookListener *webhooks.Listener
	webhookURL      string

	// Snapshot store (M6), rooted at ./snapshots.
	snapshotStore *snapshots.Store
}

// New builds a Server from cfg.
func New(cfg *config.Config) *Server {
	sub, err := fs.Sub(adcpweb.FS, ".")
	if err != nil {
		// web/ is embedded at build time; this is unreachable in practice.
		panic(fmt.Sprintf("adcp-test: embedded web assets missing: %v", err))
	}
	s := &Server{
		cfg:           cfg,
		sessions:      session.NewStore(),
		cassettes:     map[string]*storedCassette{},
		replays:       map[string]*runningReplay{},
		mockCfg:       defaultMockConfig,
		loadResults:   map[string]*load.Result{},
		snapshotStore: snapshots.New(""),
	}
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(sub)))
	mux.HandleFunc("/api/health", healthHandler)
	mux.HandleFunc("/api/conformance/run", conformanceRunHandler)
	registerInspectRoutes(mux, s)
	registerMockRoutes(mux, s)
	registerScenarioRoutes(mux, s)
	registerLoadRoutes(mux, s)
	registerSigndebugRoutes(mux, s)
	registerLifecycleRoutes(mux, s)
	registerFuzzRoutes(mux, s)
	registerWebhookRoutes(mux, s)
	registerSnapshotRoutes(mux, s)
	registerReportRoutes(mux, s)
	s.mux = mux
	s.http = &http.Server{Handler: mux}
	return s
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

// conformanceRunRequest is the POST /api/conformance/run body.
type conformanceRunRequest struct {
	TargetURL   string `json:"target_url"`
	TargetURLV2 string `json:"targetUrl"` // alias
	BearerToken string `json:"bearer_token"`
}

// conformanceRunHandler runs the conformance suite against the
// user-configured seller URL and returns the report as JSON.
func conformanceRunHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req conformanceRunRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON request body")
		return
	}
	target := req.TargetURL
	if target == "" {
		target = req.TargetURLV2
	}
	ctx, cancel := context.WithTimeout(r.Context(), maxConformanceRun)
	defer cancel()
	rep, err := conformance.Run(ctx, target, conformance.Options{BearerToken: req.BearerToken})
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(rep)
}

func writeAPIError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(map[string]string{"status": "error", "error": msg})
}
