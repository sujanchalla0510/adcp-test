// Package mockserver is the M4 mock engine: an HTTP MCP endpoint that
// implements a mockcfg.MockService — request matching, response modes
// (inline/file/sequence/template), latency profiles, fault injection, the
// media-buy lifecycle state machine, record-mode proxying, and
// multi-service compose (one Server per service, several per process).
//
// The server speaks JSON-RPC 2.0 over HTTP POST: tools/list reports the
// configured route tools, tools/call dispatches to the first matching
// route. Route responses are wrapped as the JSON-RPC "result" payload.
package mockserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sujanchalla0510/adcp-test/internal/mockcfg"
)

// maxRequestBody caps a single JSON-RPC request body.
const maxRequestBody = 4 << 20

// defaultTimeoutHang is how long a timeout fault hangs before answering
// normally (long enough for any sane client timeout to fire first).
const defaultTimeoutHang = 60 * time.Second

// Options tweaks a Server. Zero value is fine for production use; tests
// use WithSeed / short TimeoutHang for determinism.
type Options struct {
	// RandSeed seeds latency/fault randomness; 0 = time-based.
	RandSeed int64
	// TimeoutHang overrides defaultTimeoutHang.
	TimeoutHang time.Duration
	// BaseDir resolves relative response-file paths; default ".".
	BaseDir string
}

// Server is one mock MCP endpoint.
type Server struct {
	svc         mockcfg.MockService
	baseDir     string
	rng         *rand.Rand
	timeoutHang time.Duration
	routes      []*routeRuntime
	lifecycles  map[string]*Lifecycle // state machines shared across routes
	recorder    *Recorder             // non-nil in record (proxy) mode
	startedAt   time.Time

	http *http.Server
	ln   net.Listener
	url  string
}

type routeRuntime struct {
	cfg     mockcfg.Route
	calls   atomic.Uint64
	latency *latencyModel
	smName  string // state machine name; "" = none
}

// New builds a Server for svc. The config is validated and every
// file-backed response is checked for existence up front (fail fast);
// file contents are read per request so fixtures stay live-editable.
func New(svc mockcfg.MockService, opts Options) (*Server, error) {
	if err := (&mockcfg.Config{Mocks: []mockcfg.MockService{svc}}).Validate(); err != nil {
		return nil, err
	}
	baseDir := opts.BaseDir
	if baseDir == "" {
		baseDir = "."
	}
	hang := opts.TimeoutHang
	if hang <= 0 {
		hang = defaultTimeoutHang
	}
	seed := opts.RandSeed
	if seed == 0 {
		seed = time.Now().UnixNano()
	}
	s := &Server{
		svc:         svc,
		baseDir:     baseDir,
		rng:         rand.New(rand.NewSource(seed)),
		timeoutHang: hang,
		startedAt:   time.Now(),
	}
	if svc.Record != nil {
		rec, err := NewRecorder(svc.Record.Upstream)
		if err != nil {
			return nil, err
		}
		s.recorder = rec
		return s, nil
	}
	for i, rt := range svc.Routes {
		rr := &routeRuntime{cfg: rt}
		if rt.Latency != nil {
			lm, err := buildLatencyModel(rt.Latency)
			if err != nil {
				return nil, fmt.Errorf("routes[%d]: %w", i, err)
			}
			rr.latency = lm
		}
		if rt.StateMachine != "" {
			rr.smName = rt.StateMachine
			if s.lifecycles == nil {
				s.lifecycles = map[string]*Lifecycle{}
			}
			if _, ok := s.lifecycles[rt.StateMachine]; !ok {
				s.lifecycles[rt.StateMachine] = NewLifecycle()
			}
		}
		// Fail fast on missing response files; contents load per request.
		for _, p := range responseFiles(rt) {
			if _, err := os.Stat(s.resolve(p)); err != nil {
				return nil, fmt.Errorf("routes[%d]: response file %q: %w", i, p, err)
			}
		}
		s.routes = append(s.routes, rr)
	}
	return s, nil
}

// responseFiles lists every file path a route's respond mode references.
func responseFiles(rt mockcfg.Route) []string {
	var out []string
	r := rt.Respond
	if r.File != "" {
		out = append(out, r.File)
	}
	if r.Template != "" {
		out = append(out, r.Template)
	}
	for _, it := range r.Sequence {
		if it.File != "" {
			out = append(out, it.File)
		}
	}
	return out
}

func (s *Server) resolve(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(s.baseDir, p)
}

// normalizeListen forces mock endpoints onto localhost: ":8080" ->
// "127.0.0.1:8080". Mocks are local test doubles, never public services.
func normalizeListen(listen string) (string, error) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("bad listen %q: %w", listen, err)
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port), nil
}

