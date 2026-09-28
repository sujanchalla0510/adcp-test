// Mock builder API for the adcp-test web server (M4).
//
// The UI edits a YAML mock config, validates it, and starts/stops the
// configured mock services. Record mode proxies an upstream seller and
// generates a config from the captured traffic.
//
// Endpoints:
//
//	GET  /api/mock/config            {yaml, model, running, recordings}
//	PUT  /api/mock/config            {yaml} | {model} -> {ok, yaml, model}
//	POST /api/mock/validate          {yaml} | {model} -> {valid, errors, yaml?, model?}
//	POST /api/mock/start             -> {started: [{id, name, url}]}
//	POST /api/mock/stop              -> {stopped, captured: [{name, path}]}
//	POST /api/mock/record            {upstream_url} -> {record_id, proxy_url}
//	POST /api/mock/record/finish     {record_id} -> {yaml, model, exchanges}
package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/sujanchalla0510/adcp-test/internal/mockcfg"
	"github.com/sujanchalla0510/adcp-test/internal/mockserver"
)

// defaultMockConfig is the starter config the Mock builder screen loads
// with: self-contained, valid, and demonstrating inline responses plus
// the media-buy lifecycle state machine.
const defaultMockConfig = `# adcp-test mock configuration.
# Each service listens on localhost and serves its routes as a fake MCP
# seller endpoint. Response modes: inline JSON, file:, sequence: (nth
# call -> nth response), template: ({{args.path}} placeholders).
# Compose: add more entries under mocks: to run several mocks at once.
mocks:
  - name: seller
    protocol: adcp
    listen: :8080
    routes:
      - match: { tool: get_products }
        respond:
          inline:
            products:
              - product_id: prod_demo_1
                name: Demo display placement
                price: { amount: "2.50", currency: USD }
        latency: { p50: 40ms, p99: 250ms }
      - match: { tool: create_media_buy, args: { buyer_ref: "acme*" } }
        respond:
          inline:
            media_buy: { id: mb_demo_001, status: draft }
        state_machine: media-buy-lifecycle
`

// runningMock is a live mock service started from the builder.
type runningMock struct {
	id   string
	name string
	srv  *mockserver.Server
	url  string
}

// recordingProxy is a live record-mode capture session.
type recordingProxy struct {
	id  string
	srv *mockserver.Server
	url string
}

func registerMockRoutes(mux *http.ServeMux, s *Server) {
	mux.HandleFunc("GET /api/mock/config", s.mockConfigGetHandler)
	mux.HandleFunc("PUT /api/mock/config", s.mockConfigPutHandler)
	mux.HandleFunc("POST /api/mock/validate", s.mockValidateHandler)
	mux.HandleFunc("POST /api/mock/start", s.mockStartHandler)
	mux.HandleFunc("POST /api/mock/stop", s.mockStopHandler)
	mux.HandleFunc("POST /api/mock/record", s.mockRecordStartHandler)
	mux.HandleFunc("POST /api/mock/record/finish", s.mockRecordFinishHandler)
}

type mockServiceStatus struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

// mockConfigGetHandler returns the current config as YAML text plus its
// JSON model, and the status of running mocks / recordings.
func (s *Server) mockConfigGetHandler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	model, _ := configModel(s.mockCfg)
	writeJSON(w, map[string]any{
		"yaml":       s.mockCfg,
		"model":      model,
		"running":    s.mockStatusLocked(),
		"recordings": s.recordStatusLocked(),
	})
}

// mockConfigRequest is the PUT /api/mock/config and POST
// /api/mock/validate body: YAML text or the editor's JSON model.
type mockConfigRequest struct {
	YAML  string          `json:"yaml"`
	Model json.RawMessage `json:"model"`
}

