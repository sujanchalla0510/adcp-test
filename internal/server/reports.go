// Evidence report API (M6).
//
//	POST /api/reports/build
//	  {title?, format: "html"|"pdf", conformance?, scenarios?, load?,
//	   lifecycle?, fuzz?, diff_before?, diff_after?}
//	  -> the evidence pack: text/html inline, or application/pdf download.
//
// Each report field takes the raw JSON of a previous run report
// (conformance, scenario, load, lifecycle, fuzz). When diff_before and
// diff_after name two saved snapshots, their diff is appended.
package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/sujanchalla0510/adcp-test/internal/reports"
)

func registerReportRoutes(mux *http.ServeMux, s *Server) {
	mux.HandleFunc("POST /api/reports/build", s.reportBuildHandler)
}

// reportBuildRequest is the POST /api/reports/build body.
type reportBuildRequest struct {
	Title       string          `json:"title"`
	Format      string          `json:"format"` // "html" (default) or "pdf"
	Conformance json.RawMessage `json:"conformance"`
	Scenarios   json.RawMessage `json:"scenarios"`
	Load        json.RawMessage `json:"load"`
	Lifecycle   json.RawMessage `json:"lifecycle"`
	Fuzz        json.RawMessage `json:"fuzz"`
	DiffBefore  string          `json:"diff_before"`
	DiffAfter   string          `json:"diff_after"`
}

func (s *Server) reportBuildHandler(w http.ResponseWriter, r *http.Request) {
	var req reportBuildRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20))
	if err := dec.Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON request body")
		return
	}
	inputs := reports.Inputs{
		Title:       req.Title,
		Conformance: req.Conformance,
		Scenarios:   req.Scenarios,
		Load:        req.Load,
		Lifecycle:   req.Lifecycle,
		Fuzz:        req.Fuzz,
		GeneratedAt: time.Now().UTC(),
	}
	if req.DiffBefore != "" && req.DiffAfter != "" {
		d, err := s.snapshotStore.DiffSnapshots(req.DiffBefore, req.DiffAfter)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "snapshot diff: "+err.Error())
			return
		}
		inputs.Diff = d
	}
	e, err := reports.Build(inputs)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	filename := "adcp-test-evidence-" + time.Now().UTC().Format("20060102-150405")
	switch strings.ToLower(req.Format) {
	case "", "html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.html"`, filename))
		_, _ = w.Write([]byte(reports.BuildHTML(e)))
	case "pdf":
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.pdf"`, filename))
		_, _ = w.Write(reports.BuildPDF(e))
	default:
		writeAPIError(w, http.StatusBadRequest, "format must be html or pdf")
	}
}
