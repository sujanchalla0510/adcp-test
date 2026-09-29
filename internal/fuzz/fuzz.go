// Package fuzz is a small runtime protocol fuzzer: it throws malformed
// and random JSON-RPC payloads at an MCP target (localhost only, unless
// the caller explicitly opts into a remote URL) and records crashes,
// hangs, and non-JSON responses into a session.
//
// It is deliberately dumb: its job is to find the cases where the
// seller's transport falls over, not to exercise the protocol
// correctly. Conformance and scenario packs cover the happy path.
package fuzz

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sujanchalla0510/adcp-test/internal/session"
)

// Options tunes a fuzz run.
type Options struct {
	// Target is the MCP endpoint URL (required).
	Target string
	// Iterations is the number of payloads to send; 200 when <= 0.
	Iterations int
	// Timeout is the per-request timeout; 10s when <= 0.
	Timeout time.Duration
	// BearerToken is sent as Authorization: Bearer when non-empty.
	BearerToken string
	// Seed seeds the payload generator; 0 picks a random seed.
	Seed int64
	// AllowRemote disables the localhost guard. Without it, non-local
	// targets are refused.
	AllowRemote bool
	// Store receives the recorded session; created when nil.
	Store *session.Store
}

// Finding is one abnormal target response.
type Finding struct {
	Iteration int    `json:"iteration"`
	Payload   string `json:"payload"` // truncated to 4 KiB
	Kind      string `json:"kind"`    // "crash" | "hang" | "non-json" | "transport-error"
	Detail    string `json:"detail"`
}

// Report is the fuzz run outcome.
type Report struct {
	TargetURL  string    `json:"target_url"`
	Seed       int64     `json:"seed"`
	Iterations int       `json:"iterations"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	SessionID  string    `json:"session_id,omitempty"`
	Findings   []Finding `json:"findings"`
}

// Passed reports whether the target survived every payload.
func (r *Report) Passed() bool { return len(r.Findings) == 0 }

// isLocalhost reports whether raw targets a loopback address.
func isLocalhost(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip != nil && ip.IsLoopback() {
		return true
	}
	if host == "" {
		return false
	}
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		return false
	}
	for _, ip := range ips {
		if !ip.IsLoopback() {
			return false
		}
	}
	return true
}

// Run executes the fuzz run. It returns an error only for unusable
// input; target misbehavior lands in the report as findings.
func Run(ctx context.Context, opts Options) (*Report, error) {
	if strings.TrimSpace(opts.Target) == "" {
		return nil, fmt.Errorf("fuzz: target URL is required")
	}
	if !opts.AllowRemote && !isLocalhost(opts.Target) {
		return nil, fmt.Errorf("fuzz: refusing non-localhost target %q without AllowRemote", opts.Target)
	}
	iters := opts.Iterations
	if iters <= 0 {
		iters = 200
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	seed := opts.Seed
	if seed == 0 {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			seed = time.Now().UnixNano()
		} else {
			seed = int64(binary.LittleEndian.Uint64(b[:]))
		}
	}
	gen := newGenerator(seed)
	store := opts.Store
	if store == nil {
		store = session.NewStore()
	}
	sess := store.New(opts.Target)
	rep := &Report{
		TargetURL:  opts.Target,
		Seed:       seed,
		Iterations: iters,
		StartedAt:  time.Now().UTC(),
		SessionID:  sess.ID,
	}
	client := &http.Client{Timeout: timeout}
	for i := 0; i < iters; i++ {
		payload := gen.next()
		if finding := probe(ctx, client, opts, sess, payload, i); finding != nil {
			rep.Findings = append(rep.Findings, *finding)
		}
	}
	rep.FinishedAt = time.Now().UTC()
	return rep, nil
}

// probe sends one payload, records the exchange in the session, and
// classifies the target's reaction.
func probe(ctx context.Context, client *http.Client, opts Options, sess *session.Session, payload []byte, i int) *Finding {
	tool := fmt.Sprintf("iteration-%d", i)
	reqID := fmt.Sprintf("fuzz-%d", i)
	sess.Append(session.NewOutStep("fuzz/send", tool, reqID, payload, nil))
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, opts.Target, bytes.NewReader(payload))
	if err != nil {
		sess.Append(session.NewErrorStep("fuzz/send", tool, reqID, err.Error(), time.Since(start)))
		return &Finding{Iteration: i, Payload: truncate(payload), Kind: "transport-error", Detail: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if opts.BearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+opts.BearerToken)
	}
	resp, err := client.Do(req)
	dur := time.Since(start)
	if err != nil {
		// Timeouts and connection resets count as hangs/crashes worth
		// reporting rather than hard failures of the run.
		kind := "transport-error"
		if isTimeout(err) {
			kind = "hang"
		}
		sess.Append(session.NewErrorStep("fuzz/send", tool, reqID, err.Error(), dur))
		return &Finding{Iteration: i, Payload: truncate(payload), Kind: kind, Detail: err.Error()}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		sess.Append(session.NewErrorStep("fuzz/send", tool, reqID, "reading body: "+err.Error(), dur))
		return &Finding{Iteration: i, Payload: truncate(payload), Kind: "transport-error", Detail: "reading body: " + err.Error()}
	}
	sess.Append(session.NewInStep("fuzz/recv", tool, reqID, body, dur))
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && !json.Valid(trimmed) {
		// A 500 with an HTML error page is interesting; a JSON-RPC
		// error object is fine (json.Valid passes it).
		return &Finding{
			Iteration: i,
			Payload:   truncate(payload),
			Kind:      "non-json",
			Detail:    fmt.Sprintf("HTTP %d with non-JSON body (%d bytes)", resp.StatusCode, len(body)),
		}
	}
	if resp.StatusCode >= 500 {
		return &Finding{
			Iteration: i,
			Payload:   truncate(payload),
			Kind:      "crash",
			Detail:    fmt.Sprintf("HTTP %d (server error)", resp.StatusCode),
		}
	}
	return nil
}

func isTimeout(err error) bool {
	var nerr net.Error
	if errors.As(err, &nerr) && nerr.Timeout() {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "timeout") ||
		strings.Contains(strings.ToLower(err.Error()), "deadline")
}

func truncate(b []byte) string {
	const max = 4096
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + fmt.Sprintf("...<%d bytes total>", len(b))
}