// Start binds the mock endpoint (always localhost) and serves until Close.
func (s *Server) Start() (string, error) {
	addr, err := normalizeListen(s.svc.Listen)
	if err != nil {
		return "", err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", fmt.Errorf("mockserver: listen %s: %w", addr, err)
	}
	s.ln = ln
	s.url = "http://" + ln.Addr().String()
	s.http = &http.Server{Handler: s}
	go func() { _ = s.http.Serve(ln) }()
	return s.url, nil
}

// Close shuts the endpoint down.
func (s *Server) Close() error {
	if s.http == nil {
		return nil
	}
	return s.http.Close()
}

// URL is the endpoint URL after Start.
func (s *Server) URL() string { return s.url }

// Name is the configured service name.
func (s *Server) Name() string { return s.svc.Name }

// IsRecordMode reports whether this server proxies an upstream seller.
func (s *Server) IsRecordMode() bool { return s.recorder != nil }

// WriteCapturedConfig generates a mock config from the traffic this
// record-mode server proxied and writes it to the configured capture_to
// path. It is an error outside record mode.
func (s *Server) WriteCapturedConfig() (string, error) {
	if s.recorder == nil {
		return "", fmt.Errorf("mockserver: not in record mode")
	}
	cfg := s.RecordedConfig()
	data, err := cfg.Marshal()
	if err != nil {
		return "", err
	}
	path := s.resolve(s.svc.Record.CaptureTo)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("mockserver: write %s: %w", path, err)
	}
	return path, nil
}

// RecordedConfig generates a mock config from captured record-mode
// traffic without writing any file. It returns nil outside record mode.
func (s *Server) RecordedConfig() *mockcfg.Config {
	if s.recorder == nil {
		return nil
	}
	return GenerateConfig(s.recorder.Exchanges(), s.svc.Name+"-recorded", s.svc.Listen)
}

// CaptureTo is the record-mode capture_to path, or "" when unset or not
// in record mode.
func (s *Server) CaptureTo() string {
	if s.recorder == nil || s.svc.Record == nil {
		return ""
	}
	return s.resolve(s.svc.Record.CaptureTo)
}

// ExchangeCount reports how many exchanges a record-mode server captured.
func (s *Server) ExchangeCount() int {
	if s.recorder == nil {
		return 0
	}
	return len(s.recorder.Exchanges())
}

// ---------------------------------------------------------------------------
// JSON-RPC plumbing
// ---------------------------------------------------------------------------

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  rpcParams       `json:"params"`
}

type rpcParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.recorder != nil {
		s.recorder.ServeHTTP(w, r)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
	if err != nil {
		writeRPCError(w, nil, -32700, "parse error: read body", nil)
		return
	}
	var req rpcRequest
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&req); err != nil {
		writeRPCError(w, nil, -32700, "parse error: "+err.Error(), nil)
		return
	}
	if len(req.ID) == 0 || string(req.ID) == "null" {
		// JSON-RPC notification: acknowledge, no response body.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	switch req.Method {
	case "initialize":
		writeRPCResult(w, req.ID, map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "adcp-test-mock", "version": "0.1.0"},
		})
	case "tools/list":
		writeRPCResult(w, req.ID, map[string]any{"tools": s.toolList()})
	case "tools/call":
		s.handleCall(w, r.Context(), req)
	default:
		writeRPCError(w, req.ID, -32601, fmt.Sprintf("method not found: %s", req.Method), nil)
	}
}

func (s *Server) toolList() []map[string]any {
	out := make([]map[string]any, 0, len(s.routes))
	for _, rr := range s.routes {
		schema := map[string]any{"type": "object"}
		if rr.cfg.InputSchema != nil {
			if m, ok := rr.cfg.InputSchema.(map[string]any); ok {
				schema = m
			}
		}
		out = append(out, map[string]any{
			"name":        rr.cfg.Match.Tool,
			"description": "mock route from adcp-test",
			"inputSchema": schema,
		})
	}
	return out
}

