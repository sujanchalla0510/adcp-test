// Scenario pack API for the adcp-test web server (M5).
//
// Endpoints:
//
//	GET  /api/scenarios        -> {packs: [{id, name, description, scenario_count, scenarios}]}
//
//	POST /api/scenarios/run   {pack, target_url, bearer_token?}
//	                          -> text/event-stream of progress events,
//	                             then a final "report" event with the run report
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/sujanchalla0510/adcp-test/internal/scenarios"
)

// maxScenarioRun bounds a scenario pack run triggered through the API.
const maxScenarioRun = 10 * time.Minute

func registerScenarioRoutes(mux *http.ServeMux, s *Server) {
	mux.HandleFunc("GET /api/scenarios", s.scenariosListHandler)
	mux.HandleFunc("POST /api/scenarios/run", s.scenarioRunHandler)
}

// scenariosListHandler lists the built-in scenario packs.
func (s *Server) scenariosListHandler(w http.ResponseWriter, r *http.Request) {
	metas, err := scenarios.BuiltinPacks()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(map[string]any{"packs": metas})
}

// scenarioRunRequest is the POST /api/scenarios/run body.
type scenarioRunRequest struct {
	Pack        string `json:"pack"`
	TargetURL   string `json:"target_url"`
	TargetURLV2 string `json:"targetUrl"` // alias
	BearerToken string `json:"bearer_token"`
	Chaos       bool   `json:"chaos"`
	ChaosSeed   int64  `json:"chaos_seed"`
	AllowRemote bool   `json:"allow_remote"`
}

// scenarioRunHandler runs a pack against the target and streams progress
// as server-sent events, finishing with a "report" event.
func (s *Server) scenarioRunHandler(w http.ResponseWriter, r *http.Request) {
	var req scenarioRunRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON request body")
		return
	}
	target := req.TargetURL
	if target == "" {
		target = req.TargetURLV2
	}
	pack, err := scenarios.ResolvePack(req.Pack)
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

	ctx, cancel := context.WithTimeout(r.Context(), maxScenarioRun)
	defer cancel()
	var chaos *scenarios.ChaosOptions
	if req.Chaos {
		chaos = &scenarios.ChaosOptions{Enabled: true, Seed: req.ChaosSeed}
	}
	rep, err := scenarios.Run(ctx, pack, target, scenarios.Options{
		BearerToken: req.BearerToken,
		Store:       s.sessions, // scenario traffic lands in the Inspect session store
		Chaos:       chaos,
		AllowRemote: req.AllowRemote,
		OnEvent: func(e scenarios.Event) {
			emit(e.Type, e)
		},
	})
	if err != nil {
		emit("error", map[string]string{"error": err.Error()})
		return
	}
	emit("report", rep)
}

// writeSSEEvent writes one server-sent event frame and flushes.
func writeSSEEvent(w http.ResponseWriter, flusher http.Flusher, event string, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		data = []byte(fmt.Sprintf(`{"error":%q}`, "encode event"))
	}
	fmt.Fprintf(w, "event: %s\ndata: ", event)
	w.Write(data)
	fmt.Fprintf(w, "\n\n")
	flusher.Flush()
}
