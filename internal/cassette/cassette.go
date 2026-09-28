// Package cassette implements the adcp-test cassette format: a recorded
// session serialized to a JSON file, plus a replay server that serves a
// cassette back as a fake MCP endpoint.
//
// Cassette format (JSON, versioned):
//
//	{
//	  "version": 1,
//	  "target": "https://seller.example/mcp",
//	  "recorded_at": "2026-09-28T12:00:00Z",
//	  "exchanges": [
//	    {
//	      "method": "tools/call",
//	      "tool": "get_products",
//	      "recorded_at": "2026-09-28T12:00:01Z",
//	      "arguments": {"brief": "running shoes"},
//	      "response": {"jsonrpc":"2.0","id":7,"result":{...}},
//	      "duration_ms": 42.5
//	    }
//	  ]
//	}
//
// Notes for later milestones (and hand-editing):
//   - "arguments" holds only the tools/call arguments object, not the
//     full JSON-RPC envelope; request IDs are deliberately not stored
//     because they vary per run.
//   - "response" is the raw upstream response body as recorded. Payloads
//     are redacted at capture time, so a cassette never contains raw
//     tokens or signatures; strict replay against an endpoint that needs
//     auth secrets in arguments therefore requires hand-editing the
//     redacted values back in, or lenient mode (the default).
//   - "error" (transport failure, no response received) replays as a
//     structured JSON-RPC error.
//
// Replay matching: an incoming call matches an exchange by method, plus
// tool name for tools/call. In strict mode the recorded arguments must
// deep-equal the incoming arguments. In lenient mode (default) the
// recorded arguments only need to be a subset of the incoming ones, and
// when no argument match is found the first exchange for the same tool
// is used. Unmatched calls get a structured JSON-RPC error
// (code -32000); strict mode additionally refuses argument mismatches
// instead of falling back.
package cassette

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/sujanchalla0510/adcp-test/internal/session"
)

// CassetteVersion is the current cassette format version.
const CassetteVersion = 1

// maxBody bounds request bodies the replay server buffers.
const maxBody = 1 << 20

// Exchange is one recorded tool call.
type Exchange struct {
	Method     string          `json:"method"`         // "tools/list" | "tools/call"
	Tool       string          `json:"tool,omitempty"` // tool name for tools/call
	RecordedAt time.Time       `json:"recorded_at,omitempty"`
	Arguments  json.RawMessage `json:"arguments,omitempty"` // tools/call arguments object
	Response   json.RawMessage `json:"response"`            // raw recorded response body
	Error      string          `json:"error,omitempty"`     // transport failure (no response)
	DurationMs float64         `json:"duration_ms,omitempty"`
}

// Cassette is a recorded session ready for replay or hand-editing.
type Cassette struct {
	Version    int        `json:"version"`
	Target     string     `json:"target,omitempty"`
	RecordedAt time.Time  `json:"recorded_at"`
	Exchanges  []Exchange `json:"exchanges"`
}

// FromSession converts a recorded session into a cassette, pairing each
// out step with its in step by request ID and preserving call order.
// Steps without an out half are skipped; an out step with no matching in
// step becomes an exchange with an empty response.
func FromSession(s *session.Session) *Cassette {
	c := &Cassette{
		Version:    CassetteVersion,
		Target:     s.TargetURL,
		RecordedAt: time.Now().UTC(),
	}
	steps := s.Snapshot()
	type pair struct {
		out *session.Step
		in  *session.Step
	}
	order := []string{}
	byID := map[string]*pair{}
	for _, st := range steps {
		p, ok := byID[st.RequestID]
		if !ok {
			p = &pair{}
			byID[st.RequestID] = p
			order = append(order, st.RequestID)
		}
		if st.Direction == session.DirectionOut {
			p.out = st
		} else {
			p.in = st
		}
	}
	for _, id := range order {
		p := byID[id]
		if p.out == nil {
			continue
		}
		ex := Exchange{
			Method:     p.out.Method,
			Tool:       p.out.Tool,
			RecordedAt: p.out.TS,
		}
		if p.out.Method == "tools/call" {
			ex.Arguments = extractArguments(p.out.Request)
		}
		if p.in != nil {
			ex.Response = p.in.Response
			ex.Error = p.in.Error
			ex.DurationMs = p.in.DurationMs
		}
		c.Exchanges = append(c.Exchanges, ex)
	}
	return c
}