func (s *Server) handleCall(w http.ResponseWriter, ctx context.Context, req rpcRequest) {
	name := req.Params.Name
	if name == "" {
		writeRPCError(w, req.ID, -32602, "invalid params: tools/call needs params.name", nil)
		return
	}
	rr := s.matchRoute(name, req.Params.Arguments)
	if rr == nil {
		writeRPCError(w, req.ID, -32601, fmt.Sprintf("no mock route for tool %q", name), nil)
		return
	}
	callNum := rr.calls.Add(1)
	args := req.Params.Arguments
	if args == nil {
		args = map[string]any{}
	}

	// State machine: enforce legal transitions before responding. The
	// tracker is shared server-wide so transitions span tools
	// (create_media_buy -> update_media_buy).
	var state, entityID string
	if rr.smName != "" {
		entityID = stringArg(args, "id", "media_buy_id")
		st, err := s.lifecycles[rr.smName].Apply(entityID, stringArg(args, "status"))
		if err != nil {
			var terr *TransitionError
			if e, ok := err.(*TransitionError); ok {
				terr = e
				writeRPCError(w, req.ID, -32001, terr.Error(), map[string]any{
					"from": terr.From, "to": terr.To, "allowed": terr.Allowed,
				})
			} else {
				writeRPCError(w, req.ID, -32002, err.Error(), nil)
			}
			return
		}
		state = st
	}

	// Latency profile.
	if rr.latency != nil {
		d := rr.latency.sample(s.rng, time.Since(s.startedAt))
		if d > 0 {
			t, cancel := context.WithTimeout(ctx, d)
			<-t.Done()
			cancel()
			if ctx.Err() != nil {
				return // client went away
			}
		}
	}

	// Fault injection.
	if f := rr.cfg.Faults; f != nil {
		roll := s.rng.Float64()
		switch {
		case roll < f.TimeoutRate:
			// Hang past the client timeout, then answer normally.
			t, cancel := context.WithTimeout(ctx, s.timeoutHang)
			<-t.Done()
			cancel()
			if ctx.Err() != nil {
				return
			}
		case roll < f.TimeoutRate+f.ErrorRate:
			writeRPCError(w, req.ID, -32000, "mock fault injection: simulated seller error", nil)
			return
		case roll < f.TimeoutRate+f.ErrorRate+f.MalformedRate:
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("this is not JSON {{{"))
			return
		}
	}

	// Static error responses (mocked rejections).
	if e := rr.cfg.Respond.Error; e != nil {
		writeRPCError(w, req.ID, e.Code, e.Message, e.Data)
		return
	}

	payload, err := s.buildPayload(rr, callNum, templateContext{
		Tool:     name,
		ID:       req.ID,
		Args:     args,
		Call:     callNum,
		Now:      time.Now(),
		State:    state,
		EntityID: entityID,
	})
	if err != nil {
		writeRPCError(w, req.ID, -32603, err.Error(), nil)
		return
	}
	writeRPCResult(w, req.ID, payload)
}

