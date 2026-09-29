// Package webhooks is a tiny local webhook listener for testing seller
// callbacks. It binds a localhost HTTP server, captures deliveries on a
// timeline with redacted headers and pretty-printed JSON payloads, and
// exposes the capture for inspection (CLI, API, UI).
//
// It is a test instrument, not a production receiver: localhost only,
// no auth, no retries, in-memory ring buffer.
package webhooks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// redacted lists headers whose values are replaced in the capture.
var redacted = map[string]bool{
	"authorization":       true,
	"proxy-authorization": true,
	"cookie":              true,
	"set-cookie":          true,
	"x-api-key":           true,
	"api-key":             true,
}

// Delivery is one captured webhook call.
type Delivery struct {
	ID        int               `json:"id"`
	Received  time.Time         `json:"received_at"`
	Method    string            `json:"method"`
	Path      string            `json:"path"`
	Query     string            `json:"query,omitempty"`
	Headers   map[string]string `json:"headers"`
	Body      json.RawMessage   `json:"body,omitempty"` // pretty JSON when parseable
	RawBody   string            `json:"raw_body,omitempty"`
	Truncated bool              `json:"truncated,omitempty"`
}

// Listener captures deliveries on a localhost port.
type Listener struct {
	mu         sync.Mutex
	deliveries []*Delivery
	nextID     int
	maxBody    int

	server   *http.Server
	listener net.Listener
}

// NewListener creates a listener; maxBody caps stored bodies (64 KiB
// default when <= 0).
func NewListener(maxBody int) *Listener {
	if maxBody <= 0 {
		maxBody = 64 << 10
	}
	return &Listener{maxBody: maxBody}
}

// Start binds 127.0.0.1:port (port 0 picks a free one) and serves.
// The returned URL is the delivery endpoint, e.g.
// http://127.0.0.1:PORT/hook.
func (l *Listener) Start(port int) (string, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return "", fmt.Errorf("webhooks: listen: %w", err)
	}
	l.listener = ln
	mux := http.NewServeMux()
	mux.HandleFunc("/", l.handle)
	l.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = l.server.Serve(ln) }()
	return "http://" + ln.Addr().String() + "/hook", nil
}

// Close shuts the listener down.
func (l *Listener) Close() error {
	if l.server == nil {
		return nil
	}
	return l.server.Close()
}

// Addr returns the bound address, or "" before Start.
func (l *Listener) Addr() string {
	if l.listener == nil {
		return ""
	}
	return l.listener.Addr().String()
}

func (l *Listener) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, int64(l.maxBody)+1))
	_ = r.Body.Close()
	d := &Delivery{
		Received: time.Now().UTC(),
		Method:   r.Method,
		Path:     r.URL.Path,
		Query:    r.URL.RawQuery,
		Headers:  map[string]string{},
	}
	for name, vals := range r.Header {
		key := strings.ToLower(name)
		v := strings.Join(vals, ", ")
		if redacted[key] || strings.Contains(key, "token") || strings.Contains(key, "secret") {
			v = "[redacted]"
		}
		d.Headers[canonicalHeaderKey(name)] = v
	}
	trimmed := bytes.TrimSpace(body)
	if json.Valid(trimmed) {
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, trimmed, "", "  "); err == nil {
			d.Body = json.RawMessage(pretty.Bytes())
		} else {
			d.Body = json.RawMessage(trimmed)
		}
	} else if len(trimmed) > 0 {
		d.RawBody = string(trimmed)
	}
	if len(body) > l.maxBody {
		d.Truncated = true
	}
	l.mu.Lock()
	l.nextID++
	d.ID = l.nextID
	l.deliveries = append(l.deliveries, d)
	l.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// Deliveries returns the capture in arrival order.
func (l *Listener) Deliveries() []*Delivery {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]*Delivery, len(l.deliveries))
	copy(out, l.deliveries)
	return out
}

// Clear drops the capture.
func (l *Listener) Clear() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.deliveries = nil
}

func canonicalHeaderKey(name string) string {
	parts := strings.Split(strings.ToLower(name), "-")
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "-")
}

// headerNames returns sorted header names for stable snapshots.
func headerNames(h map[string]string) []string {
	names := make([]string, 0, len(h))
	for k := range h {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}