// FromSessions merges several sessions into one cassette, ordering
// exchanges by recording time.
func FromSessions(sessions []*session.Session) *Cassette {
	merged := &Cassette{Version: CassetteVersion, RecordedAt: time.Now().UTC()}
	for _, s := range sessions {
		one := FromSession(s)
		if merged.Target == "" {
			merged.Target = one.Target
		}
		merged.Exchanges = append(merged.Exchanges, one.Exchanges...)
	}
	sort.SliceStable(merged.Exchanges, func(i, j int) bool {
		return merged.Exchanges[i].RecordedAt.Before(merged.Exchanges[j].RecordedAt)
	})
	return merged
}

// extractArguments pulls params.arguments out of a recorded JSON-RPC
// request body.
func extractArguments(requestBody json.RawMessage) json.RawMessage {
	var env struct {
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(requestBody, &env); err != nil {
		return nil
	}
	var p struct {
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(env.Params, &p); err != nil {
		return nil
	}
	return p.Arguments
}

// WriteFile serializes the cassette as indented JSON to path.
func (c *Cassette) WriteFile(path string) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("cassette: encode: %w", err)
	}
	b = append(b, '\n')
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return fmt.Errorf("cassette: write %s: %w", path, err)
	}
	return nil
}

// LoadFile reads a cassette from path, rejecting unknown versions.
func LoadFile(path string) (*Cassette, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cassette: read %s: %w", path, err)
	}
	var c Cassette
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("cassette: decode %s: %w", path, err)
	}
	if c.Version != CassetteVersion {
		return nil, fmt.Errorf("cassette: version %d unsupported (want %d)", c.Version, CassetteVersion)
	}
	return &c, nil
}

// ---------------------------------------------------------------------------
// Replay server
// ---------------------------------------------------------------------------

// Server serves a Cassette as a fake MCP endpoint over HTTP POST.
type Server struct {
	// Cassette is the recorded traffic to serve.
	Cassette *Cassette
	// Strict requires exact argument equality for a match; otherwise the
	// recorded arguments only need to be a subset of the incoming call's,
	// with fallback to the first exchange for the same tool.
	Strict bool

	mu  sync.Mutex
	srv *http.Server
	ln  net.Listener
}

// Start listens on addr (use "127.0.0.1:0" for an ephemeral port) and
// serves the replay endpoint in the background. It returns the base URL.
func (s *Server) Start(addr string) (string, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", fmt.Errorf("cassette replay: listen %s: %w", addr, err)
	}
	s.mu.Lock()
	s.ln = ln
	s.srv = &http.Server{Handler: s}
	s.mu.Unlock()
	go s.srv.Serve(ln) //nolint:errcheck // Close reports shutdown
	return "http://" + ln.Addr().String(), nil
}

// Close shuts the replay listener down.
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.srv == nil {
		return nil
	}
	return s.srv.Close()
}