// stringArg returns the first non-empty string argument among keys.
func stringArg(args map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := args[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func writeRPCResult(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": rawID(id), "result": result})
}

func writeRPCError(w http.ResponseWriter, id json.RawMessage, code int, message string, data any) {
	w.Header().Set("Content-Type", "application/json")
	errObj := map[string]any{"code": code, "message": message}
	if data != nil {
		errObj["data"] = data
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": rawID(id), "error": errObj})
}

// rawID re-encodes the request id verbatim; missing ids become null.
func rawID(id json.RawMessage) any {
	if len(id) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(id, &v); err != nil {
		return nil
	}
	return v
}

// ---------------------------------------------------------------------------
// Matching
// ---------------------------------------------------------------------------

// matchRoute returns the first route whose tool and argument patterns
// match the incoming call.
func (s *Server) matchRoute(tool string, args map[string]any) *routeRuntime {
	for _, rr := range s.routes {
		if rr.cfg.Match.Tool != tool {
			continue
		}
		if matchArgs(rr.cfg.Match.Args, args) {
			return rr
		}
	}
	return nil
}

func matchArgs(pattern map[string]any, args map[string]any) bool {
	if args == nil {
		args = map[string]any{}
	}
	for k, pv := range pattern {
		var av any
		if strings.Contains(k, ".") {
			var ok bool
			av, ok = lookupPath(args, strings.Split(k, "."))
			if !ok {
				return false
			}
		} else {
			var ok bool
			av, ok = args[k]
			if !ok {
				return false
			}
		}
		if !matchValue(pv, av) {
			return false
		}
	}
	return true
}

// lookupPath walks nested maps along segments.
func lookupPath(m map[string]any, segments []string) (any, bool) {
	var cur any = m
	for _, seg := range segments {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = mm[seg]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

func matchValue(pattern, actual any) bool {
	switch pv := pattern.(type) {
	case string:
		s, ok := actual.(string)
		if !ok {
			return false
		}
		if strings.ContainsAny(pv, "*?[") {
			ok, err := matchGlob(pv, s)
			return err == nil && ok
		}
		return pv == s
	case map[string]any:
		am, ok := actual.(map[string]any)
		if !ok {
			return false
		}
		return matchArgs(pv, am)
	case []any:
		al, ok := actual.([]any)
		if !ok || len(pv) != len(al) {
			return false
		}
		for i := range pv {
			if !matchValue(pv[i], al[i]) {
				return false
			}
		}
		return true
	default:
		if pv == nil {
			return actual == nil
		}
		if pf, ok := toFloat(pv); ok {
			af, ok := toFloat(actual)
			return ok && pf == af
		}
		return fmt.Sprintf("%v", pv) == fmt.Sprintf("%v", actual)
	}
}

// toFloat converts JSON/YAML numbers to float64 for comparison.
func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// ---------------------------------------------------------------------------
// Latency profiles
// ---------------------------------------------------------------------------

type latencyModel struct {
	fixed      time.Duration
	p50, p99   time.Duration
	dist       bool
	spikeEvery time.Duration
	spikeFor   time.Duration
	spikeAdd   time.Duration
}

func buildLatencyModel(l *mockcfg.Latency) (*latencyModel, error) {
	m := &latencyModel{}
	parse := func(name, s string) (time.Duration, error) {
		d, err := time.ParseDuration(s)
		if err != nil {
			return 0, fmt.Errorf("latency.%s %q: %w", name, s, err)
		}
		return d, nil
	}
	switch {
	case l.Fixed != "":
		d, err := parse("fixed", l.Fixed)
		if err != nil {
			return nil, err
		}
		m.fixed = d
	case l.P50 != "" || l.P99 != "":
		if l.P50 != "" {
			d, err := parse("p50", l.P50)
			if err != nil {
				return nil, err
			}
			m.p50 = d
		}
		if l.P99 != "" {
			d, err := parse("p99", l.P99)
			if err != nil {
				return nil, err
			}
			m.p99 = d
		}
		m.dist = true
	}
	if l.Spike != nil {
		var err error
		if m.spikeEvery, err = parse("spike.every", l.Spike.Every); err != nil {
			return nil, err
		}
		if m.spikeFor, err = parse("spike.for", l.Spike.For); err != nil {
			return nil, err
		}
		if m.spikeAdd, err = parse("spike.add", l.Spike.Add); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// sample draws one latency: fixed, or a p50/p99-ish distribution
// (median ~p50, 99th percentile ~p99), plus spike windows on top.
func (m *latencyModel) sample(rng *rand.Rand, elapsed time.Duration) time.Duration {
	var d time.Duration
	switch {
	case m.dist:
		u := rng.Float64()
		switch {
		case u < 0.5:
			d = time.Duration(float64(m.p50) * (0.5 + 0.5*rng.Float64()))
		case u < 0.99:
			d = m.p50 + time.Duration(float64(m.p99-m.p50)*rng.Float64())
		default:
			d = m.p99 + time.Duration(float64(m.p99)*0.5*rng.Float64())
		}
	default:
		d = m.fixed
	}
	if m.spikeEvery > 0 && elapsed%m.spikeEvery < m.spikeFor {
		d += m.spikeAdd
	}
	return d
}

// ---------------------------------------------------------------------------
// Response payloads
// ---------------------------------------------------------------------------

func (s *Server) buildPayload(rr *routeRuntime, callNum uint64, tctx templateContext) (any, error) {
	r := rr.cfg.Respond
	switch {
	case r.File != "":
		return readJSONFile(s.resolve(r.File))
	case r.Template != "":
		raw, err := os.ReadFile(s.resolve(r.Template))
		if err != nil {
			return nil, fmt.Errorf("read template %q: %w", r.Template, err)
		}
		rendered, err := renderTemplate(string(raw), tctx)
		if err != nil {
			return nil, fmt.Errorf("render template %q: %w", r.Template, err)
		}
		var v any
		if err := json.Unmarshal([]byte(rendered), &v); err != nil {
			return nil, fmt.Errorf("template %q rendered invalid JSON: %w", r.Template, err)
		}
		return v, nil
	case len(r.Sequence) > 0:
		idx := callNum - 1
		if idx >= uint64(len(r.Sequence)) {
			idx = uint64(len(r.Sequence)) - 1 // clamp: extra calls repeat the last step
		}
		it := r.Sequence[idx]
		if it.File != "" {
			return readJSONFile(s.resolve(it.File))
		}
		return it.Inline, nil
	default:
		return r.Inline, nil
	}
}

func readJSONFile(path string) (any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", path, err)
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("parse %q: %w", path, err)
	}
	return v, nil
}

// matchGlob delegates to a tiny glob matcher supporting '*' and '?'.
// (Kept local so argument patterns never depend on path-separator rules.)
func matchGlob(pattern, s string) (bool, error) {
	px, sx := 0, 0
	star, ss := -1, 0
	for sx < len(s) {
		if px < len(pattern) && (pattern[px] == '?' || pattern[px] == s[sx]) {
			px++
			sx++
		} else if px < len(pattern) && pattern[px] == '*' {
			star = px
			ss = sx
			px++
		} else if star != -1 {
			px = star + 1
			ss++
			sx = ss
		} else {
			return false, nil
		}
	}
	for px < len(pattern) && pattern[px] == '*' {
		px++
	}
	if px != len(pattern) {
		return false, nil
	}
	return true, nil
}