// resolveMockConfig parses and validates a config request, returning the
// canonical YAML text and parsed config.
func resolveMockConfig(req mockConfigRequest) (string, *mockcfg.Config, error) {
	var (
		cfg *mockcfg.Config
		err error
	)
	switch {
	case len(req.Model) > 0:
		cfg = &mockcfg.Config{}
		if err = json.Unmarshal(req.Model, cfg); err != nil {
			return "", nil, fmt.Errorf("invalid model JSON: %w", err)
		}
	case strings.TrimSpace(req.YAML) != "":
		if cfg, err = mockcfg.Parse([]byte(req.YAML)); err != nil {
			return "", nil, err
		}
	default:
		return "", nil, fmt.Errorf("need {\"yaml\": ...} or {\"model\": ...}")
	}
	if err := cfg.Validate(); err != nil {
		return "", nil, err
	}
	if len(req.Model) > 0 {
		// The editor model round-trips through canonical YAML so the
		// preview pane always shows exactly what will run.
		out, err := cfg.Marshal()
		if err != nil {
			return "", nil, err
		}
		return string(out), cfg, nil
	}
	return req.YAML, cfg, nil
}

func configModel(yamlText string) (json.RawMessage, error) {
	cfg, err := mockcfg.Parse([]byte(yamlText))
	if err != nil {
		return nil, err
	}
	return json.Marshal(cfg)
}

// mockConfigPutHandler stores a validated config as the builder's current
// config.
func (s *Server) mockConfigPutHandler(w http.ResponseWriter, r *http.Request) {
	var req mockConfigRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON request body")
		return
	}
	text, _, err := resolveMockConfig(req)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.mu.Lock()
	s.mockCfg = text
	s.mu.Unlock()
	model, _ := configModel(text)
	writeJSON(w, map[string]any{"ok": true, "yaml": text, "model": model})
}

// mockValidateRequest mirrors mockConfigRequest; validate returns the
// canonical YAML/model alongside the verdict so the editor preview can
// render exactly what was checked.
func (s *Server) mockValidateHandler(w http.ResponseWriter, r *http.Request) {
	var req mockConfigRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON request body")
		return
	}
	text, cfg, err := resolveMockConfig(req)
	resp := map[string]any{"valid": err == nil}
	if err != nil {
		resp["errors"] = splitErrors(err)
	} else {
		resp["yaml"] = text
		if model, merr := json.Marshal(cfg); merr == nil {
			resp["model"] = json.RawMessage(model)
		}
	}
	writeJSON(w, resp)
}

func splitErrors(err error) []string {
	var out []string
	for _, line := range strings.Split(err.Error(), "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

// mockStartHandler stops any running mocks and starts the services in the
// current config.
func (s *Server) mockStartHandler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopMocksLocked()
	cfg, err := mockcfg.Parse([]byte(s.mockCfg))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "stored config no longer parses: "+err.Error())
		return
	}
	if err := cfg.Validate(); err != nil {
		writeAPIError(w, http.StatusBadRequest, "stored config invalid: "+err.Error())
		return
	}
	var started []mockServiceStatus
	for _, svc := range cfg.Services() {
		srv, err := mockserver.New(svc, mockserver.Options{BaseDir: cfg.BaseDir()})
		if err != nil {
			s.stopMocksLocked()
			writeAPIError(w, http.StatusBadRequest, fmt.Sprintf("mock %q: %v", svc.Name, err))
			return
		}
		url, err := srv.Start()
		if err != nil {
			_ = srv.Close()
			s.stopMocksLocked()
			writeAPIError(w, http.StatusBadRequest, fmt.Sprintf("mock %q: %v", svc.Name, err))
			return
		}
		id := fmt.Sprintf("m-%d", atomic.AddInt64(&s.mockSeq, 1))
		s.mockRunning = append(s.mockRunning, &runningMock{id: id, name: svc.Name, srv: srv, url: url})
		started = append(started, mockServiceStatus{ID: id, Name: svc.Name, URL: url})
	}
	writeJSON(w, map[string]any{"started": started})
}

