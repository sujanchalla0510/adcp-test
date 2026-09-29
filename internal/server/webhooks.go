// Webhook listener API (M6).
//
//	POST /api/webhooks/listen      {port?} -> {url}      (restarts any existing listener)
//	GET  /api/webhooks/deliveries                -> {deliveries: [...], url}
//	POST /api/webhooks/clear                     -> {}
//	POST /api/webhooks/stop                      -> {}
//
// The listener binds 127.0.0.1 only. Point the seller's webhook
// configuration at the returned URL during a test, then inspect the
// captured timeline here.
package server

import (
	"encoding/json"
	"net/http"

	"github.com/sujanchalla0510/adcp-test/internal/webhooks"
)

func registerWebhookRoutes(mux *http.ServeMux, s *Server) {
	mux.HandleFunc("POST /api/webhooks/listen", s.webhookListenHandler)
	mux.HandleFunc("GET /api/webhooks/deliveries", s.webhookDeliveriesHandler)
	mux.HandleFunc("POST /api/webhooks/clear", s.webhookClearHandler)
	mux.HandleFunc("POST /api/webhooks/stop", s.webhookStopHandler)
}

func (s *Server) webhookMuLock() func() {
	s.webhookMu.Lock()
	return s.webhookMu.Unlock
}

// webhookListenRequest is the POST /api/webhooks/listen body.
type webhookListenRequest struct {
	Port int `json:"port"` // 0 = pick a free port
}

func (s *Server) webhookListenHandler(w http.ResponseWriter, r *http.Request) {
	var req webhookListenRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON request body")
		return
	}
	unlock := s.webhookMuLock()
	defer unlock()
	if s.webhookListener != nil {
		_ = s.webhookListener.Close()
		s.webhookListener = nil
	}
	l := webhooks.NewListener(0)
	url, err := l.Start(req.Port)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.webhookListener = l
	s.webhookURL = url
	writeJSON(w, map[string]any{"url": url})
}

func (s *Server) webhookDeliveriesHandler(w http.ResponseWriter, r *http.Request) {
	unlock := s.webhookMuLock()
	defer unlock()
	var deliveries []*webhooks.Delivery
	if s.webhookListener != nil {
		deliveries = s.webhookListener.Deliveries()
	}
	if deliveries == nil {
		deliveries = []*webhooks.Delivery{}
	}
	writeJSON(w, map[string]any{"url": s.webhookURL, "deliveries": deliveries})
}

func (s *Server) webhookClearHandler(w http.ResponseWriter, r *http.Request) {
	unlock := s.webhookMuLock()
	defer unlock()
	if s.webhookListener != nil {
		s.webhookListener.Clear()
	}
	writeJSON(w, map[string]any{"status": "ok"})
}

func (s *Server) webhookStopHandler(w http.ResponseWriter, r *http.Request) {
	unlock := s.webhookMuLock()
	defer unlock()
	if s.webhookListener != nil {
		_ = s.webhookListener.Close()
		s.webhookListener = nil
	}
	s.webhookURL = ""
	writeJSON(w, map[string]any{"status": "ok"})
}
