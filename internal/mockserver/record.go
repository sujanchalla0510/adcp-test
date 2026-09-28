package mockserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/sujanchalla0510/adcp-test/internal/mockcfg"
)

// maxProxyBody caps one proxied request/response body in record mode.
const maxProxyBody = 2 << 20

// maxRecordedExchanges bounds memory while capturing record-mode traffic.
const maxRecordedExchanges = 5000

// RecordedExchange is one captured tools/call round trip.
type RecordedExchange struct {
	Tool    string
	Args    any
	Result  any
	Latency time.Duration
	At      time.Time
}

// Recorder is an HTTP proxy that forwards JSON-RPC traffic to an upstream
// seller and captures tools/call exchanges for mock config generation.
type Recorder struct {
	upstream *url.URL
	client   *http.Client

	mu        sync.Mutex
	exchanges []RecordedExchange
}

// NewRecorder builds a proxy for upstream (must be an http(s) URL).
func NewRecorder(upstream string) (*Recorder, error) {
	u, err := url.Parse(upstream)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("mockserver: bad upstream URL %q", upstream)
	}
	return &Recorder{
		upstream: u,
		client:   &http.Client{Timeout: 60 * time.Second},
	}, nil
}

func (r *Recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, maxProxyBody))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	start := time.Now()
	fwd, err := http.NewRequestWithContext(req.Context(), http.MethodPost, r.upstream.String(), bytes.NewReader(body))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	fwd.Header.Set("Content-Type", "application/json")
	fwd.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := r.client.Do(fwd)
	lat := time.Since(start)
	if err != nil {
		http.Error(w, "upstream: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxProxyBody))
	if err != nil {
		http.Error(w, "upstream read: "+err.Error(), http.StatusBadGateway)
		return
	}
	r.capture(body, respBody, lat)

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(respBody)
}

// capture records one tools/call exchange on a best-effort basis: anything
// that is not a JSON-RPC 2.0 success response is skipped silently.
func (r *Recorder) capture(reqBody, respBody []byte, lat time.Duration) {
	var req struct {
		Method string `json:"method"`
		Params struct {
			Name      string `json:"name"`
			Arguments any    `json:"arguments"`
		} `json:"params"`
	}
	if err := json.Unmarshal(reqBody, &req); err != nil {
		return
	}
	if req.Method != "tools/call" || req.Params.Name == "" {
		return
	}
	var resp struct {
		Result any `json:"result"`
		Error  any `json:"error"`
	}
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return
	}
	if resp.Result == nil || resp.Error != nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.exchanges) >= maxRecordedExchanges {
		return
	}
	r.exchanges = append(r.exchanges, RecordedExchange{
		Tool:    req.Params.Name,
		Args:    req.Params.Arguments,
		Result:  resp.Result,
		Latency: lat,
		At:      time.Now(),
	})
}

// Exchanges returns the captured exchanges in arrival order.
func (r *Recorder) Exchanges() []RecordedExchange {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]RecordedExchange, len(r.exchanges))
	copy(out, r.exchanges)
	return out
}

// GenerateConfig builds a mock config from recorded exchanges: one route
// per observed tool (first-seen order), response from the observed
// payloads (inline for a single distinct payload, sequenced otherwise),
// latency estimated from observed timings (p50/p99, or fixed for a single
// sample).
func GenerateConfig(ex []RecordedExchange, name, listen string) *mockcfg.Config {
	type toolGroup struct {
		tool      string
		results   []any
		seen      map[string]bool
		latencies []time.Duration
	}
	var order []string
	groups := map[string]*toolGroup{}
	for _, e := range ex {
		g, ok := groups[e.Tool]
		if !ok {
			g = &toolGroup{tool: e.Tool, seen: map[string]bool{}}
			groups[e.Tool] = g
			order = append(order, e.Tool)
		}
		key := canonicalJSON(e.Result)
		if !g.seen[key] {
			g.seen[key] = true
			if len(g.results) < 25 {
				g.results = append(g.results, e.Result)
			}
		}
		g.latencies = append(g.latencies, e.Latency)
	}

	svc := mockcfg.MockService{Name: name, Protocol: "adcp", Listen: listen}
	for _, tool := range order {
		g := groups[tool]
		route := mockcfg.Route{Match: mockcfg.Match{Tool: tool}}
		switch len(g.results) {
		case 0:
			continue
		case 1:
			route.Respond = mockcfg.Respond{Inline: g.results[0]}
		default:
			items := make([]mockcfg.SequenceItem, 0, len(g.results))
			for _, res := range g.results {
				items = append(items, mockcfg.SequenceItem{Inline: res})
			}
			route.Respond = mockcfg.Respond{Sequence: items}
		}
		route.Latency = latencyFromSamples(g.latencies)
		svc.Routes = append(svc.Routes, route)
	}
	return &mockcfg.Config{Mocks: []mockcfg.MockService{svc}}
}

// latencyFromSamples estimates a latency profile from observed timings.
func latencyFromSamples(samples []time.Duration) *mockcfg.Latency {
	if len(samples) == 0 {
		return nil
	}
	sorted := append([]time.Duration(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	if len(sorted) == 1 {
		return &mockcfg.Latency{Fixed: fmtDur(sorted[0])}
	}
	p50 := sorted[len(sorted)/2]
	p99 := sorted[(99*len(sorted)+99)/100-1]
	if p99 < p50 {
		p99 = p50
	}
	return &mockcfg.Latency{P50: fmtDur(p50), P99: fmtDur(p99)}
}

// fmtDur formats d for a config file, floored at 1ms (a 0s p50 would fail
// validation and means "faster than we can measure" anyway).
func fmtDur(d time.Duration) string {
	if d < time.Millisecond {
		d = time.Millisecond
	}
	return d.Round(time.Millisecond).String()
}

// canonicalJSON renders v as compact JSON for distinct-payload detection.
func canonicalJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%#v", v)
	}
	return string(b)
}
