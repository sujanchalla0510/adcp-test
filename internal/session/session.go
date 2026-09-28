// Package session models a recorded MCP traffic session: an ordered list
// of steps (one "out" step for each request forwarded to the seller, one
// "in" step for each response or failure), held in a thread-safe
// in-memory store with JSON serialization.
//
// Secret-safe by construction: request/response payloads are passed
// through mcpclient.RedactJSON and headers through
// mcpclient.RedactHeaders at capture time, so raw tokens, signatures,
// and authorization headers never reach the store.
package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/sujanchalla0510/adcp-test/internal/mcpclient"
)

// Direction marks which way a step's payload flowed relative to the
// recorder: out = client -> seller (a request we forwarded), in =
// seller -> client (the response or failure we observed).
type Direction string

const (
	// DirectionOut is a request forwarded toward the seller.
	DirectionOut Direction = "out"
	// DirectionIn is a response (or failure) coming back from the seller.
	DirectionIn Direction = "in"
)

// Step is one half of a recorded JSON-RPC exchange. An out step carries
// the (redacted) request; the matching in step, linked by RequestID,
// carries the (redacted) response or a transport error.
type Step struct {
	TS        time.Time       `json:"ts"`
	Direction Direction       `json:"direction"`
	Method    string          `json:"method"`             // "tools/list", "tools/call", or "unknown"
	Tool      string          `json:"tool,omitempty"`     // tool name for tools/call
	RequestID string          `json:"request_id"`         // links an out step to its in step
	Request   json.RawMessage `json:"request,omitempty"`  // redacted JSON-RPC request (out steps)
	Response  json.RawMessage `json:"response,omitempty"` // redacted JSON-RPC response (in steps)
	Error     string          `json:"error,omitempty"`    // transport/protocol failure (in steps)
	// DurationMs is the upstream round-trip time, set on in steps.
	DurationMs float64 `json:"duration_ms,omitempty"`
	// Headers are the redacted request headers, set on out steps.
	Headers map[string]string `json:"headers,omitempty"`
	// ArgProblems holds conformance argument-validation findings for
	// tools/call out steps; empty (and omitted) means the arguments
	// satisfy the tool's expected schema.
	ArgProblems []string `json:"arg_problems,omitempty"`
}

// NewOutStep builds an out step, redacting the payload and headers.
func NewOutStep(method, tool, requestID string, requestBody []byte, headers http.Header) *Step {
	redacted := mcpclient.RedactHeaders(headers)
	flat := make(map[string]string, len(redacted))
	for k, vs := range redacted {
		if len(vs) > 0 {
			flat[k] = vs[0]
		}
	}
	return &Step{
		TS:        time.Now().UTC(),
		Direction: DirectionOut,
		Method:    method,
		Tool:      tool,
		RequestID: requestID,
		Request:   mcpclient.RedactJSON(requestBody),
		Headers:   flat,
	}
}

// NewInStep builds an in step for a response received from the seller,
// redacting the payload.
func NewInStep(method, tool, requestID string, responseBody []byte, duration time.Duration) *Step {
	return &Step{
		TS:         time.Now().UTC(),
		Direction:  DirectionIn,
		Method:     method,
		Tool:       tool,
		RequestID:  requestID,
		Response:   mcpclient.RedactJSON(responseBody),
		DurationMs: float64(duration) / float64(time.Millisecond),
	}
}

// NewErrorStep builds an in step for a transport/protocol failure where
// no response was received.
func NewErrorStep(method, tool, requestID, errMsg string, duration time.Duration) *Step {
	return &Step{
		TS:         time.Now().UTC(),
		Direction:  DirectionIn,
		Method:     method,
		Tool:       tool,
		RequestID:  requestID,
		Error:      errMsg,
		DurationMs: float64(duration) / float64(time.Millisecond),
	}
}

// Session is an ordered list of steps recorded against one target.
type Session struct {
	ID        string    `json:"id"`
	StartedAt time.Time `json:"started_at"`
	TargetURL string    `json:"target_url,omitempty"`

	mu    sync.Mutex
	Steps []*Step `json:"steps"`
}

// Append adds a step to the session. Safe for concurrent use.
func (s *Session) Append(st *Step) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Steps = append(s.Steps, st)
}

// Snapshot returns a copy of the session's steps in order. Safe for
// concurrent use.
func (s *Session) Snapshot() []*Step {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Step, len(s.Steps))
	copy(out, s.Steps)
	return out
}

// Len returns the number of recorded steps. Safe for concurrent use.
func (s *Session) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.Steps)
}

// MarshalJSON serializes the session, holding the lock so a concurrent
// Append cannot race the encoder.
func (s *Session) MarshalJSON() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	type plain Session
	return json.Marshal((*plain)(s))
}

// UnmarshalJSON restores a session previously serialized with MarshalJSON.
func (s *Session) UnmarshalJSON(b []byte) error {
	type plain Session
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ID, s.StartedAt, s.TargetURL, s.Steps = p.ID, p.StartedAt, p.TargetURL, p.Steps
	return nil
}

// Store is a thread-safe in-memory registry of sessions keyed by ID.
type Store struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

// NewStore returns an empty session store.
func NewStore() *Store {
	return &Store{sessions: map[string]*Session{}}
}

// newSessionID generates a random session ID.
func newSessionID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("s-%d", time.Now().UnixNano())
	}
	return "s-" + hex.EncodeToString(b[:])
}

// New creates a session with a generated ID and registers it.
func (s *Store) New(targetURL string) *Session {
	return s.NewWithID(newSessionID(), targetURL)
}

// NewWithID returns the existing session for id, or creates and
// registers a new one. Safe for concurrent use.
func (s *Store) NewWithID(id, targetURL string) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess, ok := s.sessions[id]; ok {
		return sess
	}
	sess := &Session{ID: id, StartedAt: time.Now().UTC(), TargetURL: targetURL}
	s.sessions[id] = sess
	return sess
}

// Get returns the session for id.
func (s *Store) Get(id string) (*Session, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[id]
	return sess, ok
}

// List returns all sessions ordered by start time (oldest first).
func (s *Store) List() []*Session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		out = append(out, sess)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].StartedAt.Before(out[j].StartedAt)
	})
	return out
}
