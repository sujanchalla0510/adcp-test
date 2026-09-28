// Inspect + record/replay API for the adcp-test web server.
//
// Sessions accumulate in the server's session.Store (populated by the
// recording proxy). Recordings are started and stopped through the API;
// stopping a recording persists the session as a cassette file under
// ./cassettes/ and registers it for replay. Replay servers run until
// explicitly stopped.
package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sujanchalla0510/adcp-test/internal/cassette"
	"github.com/sujanchalla0510/adcp-test/internal/recorder"
)

// cassetteDir is where the web UI persists cassettes saved via
// POST /api/record/stop, relative to the process working directory.
const cassetteDir = "cassettes"

// storedCassette is a cassette registered for replay.
type storedCassette struct {
	name string
	path string
	c    *cassette.Cassette
}

// runningReplay is a live replay server.
type runningReplay struct {
	id  string
	srv *cassette.Server
	url string
}

func registerInspectRoutes(mux *http.ServeMux, s *Server) {
	mux.HandleFunc("GET /api/sessions", s.sessionsHandler)
	mux.HandleFunc("GET /api/sessions/{id}", s.sessionDetailHandler)
	mux.HandleFunc("POST /api/record/start", s.recordStartHandler)
	mux.HandleFunc("POST /api/record/stop", s.recordStopHandler)
	mux.HandleFunc("GET /api/cassettes", s.cassettesHandler)
	mux.HandleFunc("POST /api/replay/start", s.replayStartHandler)
	mux.HandleFunc("POST /api/replay/stop", s.replayStopHandler)
}

// sessionSummary is the list-view projection of a session.
type sessionSummary struct {
	ID        string    `json:"id"`
	StartedAt time.Time `json:"started_at"`
	TargetURL string    `json:"target_url"`
	Steps     int       `json:"steps"`
}

func (s *Server) sessionsHandler(w http.ResponseWriter, r *http.Request) {
	out := []sessionSummary{}
	for _, sess := range s.sessions.List() {
		out = append(out, sessionSummary{
			ID:        sess.ID,
			StartedAt: sess.StartedAt,
			TargetURL: sess.TargetURL,
			Steps:     sess.Len(),
		})
	}
	writeJSON(w, out)
}

func (s *Server) sessionDetailHandler(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessions.Get(r.PathValue("id"))
	if !ok {
		writeAPIError(w, http.StatusNotFound, "unknown session")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(sess)
}

// recordStartRequest is the POST /api/record/start body.
type recordStartRequest struct {
	UpstreamURL   string `json:"upstream_url"`
	UpstreamURLV2 string `json:"upstreamUrl"` // alias
}

// recordStartHandler creates a pinned recording session and ensures the
// recording proxy is running, returning the session ID and proxy URL.
// Point a buyer client at the proxy URL with header
// "X-Session-ID: <session_id>" to pin its traffic to the session.
func (s *Server) recordStartHandler(w http.ResponseWriter, r *http.Request) {
	var req recordStartRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON request body")
		return
	}
	upstream := req.UpstreamURL
	if upstream == "" {
		upstream = req.UpstreamURLV2
	}
	proxyURL, err := s.ensureRecorder(upstream)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	sess := s.sessions.New(upstream)
	writeJSON(w, map[string]string{
		"session_id": sess.ID,
		"proxy_url":  proxyURL,
	})
}

// recordStopRequest is the POST /api/record/stop body.
type recordStopRequest struct {
	SessionID string `json:"session_id"`
}

// recordStopHandler converts a recorded session into a cassette, saves
// it under ./cassettes/, and registers it for replay.
func (s *Server) recordStopHandler(w http.ResponseWriter, r *http.Request) {
	var req recordStopRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&req); err != nil || strings.TrimSpace(req.SessionID) == "" {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON request body: need {\"session_id\": ...}")
		return
	}
	sess, ok := s.sessions.Get(req.SessionID)
	if !ok {
		writeAPIError(w, http.StatusNotFound, "unknown session")
		return
	}
	c := cassette.FromSession(sess)
	if len(c.Exchanges) == 0 {
		writeAPIError(w, http.StatusBadRequest, "session has no recorded exchanges")
		return
	}
	if err := os.MkdirAll(cassetteDir, 0o755); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "create cassette dir: "+err.Error())
		return
	}
	name := sanitizeFileName(sess.ID) + ".cassette.json"
	path := filepath.Join(cassetteDir, name)
	if err := c.WriteFile(path); err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.mu.Lock()
	s.cassettes[name] = &storedCassette{name: name, path: path, c: c}
	s.mu.Unlock()
	writeJSON(w, map[string]any{
		"cassette":  name,
		"path":      path,
		"exchanges": len(c.Exchanges),
	})
}

