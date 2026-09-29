// Snapshot API (M6).
//
//	GET  /api/snapshots                  -> {snapshots: [{name, kind, saved_at, file, summary}]}
//	POST /api/snapshots/save             {kind, name, report} -> {entry}
//	GET  /api/snapshots/diff?before=&after= -> the snapshot diff
//	POST /api/snapshots/delete           {name} -> {}
//
// Snapshots persist under ./snapshots/ next to the working directory
// the server was started from.
package server

import (
	"encoding/json"
	"net/http"

	"github.com/sujanchalla0510/adcp-test/internal/snapshots"
)

func registerSnapshotRoutes(mux *http.ServeMux, s *Server) {
	mux.HandleFunc("GET /api/snapshots", s.snapshotListHandler)
	mux.HandleFunc("POST /api/snapshots/save", s.snapshotSaveHandler)
	mux.HandleFunc("GET /api/snapshots/diff", s.snapshotDiffHandler)
	mux.HandleFunc("POST /api/snapshots/delete", s.snapshotDeleteHandler)
}

func (s *Server) snapshotListHandler(w http.ResponseWriter, r *http.Request) {
	list, err := s.snapshotStore.List()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if list == nil {
		list = []*snapshots.Entry{}
	}
	writeJSON(w, map[string]any{"snapshots": list})
}

// snapshotSaveRequest is the POST /api/snapshots/save body.
type snapshotSaveRequest struct {
	Kind   string          `json:"kind"`
	Name   string          `json:"name"`
	Report json.RawMessage `json:"report"`
}

func (s *Server) snapshotSaveHandler(w http.ResponseWriter, r *http.Request) {
	var req snapshotSaveRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20))
	if err := dec.Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON request body")
		return
	}
	if req.Kind == "" {
		req.Kind = "report"
	}
	entry, err := s.snapshotStore.Save(req.Kind, req.Name, req.Report)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, map[string]any{"entry": entry})
}

func (s *Server) snapshotDiffHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	before, after := q.Get("before"), q.Get("after")
	if before == "" || after == "" {
		writeAPIError(w, http.StatusBadRequest, "before and after query params are required")
		return
	}
	d, err := s.snapshotStore.DiffSnapshots(before, after)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, d)
}

// snapshotDeleteRequest is the POST /api/snapshots/delete body.
type snapshotDeleteRequest struct {
	Name string `json:"name"`
}

func (s *Server) snapshotDeleteHandler(w http.ResponseWriter, r *http.Request) {
	var req snapshotDeleteRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON request body")
		return
	}
	if err := s.snapshotStore.Delete(req.Name); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, map[string]any{"status": "ok"})
}
