// Lifecycle check API (M6).
//
//	POST /api/lifecycle/run  {target_url, bearer_token?} -> the lifecycle report as JSON
package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/sujanchalla0510/adcp-test/internal/lifecycle"
)

// maxLifecycleRun bounds a lifecycle run triggered through the API.
const maxLifecycleRun = 5 * time.Minute

func registerLifecycleRoutes(mux *http.ServeMux, s *Server) {
	mux.HandleFunc("POST /api/lifecycle/run", s.lifecycleRunHandler)
}

// lifecycleRunRequest is the POST /api/lifecycle/run body.
type lifecycleRunRequest struct {
	TargetURL   string `json:"target_url"`
	TargetURLV2 string `json:"targetUrl"` // alias
	BearerToken string `json:"bearer_token"`
}

func (s *Server) lifecycleRunHandler(w http.ResponseWriter, r *http.Request) {
	var req lifecycleRunRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON request body")
		return
	}
	target := req.TargetURL
	if target == "" {
		target = req.TargetURLV2
	}
	ctx, cancel := context.WithTimeout(r.Context(), maxLifecycleRun)
	defer cancel()
	rep, err := lifecycle.Run(ctx, target, lifecycle.Options{BearerToken: req.BearerToken})
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(rep)
}