// mockStopHandler stops all running mocks. Record-mode mocks write their
// captured config to capture_to on stop.
func (s *Server) mockStopHandler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var captured []map[string]string
	for _, m := range s.mockRunning {
		if m.srv.IsRecordMode() {
			if path, err := m.srv.WriteCapturedConfig(); err == nil {
				captured = append(captured, map[string]string{"name": m.name, "path": path})
			}
		}
		_ = m.srv.Close()
	}
	s.mockRunning = nil
	writeJSON(w, map[string]any{"stopped": true, "captured": captured})
}

func (s *Server) stopMocksLocked() {
	for _, m := range s.mockRunning {
		_ = m.srv.Close()
	}
	s.mockRunning = nil
}

func (s *Server) mockStatusLocked() []mockServiceStatus {
	out := []mockServiceStatus{}
	for _, m := range s.mockRunning {
		out = append(out, mockServiceStatus{ID: m.id, Name: m.name, URL: m.url})
	}
	return out
}

func (s *Server) recordStatusLocked() []map[string]any {
	out := []map[string]any{}
	for _, rec := range s.mockRecordings {
		out = append(out, map[string]any{
			"id": rec.id, "proxy_url": rec.url, "exchanges": rec.srv.ExchangeCount(),
		})
	}
	return out
}

// mockRecordStartRequest is the POST /api/mock/record body.
type mockRecordStartRequest struct {
	UpstreamURL string `json:"upstream_url"`
}

// mockRecordStartHandler starts a record-mode proxy against the
// user-configured upstream. Point a buyer client at the returned
// proxy_url, generate traffic, then POST /api/mock/record/finish to get
// the generated config.
func (s *Server) mockRecordStartHandler(w http.ResponseWriter, r *http.Request) {
	var req mockRecordStartRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&req); err != nil || strings.TrimSpace(req.UpstreamURL) == "" {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON request body: need {\"upstream_url\": ...}")
		return
	}
	svc := mockcfg.MockService{
		Name:   "record",
		Listen: ":0",
		Record: &mockcfg.Record{Upstream: strings.TrimSpace(req.UpstreamURL)},
	}
	srv, err := mockserver.New(svc, mockserver.Options{})
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	url, err := srv.Start()
	if err != nil {
		_ = srv.Close()
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	id := fmt.Sprintf("rec-%d", atomic.AddInt64(&s.mockSeq, 1))
	s.mu.Lock()
	s.mockRecordings = append(s.mockRecordings, &recordingProxy{id: id, srv: srv, url: url})
	s.mu.Unlock()
	writeJSON(w, map[string]string{"record_id": id, "proxy_url": url})
}

// mockRecordFinishRequest is the POST /api/mock/record/finish body.
type mockRecordFinishRequest struct {
	RecordID string `json:"record_id"`
}

// mockRecordFinishHandler stops a recording proxy and returns the mock
// config generated from the captured traffic.
func (s *Server) mockRecordFinishHandler(w http.ResponseWriter, r *http.Request) {
	var req mockRecordFinishRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&req); err != nil || strings.TrimSpace(req.RecordID) == "" {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON request body: need {\"record_id\": ...}")
		return
	}
	s.mu.Lock()
	var rec *recordingProxy
	keep := s.mockRecordings[:0]
	for _, rp := range s.mockRecordings {
		if rp.id == req.RecordID {
			rec = rp
		} else {
			keep = append(keep, rp)
		}
	}
	s.mockRecordings = keep
	s.mu.Unlock()
	if rec == nil {
		writeAPIError(w, http.StatusNotFound, "unknown record_id")
		return
	}
	defer rec.srv.Close()
	cfg := rec.srv.RecordedConfig()
	if cfg == nil || len(cfg.Services()) == 0 || len(cfg.Services()[0].Routes) == 0 {
		writeAPIError(w, http.StatusBadRequest, "no exchanges captured; generate some traffic through the proxy first")
		return
	}
	data, err := cfg.Marshal()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	model, _ := json.Marshal(cfg)
	writeJSON(w, map[string]any{
		"yaml":      string(data),
		"model":     json.RawMessage(model),
		"exchanges": rec.srv.ExchangeCount(),
	})
}
