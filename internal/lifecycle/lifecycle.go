// Package lifecycle checks the media-buy lifecycle against a seller
// (real or mock): create -> activate -> pause -> resume -> cancel must
// succeed, and an illegal transition (resume after cancel) must be
// rejected with a structured error.
//
// Results use the conformance check shape {name, status, detail} so
// they slot into conformance reports and snapshot diffs.
package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sujanchalla0510/adcp-test/internal/conformance"
	"github.com/sujanchalla0510/adcp-test/internal/mcpclient"
)

// Options tunes a lifecycle run.
type Options struct {
	// Timeout is the per-request timeout; 30s when <= 0.
	Timeout time.Duration
	// BearerToken is sent as Authorization: Bearer when non-empty.
	BearerToken string
}

// Report is the lifecycle check outcome.
type Report struct {
	TargetURL  string                    `json:"target_url"`
	StartedAt  time.Time                 `json:"started_at"`
	FinishedAt time.Time                 `json:"finished_at"`
	MediaBuyID string                    `json:"media_buy_id,omitempty"`
	Checks     []conformance.CheckResult `json:"checks"`
}

// AllPassed reports whether every check passed.
func (r *Report) AllPassed() bool {
	if len(r.Checks) == 0 {
		return false
	}
	for _, c := range r.Checks {
		if c.Status != conformance.StatusPass {
			return false
		}
	}
	return true
}

