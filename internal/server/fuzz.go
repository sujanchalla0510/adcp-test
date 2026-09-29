// Protocol fuzzer API (M6).
//
//	POST /api/fuzz/run  {target_url, iterations?, seed?, bearer_token?, allow_remote?}
//	                    -> the fuzz report as JSON
package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/sujanchalla0510/adcp-test/internal/fuzz"
)

// maxFuzzRun bounds a fuzz run triggered through the API; iterations
// are capped so one click cannot run forever.
const (
	maxFuzzRun        = 5 * time.Minute
	maxFuzzIterations = 2000
)

func registerFuzzRoutes(mux *http.ServeMux, s *Server) {
	mux.HandleFunc("POST /api/fuzz/run", s.fuzzRunHandler)
}

// fuzzRunRequest is the POST /api/fuzz/run body.
type fuzzRunRequest struct {
	TargetURL   string `json:"target_url"`
	TargetURLV2 string `json:"targetUrl"` // alias
	Iterations  int    `json:"iterations"`
	Seed        int64  `json:"seed"`
	BearerToken string `json:"bearer_token"`
	AllowRemote bool   `json:"allow_remote"`
}

func (s *Server) fuzzRunHandler(w http.ResponseWriter, r *http.Request) {
	var req fuzzRunRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON request body")
		return
	}
	target := req.TargetURL
	if target == "" {
		target = req.TargetURLV2
	}
	iters := req.Iterations
	if iters > maxFuzzIterations {
		iters = maxFuzzIterations
	}
	ctx, cancel := context.WithTimeout(r.Context(), maxFuzzRun)
	defer cancel()
	rep, err := fuzz.Run(ctx, fuzz.Options{
		Target:      target,
		Iterations:  iters,
		Seed:        req.Seed,
		BearerToken: req.BearerToken,
		AllowRemote: req.AllowRemote,
		Store:       s.sessions, // fuzz traffic lands in the Inspect session store
	})
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(rep)
}