// ServeHTTP answers one JSON-RPC call from the cassette.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "replay serves MCP POST requests only", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	r.Body.Close()
	if err != nil {
		http.Error(w, "replay: read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	method, tool, idRaw, args := parseRequest(body)
	ex := s.match(method, tool, args)

	w.Header().Set("Content-Type", "application/json")
	if ex == nil {
		writeEnvelope(w, idRaw, nil, rpcError(-32000,
			fmt.Sprintf("adcp-test replay: no recorded exchange matches %s %s", method, toolLabel(tool))))
		return
	}
	if ex.Error != "" {
		writeEnvelope(w, idRaw, nil, rpcError(-32000,
			"adcp-test replay: recorded exchange failed upstream: "+ex.Error))
		return
	}
	_, _ = w.Write(rewriteID(ex.Response, idRaw))
}

func toolLabel(tool string) string {
	if tool == "" {
		return ""
	}
	return "(" + tool + ")"
}

// match finds the exchange for an incoming call, or nil.
func (s *Server) match(method, tool string, args json.RawMessage) *Exchange {
	var cands []*Exchange
	for i := range s.Cassette.Exchanges {
		ex := &s.Cassette.Exchanges[i]
		if ex.Method != method {
			continue
		}
		if method == "tools/call" && ex.Tool != tool {
			continue
		}
		cands = append(cands, ex)
	}
	if len(cands) == 0 {
		return nil
	}
	if s.Strict {
		for _, ex := range cands {
			if method != "tools/call" || jsonDeepEqual(ex.Arguments, args) {
				return ex
			}
		}
		return nil
	}
	for _, ex := range cands {
		if method != "tools/call" || jsonSubset(ex.Arguments, args) {
			return ex
		}
	}
	// Lenient fallback: same tool, any arguments.
	return cands[0]
}

// parseRequest extracts method, tool, raw id, and arguments from a
// JSON-RPC request body.
func parseRequest(body []byte) (method, tool string, idRaw, args json.RawMessage) {
	var env struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(body, &env); err != nil || env.Method == "" {
		return "unknown", "", nil, nil
	}
	if env.Method == "tools/call" {
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(env.Params, &p); err == nil {
			return env.Method, p.Name, env.ID, p.Arguments
		}
	}
	return env.Method, "", env.ID, nil
}

// rewriteID returns the recorded response with its "id" replaced by the
// incoming request's id. Non-object payloads pass through unchanged.
func rewriteID(response, idRaw json.RawMessage) json.RawMessage {
	var obj map[string]any
	if err := json.Unmarshal(response, &obj); err != nil {
		return response
	}
	var id any
	if len(idRaw) > 0 && json.Unmarshal(idRaw, &id) == nil {
		obj["id"] = id
	} else {
		delete(obj, "id")
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return response
	}
	return out
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func rpcError(code int, msg string) *rpcErr { return &rpcErr{Code: code, Message: msg} }

// writeEnvelope writes a JSON-RPC 2.0 response envelope.
func writeEnvelope(w http.ResponseWriter, idRaw json.RawMessage, result any, rerr *rpcErr) {
	var id any
	if len(idRaw) > 0 {
		_ = json.Unmarshal(idRaw, &id)
	}
	env := map[string]any{"jsonrpc": "2.0", "id": id}
	if rerr != nil {
		env["error"] = rerr
	} else {
		env["result"] = result
	}
	_ = json.NewEncoder(w).Encode(env)
}

// jsonDeepEqual reports whether two JSON documents are deeply equal.
// Empty documents unmarshal to nil and compare equal to each other.
func jsonDeepEqual(a, b json.RawMessage) bool {
	return reflect.DeepEqual(normalizeJSON(a), normalizeJSON(b))
}

// jsonSubset reports whether recorded is a subset of incoming: every
// value present in recorded must be present and equal (recursively) in
// incoming. Objects compare by subset; arrays and scalars by deep
// equality. An empty recorded document matches anything.
func jsonSubset(recorded, incoming json.RawMessage) bool {
	if len(bytes.TrimSpace(recorded)) == 0 {
		return true
	}
	return subsetValue(normalizeJSON(recorded), normalizeJSON(incoming))
}

func normalizeJSON(raw json.RawMessage) any {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	return v
}

func subsetValue(rec, inc any) bool {
	recMap, recIsMap := rec.(map[string]any)
	if recIsMap {
		incMap, ok := inc.(map[string]any)
		if !ok {
			return false
		}
		for k, rv := range recMap {
			iv, ok := incMap[k]
			if !ok || !subsetValue(rv, iv) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(rec, inc)
}