// cassetteSummary is the list-view projection of a stored cassette.
type cassetteSummary struct {
	Name       string    `json:"name"`
	Path       string    `json:"path"`
	Target     string    `json:"target"`
	RecordedAt time.Time `json:"recorded_at"`
	Exchanges  int       `json:"exchanges"`
}

func (s *Server) cassettesHandler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []cassetteSummary{}
	for _, entry := range s.cassettes {
		out = append(out, cassetteSummary{
			Name:       entry.name,
			Path:       entry.path,
			Target:     entry.c.Target,
			RecordedAt: entry.c.RecordedAt,
			Exchanges:  len(entry.c.Exchanges),
		})
	}
	writeJSON(w, out)
}

// replayStartRequest is the POST /api/replay/start body.
type replayStartRequest struct {
	Cassette string `json:"cassette"`
	Strict   bool   `json:"strict"`
}

// replayStartHandler serves a stored cassette as a fake MCP endpoint.
func (s *Server) replayStartHandler(w http.ResponseWriter, r *http.Request) {
	var req replayStartRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&req); err != nil || strings.TrimSpace(req.Cassette) == "" {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON request body: need {\"cassette\": ...}")
		return
	}
	s.mu.Lock()
	entry, ok := s.cassettes[req.Cassette]
	s.mu.Unlock()
	if !ok {
		writeAPIError(w, http.StatusNotFound, "unknown cassette")
		return
	}
	srv := &cassette.Server{Cassette: entry.c, Strict: req.Strict}
	url, err := srv.Start("127.0.0.1:0")
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "start replay server: "+err.Error())
		return
	}
	id := fmt.Sprintf("r-%d", atomic.AddInt64(&s.replaySeq, 1))
	s.mu.Lock()
	s.replays[id] = &runningReplay{id: id, srv: srv, url: url}
	s.mu.Unlock()
	writeJSON(w, map[string]string{"replay_id": id, "replay_url": url})
}

// replayStopRequest is the POST /api/replay/stop body.
type replayStopRequest struct {
	ReplayID string `json:"replay_id"`
}

// replayStopHandler shuts a running replay server down.
func (s *Server) replayStopHandler(w http.ResponseWriter, r *http.Request) {
	var req replayStopRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&req); err != nil || strings.TrimSpace(req.ReplayID) == "" {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON request body: need {\"replay_id\": ...}")
		return
	}
	s.mu.Lock()
	entry, ok := s.replays[req.ReplayID]
	if ok {
		delete(s.replays, req.ReplayID)
	}
	s.mu.Unlock()
	if !ok {
		writeAPIError(w, http.StatusNotFound, "unknown replay")
		return
	}
	_ = entry.srv.Close()
	writeJSON(w, map[string]bool{"stopped": true})
}

// ensureRecorder starts the recording proxy for upstream if needed and
// returns its URL. Starting with a different upstream replaces the
// running proxy.
func (s *Server) ensureRecorder(upstream string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(upstream) == "" {
		return "", fmt.Errorf("upstream URL is empty")
	}
	if s.recorder != nil && s.recorder.Upstream.String() == upstream {
		return s.recorder.ProxyURL(), nil
	}
	if s.recorder != nil {
		_ = s.recorder.Close()
		s.recorder = nil
	}
	rec, err := recorder.NewRecorder(upstream, s.sessions)
	if err != nil {
		return "", err
	}
	proxyURL, err := rec.Start("127.0.0.1:0")
	if err != nil {
		return "", err
	}
	s.recorder = rec
	return proxyURL, nil
}

// sanitizeFileName keeps alphanumerics plus a few safe punctuation
// marks; everything else becomes "_".
func sanitizeFileName(name string) string {
	var b strings.Builder
	for _, c := range name {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' {
			b.WriteRune(c)
		} else {
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "session"
	}
	return b.String()
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}