// Run walks one media buy through its lifecycle against target. It
// returns a non-nil error only for unusable input; every seller-side
// outcome lands in the report.
func Run(ctx context.Context, target string, opts Options) (*Report, error) {
	if strings.TrimSpace(target) == "" {
		return nil, fmt.Errorf("lifecycle: target URL is required")
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	client := &mcpclient.Client{Endpoint: target, Timeout: timeout, BearerToken: opts.BearerToken}
	rep := &Report{TargetURL: target, StartedAt: time.Now().UTC()}
	r := &checker{ctx: ctx, client: client, rep: rep}

	id, idKey := r.create()
	if id == "" {
		rep.FinishedAt = time.Now().UTC()
		return rep, nil // create failed; remaining checks are meaningless
	}
	rep.MediaBuyID = id
	r.transition("lifecycle:activate", id, idKey, "active")
	r.transition("lifecycle:pause", id, idKey, "paused")
	r.transition("lifecycle:resume", id, idKey, "active")
	r.transition("lifecycle:cancel", id, idKey, "cancelled")
	r.illegalTransition(id, idKey)

	rep.FinishedAt = time.Now().UTC()
	return rep, nil
}

type checker struct {
	ctx    context.Context
	client *mcpclient.Client
	rep    *Report
}

func (c *checker) record(name string, status conformance.Status, detail string, d time.Duration) {
	c.rep.Checks = append(c.rep.Checks, conformance.CheckResult{
		Name: name, Status: status, Detail: detail,
		DurationMs: float64(d) / float64(time.Millisecond),
	})
}

// create issues the create_media_buy call and extracts the buy id.
// Returns the id and the argument key the seller used for it.
func (c *checker) create() (string, string) {
	const name = "lifecycle:create"
	args := map[string]any{
		"buyer_ref":     fmt.Sprintf("adcp-test-lifecycle-%d", time.Now().Unix()),
		"account":       "adcp-test",
		"brand":         "adcp-test",
		"start_time":    time.Now().UTC().Format(time.RFC3339),
		"end_time":      time.Now().Add(7 * 24 * time.Hour).UTC().Format(time.RFC3339),
		"budget_micros": 1000000,
	}
	start := time.Now()
	res, err := c.client.CallTool(c.ctx, "create_media_buy", args)
	d := time.Since(start)
	if err != nil {
		c.record(name, conformance.StatusFail, "create_media_buy failed: "+describeErr(err), d)
		return "", ""
	}
	if res.IsError {
		c.record(name, conformance.StatusFail, "create_media_buy returned a tool-level error", d)
		return "", ""
	}
	id, key := extractID(res.Raw)
	if id == "" {
		c.record(name, conformance.StatusFail, "create_media_buy succeeded but the response carries no media-buy id", d)
		return "", ""
	}
	detail := fmt.Sprintf("created media buy %q", id)
	if st := extractStatus(res.Raw); st != "" {
		detail += fmt.Sprintf(" (status %q)", st)
	}
	c.record(name, conformance.StatusPass, detail, d)
	return id, key
}

// transition issues update_media_buy moving the buy to status.
func (c *checker) transition(name, id, idKey, status string) {
	args := map[string]any{idKey: id, "status": status}
	start := time.Now()
	res, err := c.client.CallTool(c.ctx, "update_media_buy", args)
	d := time.Since(start)
	switch {
	case err != nil:
		c.record(name, conformance.StatusFail,
			fmt.Sprintf("update_media_buy -> %q failed: %s", status, describeErr(err)), d)
	case res.IsError:
		c.record(name, conformance.StatusFail,
			fmt.Sprintf("update_media_buy -> %q returned a tool-level error", status), d)
	default:
		detail := fmt.Sprintf("update_media_buy -> %q accepted", status)
		if st := extractStatus(res.Raw); st != "" {
			detail += fmt.Sprintf(" (seller reports status %q)", st)
		}
		c.record(name, conformance.StatusPass, detail, d)
	}
}

// illegalTransition asserts the seller rejects resume-after-cancel with
// a structured error.
func (c *checker) illegalTransition(id, idKey string) {
	const name = "lifecycle:illegal-transition-rejected"
	args := map[string]any{idKey: id, "status": "active"}
	start := time.Now()
	res, err := c.client.CallTool(c.ctx, "update_media_buy", args)
	d := time.Since(start)
	switch {
	case err == nil && !res.IsError:
		c.record(name, conformance.StatusFail,
			"update_media_buy cancelled -> active was ACCEPTED; a conforming seller must reject it", d)
	case err == nil:
		c.record(name, conformance.StatusPass,
			"cancelled -> active returned a tool-level error (rejected; not an RPC error, but not accepted)", d)
	default:
		var cerr *mcpclient.Error
		if errors.As(err, &cerr) && cerr.Kind == mcpclient.KindRPC {
			c.record(name, conformance.StatusPass,
				fmt.Sprintf("cancelled -> active rejected with structured JSON-RPC error %d (%s)",
					cerr.RPC.Code, cerr.RPC.Message), d)
		} else {
			c.record(name, conformance.StatusFail,
				"cancelled -> active did not come back as a structured rejection: "+describeErr(err), d)
		}
	}
}

// extractID finds the media-buy id in a create response, trying the
// common shapes. Returns the id and the key it was found under.
func extractID(raw json.RawMessage) (string, string) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", ""
	}
	for _, key := range []string{"media_buy_id", "id"} {
		if s, ok := lookupString(v, key); ok {
			return s, key
		}
	}
	if m, ok := v.(map[string]any); ok {
		if nested, ok := m["media_buy"].(map[string]any); ok {
			for _, key := range []string{"media_buy_id", "id"} {
				if s, ok := nested[key].(string); ok && s != "" {
					return s, key
				}
			}
		}
	}
	return "", ""
}

func lookupString(v any, key string) (string, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return "", false
	}
	s, ok := m[key].(string)
	return s, ok && s != ""
}

// extractStatus reads the buy status from an update response
// (best-effort; sellers vary).
func extractStatus(raw json.RawMessage) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return ""
	}
	if s, ok := lookupString(v, "status"); ok {
		return s
	}
	if m, ok := v.(map[string]any); ok {
		if nested, ok := m["media_buy"].(map[string]any); ok {
			if s, ok := nested["status"].(string); ok {
				return s
			}
		}
	}
	return ""
}

func describeErr(err error) string {
	var cerr *mcpclient.Error
	if errors.As(err, &cerr) {
		return cerr.Error()
	}
	return err.Error()
}
