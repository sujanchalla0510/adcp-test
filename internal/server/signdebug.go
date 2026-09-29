// Signing debugger API (M6).
//
//	POST /api/signdebug/verify  {request: {method, url, headers, body}, key_pem, expected_base?}
//	                            -> the signdebug report as JSON
//
// The pasted key is used in memory for this one verification only: it is
// never persisted, never logged, and never stored in the session store.
package server

import (
	"encoding/json"
	"net/http"

	"github.com/sujanchalla0510/adcp-test/internal/signdebug"
)

func registerSigndebugRoutes(mux *http.ServeMux, s *Server) {
	mux.HandleFunc("POST /api/signdebug/verify", s.signdebugVerifyHandler)
}

// signdebugVerifyRequest is the POST /api/signdebug/verify body.
type signdebugVerifyRequest struct {
	Request      signdebug.Request `json:"request"`
	KeyPEM       string            `json:"key_pem"`
	ExpectedBase string            `json:"expected_base"`
}

func (s *Server) signdebugVerifyHandler(w http.ResponseWriter, r *http.Request) {
	var req signdebugVerifyRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON request body")
		return
	}
	rep := signdebug.Verify(&req.Request, []byte(req.KeyPEM),
		signdebug.Options{ExpectedBase: req.ExpectedBase})
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(rep)
}
