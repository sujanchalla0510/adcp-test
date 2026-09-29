// Load testing API for the adcp-test web server (M5).
//
// Endpoints:
//
//	GET  /api/load/presets        -> {presets: [{id, name, description, yaml}]}
//
//	POST /api/load/run           {config_yaml} | {target_url, tool|scenario, ...}
//	                              -> text/event-stream of progress events,
//	                                 then a final "result" event {id, result}
//
//	GET  /api/load/results/{id}  -> {id, result}
package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/sujanchalla0510/adcp-test/internal/load"
)

// loadRunRequest is the POST /api/load/run body: either a full YAML
// config (config_yaml) or structured fields.
type loadRunRequest struct {
	ConfigYAML     string             `json:"config_yaml"`
	TargetURL      string             `json:"target_url"`
	Tool           string             `json:"tool"`
	Arguments      map[string]any     `json:"arguments"`
	Scenario       string             `json:"scenario"`
	ScenarioName   string             `json:"scenario_name"`
	Concurrency    int                `json:"concurrency"`
	RampUp         string             `json:"ramp_up"`
	Duration       string             `json:"duration"`
	Iterations     int                `json:"iterations"`
	RequestTimeout string             `json:"request_timeout"`
	AllowRemote    bool               `json:"allow_remote"`
	Thresholds     map[string]float64 `json:"thresholds"`
}

func registerLoadRoutes(mux *http.ServeMux, s *Server) {
	mux.HandleFunc("GET /api/load/presets", s.loadPresetsHandler)
	mux.HandleFunc("POST /api/load/run", s.loadRunHandler)
	mux.HandleFunc("GET /api/load/results/{id}", s.loadResultHandler)
}

// loadPresetsHandler lists the built-in load presets.
func (s *Server) loadPresetsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(map[string]any{"presets": load.Presets()})
}

// loadRunHandler runs a load test and streams progress as SSE,
// finishing with a "result" event. The result is stored for later
// retrieval via GET /api/load/results/{id}.
func (s *Server) loadRunHandler(w http.ResponseWriter, r *http.Request) {
	var req loadRunRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON request body")
		return
	}
	cfg, err := buildLoadConfig(req)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	eng, err := load.New(cfg)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	emit := func(event string, v any) {
		writeSSEEvent(w, flusher, event, v)
	}

	progress := make(chan load.Progress, 64)
	resultCh := make(chan *load.Result, 1)
	errCh := make(chan error, 1)
	go func() {
		res, err := eng.Run(r.Context(), progress)
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- res
	}()

	for {
		select {
		case <-r.Context().Done():
			return // client went away; the engine sees the same ctx
		case p, ok := <-progress:
			if !ok {
				progress = nil
				continue
			}
			emit("progress", p)
		case err := <-errCh:
			emit("error", map[string]string{"error": err.Error()})
			return
		case res := <-resultCh:
			id := s.storeLoadResult(res)
			emit("result", map[string]any{"id": id, "result": res})
			return
		}
	}
}

// loadResultHandler returns a stored load result by id.
func (s *Server) loadResultHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.loadMu.Lock()
	res, ok := s.loadResults[id]
	s.loadMu.Unlock()
	if !ok {
		writeAPIError(w, http.StatusNotFound, "unknown load result id")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(map[string]any{"id": id, "result": res})
}

// storeLoadResult stores a finished result and returns its id.
func (s *Server) storeLoadResult(res *load.Result) string {
	id := fmt.Sprintf("load-%d", s.loadSeq.Add(1))
	s.loadMu.Lock()
	s.loadResults[id] = res
	s.loadMu.Unlock()
	return id
}

// buildLoadConfig builds a load.Config from the run request.
func buildLoadConfig(req loadRunRequest) (*load.Config, error) {
	if req.ConfigYAML != "" {
		return load.ParseConfig([]byte(req.ConfigYAML))
	}
	cfg := &load.Config{
		TargetURL:    req.TargetURL,
		Tool:         req.Tool,
		Arguments:    req.Arguments,
		Scenario:     req.Scenario,
		ScenarioName: req.ScenarioName,
		Concurrency:  req.Concurrency,
		Iterations:   req.Iterations,
		AllowRemote:  req.AllowRemote,
	}
	var err error
	if cfg.RampUp, err = parseDur(req.RampUp, "ramp_up"); err != nil {
		return nil, err
	}
	if cfg.Duration, err = parseDur(req.Duration, "duration"); err != nil {
		return nil, err
	}
	if cfg.RequestTimeout, err = parseDur(req.RequestTimeout, "request_timeout"); err != nil {
		return nil, err
	}
	for k, v := range req.Thresholds {
		switch k {
		case "p99_ms_lt":
			cfg.Thresholds.P99MsLt = v
		case "error_rate_lt":
			cfg.Thresholds.ErrorRateLt = v
		case "timeout_rate_lt":
			cfg.Thresholds.TimeoutRateLt = v
		default:
			return nil, fmt.Errorf("load: unknown threshold %q", k)
		}
	}
	if cfg.Concurrency == 0 {
		cfg.Concurrency = 1
	}
	return cfg, nil
}

// parseDur parses an optional duration string ("" stays 0).
func parseDur(s, field string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("load: bad %s %q: %w", field, s, err)
	}
	return d, nil
}

// loadResultStore fields live on Server (see server.go).
