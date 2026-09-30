// Package conformance checks an AdCP seller agent's MCP endpoint for
// protocol conformance and produces a machine-readable report.
//
// Checks, in run order:
//  1. tool-surface — tools/list returns the expected AdCP tool surface.
//  2. schema:<tool> — each advertised core tool's declared inputSchema is a
//     well-formed object schema declaring the expected required fields.
//  3. auth:unsigned-mutating-call-rejected — a create_media_buy sent with no
//     Signature headers must come back as a structured rejection, never a
//     success. (Behavioral probe: the probe arguments are deliberately
//     invalid so no conforming seller can act on them.)
//  4. auth:malformed-signature-rejected — the same call with garbage
//     RFC 9421 Signature-Input/Signature headers must be rejected.
//  5. auth:get-products-behavior — documents how the read-only get_products
//     behaves (success vs. structured rejection).
//  6. errors:unknown-tool-structured — calling a tool that does not exist
//     must return a structured JSON-RPC error, not HTML/plaintext/empty.
//  7. errors:invalid-args-structured — a call with invalid arguments must
//     return a structured JSON-RPC error.
//
// These are behavioral probes against the URL the user configures, not
// exploits: probes never touch customer data and never attempt to bypass
// anything, they only observe how the endpoint responds.
package conformance

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/sujanchalla0510/adcp-test/internal/mcpclient"
)

// Status is a check outcome.
type Status string

const (
	StatusPass Status = "pass"
	StatusFail Status = "fail"
	StatusSkip Status = "skip"
)

// CheckResult is one conformance check's outcome.
type CheckResult struct {
	Name       string  `json:"name"`
	Status     Status  `json:"status"`
	Detail     string  `json:"detail"`
	DurationMs float64 `json:"duration_ms"`
}

// Summary aggregates a report's check outcomes.
type Summary struct {
	Total   int `json:"total"`
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
}

// Report is the machine-readable conformance report.
type Report struct {
	TargetURL  string        `json:"target_url"`
	StartedAt  time.Time     `json:"started_at"`
	FinishedAt time.Time     `json:"finished_at"`
	Summary    Summary       `json:"summary"`
	Checks     []CheckResult `json:"checks"`
}

// AllPassed reports whether every check passed (no fails; skips allowed).
func (r *Report) AllPassed() bool {
	return r.Summary.Failed == 0 && r.Summary.Total > 0
}

// Options tunes a conformance run.
type Options struct {
	// Timeout is the per-request timeout for MCP calls.
	Timeout time.Duration
	// BearerToken is sent as Authorization: Bearer on every probe.
	BearerToken string
	// Profile selects the required tool surface: "full" (default;
	// historical strict behavior — every Required tool), "media-buy",
	// "creative", or "signals", grounded in the AdCP spec's
	// docs/protocol/required-tasks.mdx ("Required tasks by protocol").
	Profile string
}

// Profiles are the valid Options.Profile values.
var Profiles = []string{"full", "media-buy", "creative", "signals"}

// normalizeProfile resolves "" to "full" and rejects unknown profiles.
func normalizeProfile(p string) (string, error) {
	if p == "" {
		return "full", nil
	}
	for _, v := range Profiles {
		if p == v {
			return p, nil
		}
	}
	return "", fmt.Errorf("conformance: unknown profile %q (valid: %s)", p, strings.Join(Profiles, ", "))
}

// profileRequired reports whether the tool is required under the profile.
func profileRequired(t ExpectedTool, profile string) bool {
	if profile == "full" {
		return t.Required
	}
	for _, p := range t.Profiles {
		if p == profile {
			return true
		}
	}
	return false
}

// DefaultTimeout is used when Options.Timeout is <= 0.
const DefaultTimeout = 30 * time.Second

// Run executes the conformance suite against targetURL and returns the
// report. It returns a non-nil error only for unusable input (empty or
// non-HTTP(S) target URL); every seller-side outcome lands in the report.
func Run(ctx context.Context, targetURL string, opts Options) (*Report, error) {
	if strings.TrimSpace(targetURL) == "" {
		return nil, fmt.Errorf("conformance: target URL is empty")
	}
	u, err := url.Parse(targetURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("conformance: target URL %q is not a valid http(s) URL", targetURL)
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	profile, err := normalizeProfile(opts.Profile)
	if err != nil {
		return nil, err
	}
	client := &mcpclient.Client{Endpoint: targetURL, Timeout: timeout, BearerToken: opts.BearerToken}

	rep := &Report{TargetURL: targetURL, StartedAt: time.Now().UTC()}
	r := &runner{ctx: ctx, client: client, profile: profile}

	tools, surfaceOK := r.checkToolSurface(rep)
	r.checkSchemas(rep, tools, surfaceOK)
	r.checkAuthProbes(rep, tools, surfaceOK)
	r.checkErrorTaxonomy(rep, tools)

	rep.FinishedAt = time.Now().UTC()
	rep.Summary = summarize(rep.Checks)
	return rep, nil
}

type runner struct {
	ctx     context.Context
	client  *mcpclient.Client
	profile string
}

func (r *runner) record(rep *Report, name string, status Status, detail string, d time.Duration) {
	rep.Checks = append(rep.Checks, CheckResult{
		Name: name, Status: status, Detail: detail,
		DurationMs: float64(d) / float64(time.Millisecond),
	})
}

// checkToolSurface calls tools/list and verifies the expected AdCP tool
// surface. Returns the advertised tools and whether the call succeeded.
func (r *runner) checkToolSurface(rep *Report) ([]mcpclient.Tool, bool) {
	start := time.Now()
	tools, err := r.client.ListTools(r.ctx)
	d := time.Since(start)
	if err != nil {
		r.record(rep, "tool-surface", StatusFail, describeClientError("tools/list", err), d)
		return nil, false
	}
	present := map[string]bool{}
	for _, t := range tools {
		present[t.Name] = true
	}
	var missing, missingOptional []string
	for _, exp := range CoreTools {
		if !present[exp.Name] {
			if profileRequired(exp, r.profile) {
				missing = append(missing, exp.Name)
			} else {
				missingOptional = append(missingOptional, exp.Name)
			}
		}
	}
	if len(missing) > 0 {
		r.record(rep, "tool-surface", StatusFail,
			fmt.Sprintf("profile %q: advertised %d tools; missing required: %s", r.profile, len(tools), strings.Join(missing, ", ")), d)
		return tools, true
	}
	detail := fmt.Sprintf("profile %q: advertised %d tools; all %d required AdCP tools present", r.profile, len(tools), countRequired(r.profile))
	if len(missingOptional) > 0 {
		detail += fmt.Sprintf("; optional tools absent (informational): %s", strings.Join(missingOptional, ", "))
	}
	r.record(rep, "tool-surface", StatusPass, detail, d)
	return tools, true
}

func countRequired(profile string) int {
	n := 0
	for _, t := range CoreTools {
		if profileRequired(t, profile) {
			n++
		}
	}
	return n
}

// checkSchemas validates each advertised core tool's declared inputSchema.
func (r *runner) checkSchemas(rep *Report, tools []mcpclient.Tool, surfaceOK bool) {
	byName := map[string]mcpclient.Tool{}
	for _, t := range tools {
		byName[t.Name] = t
	}
	expected := expectedToolByName()
	for _, exp := range CoreTools {
		name := "schema:" + exp.Name
		t, ok := byName[exp.Name]
		if !ok {
			switch {
			case !surfaceOK:
				r.record(rep, name, StatusSkip, "skipped: tools/list failed", 0)
			case r.profile != "full" && !profileRequired(exp, r.profile):
				r.record(rep, name, StatusSkip, fmt.Sprintf("skipped: not required under profile %q", r.profile), 0)
			case profileRequired(exp, r.profile):
				r.record(rep, name, StatusSkip, "skipped: tool not advertised (see tool-surface)", 0)
			default:
				r.record(rep, name, StatusSkip, "skipped: optional tool not advertised", 0)
			}
			continue
		}
		_ = expected
		start := time.Now()
		problems := validateInputSchema(exp, t.InputSchema)
		d := time.Since(start)
		if len(problems) > 0 {
			r.record(rep, name, StatusFail, "inputSchema problems: "+strings.Join(problems, "; "), d)
			continue
		}
		r.record(rep, name, StatusPass, "inputSchema is a well-formed object schema with expected required fields", d)
	}
}

// checkAuthProbes runs the behavioral auth checks.
func (r *runner) checkAuthProbes(rep *Report, tools []mcpclient.Tool, surfaceOK bool) {
	present := map[string]bool{}
	for _, t := range tools {
		present[t.Name] = true
	}

	// Probe arguments are valid and well-typed per the AdCP 3.1 schemas
	// (they must survive argument validation so the seller's rejection,
	// if any, is about authentication — not about bad arguments).
	// Every value is prefixed adcp-test-probe- so no seller can mistake
	// them for a real buy, and the probes are never RFC 9421 signed, so
	// a conforming seller rejects them before doing anything.
	validProbeArgs := map[string]any{
		"idempotency_key": "adcp-test-probe-00000000-0000-0000-0000-000000000000",
		"account":         "adcp-test-probe-account",
		"brand":           "adcp-test-probe-brand",
		"start_time":      "2030-01-01T00:00:00Z",
		"end_time":        "2030-02-01T00:00:00Z",
	}

	// (a) unsigned mutating call.
	r.authProbe(rep, "auth:unsigned-mutating-call-rejected", present, probeMutatingTool, surfaceOK,
		validProbeArgs, nil,
		"unsigned create_media_buy")

	// (b) malformed RFC 9421 signature headers.
	r.authProbe(rep, "auth:malformed-signature-rejected", present, probeMutatingTool, surfaceOK,
		validProbeArgs,
		map[string]string{
			"Signature-Input": `sig1=("@method" "@target-uri");created=notanumber`,
			"Signature":       `sig1=:!!!not-base64!!!:`,
		},
		"create_media_buy with malformed Signature-Input/Signature headers")

	// (c) read-only behavior documentation.
	name := "auth:get-products-behavior"
	if !surfaceOK || !present[probeReadTool] {
		reason := "skipped: tool not advertised"
		if !surfaceOK {
			reason = "skipped: tools/list failed"
		}
		r.record(rep, name, StatusSkip, reason, 0)
		return
	}
	start := time.Now()
	res, err := r.client.CallTool(r.ctx, probeReadTool, map[string]any{
		"buying_mode": "brief",
		"brief":       "adcp-test conformance probe (synthetic; no real buy)",
	})
	d := time.Since(start)
	switch {
	case err == nil && res.IsError:
		r.record(rep, name, StatusPass,
			"get_products returned a tool-level error for the probe brief (documented; read path is guarded)", d)
	case err == nil:
		r.record(rep, name, StatusPass,
			"get_products succeeded for the probe brief (documented; read path reachable without signing)", d)
	default:
		var cerr *mcpclient.Error
		if errors.As(err, &cerr) && cerr.Kind == mcpclient.KindRPC {
			r.record(rep, name, StatusPass,
				fmt.Sprintf("get_products rejected the probe with structured JSON-RPC error %d (%s) (documented)",
					cerr.RPC.Code, cerr.RPC.Message), d)
		} else {
			r.record(rep, name, StatusFail, describeClientError("get_products probe", err), d)
		}
	}
}

// authProbe sends one mutating probe and requires an auth/signature-class
// rejection.
//
// The probe carries VALID, well-typed, clearly synthetic arguments, so a
// seller that validates arguments before checking signatures will pass
// argument validation and only reject on the missing/malformed signature.
// PASS requires the rejection to be auth/signature-class: a structured
// JSON-RPC error whose message looks like an auth error, or a tool-level
// error whose text does. A rejection for any other reason (e.g. argument
// validation) is a FAIL — the probe cannot distinguish "seller enforces
// signing" from "seller rejects bad arguments", and claiming otherwise
// would be a false positive. Acceptance is a FAIL: the seller performed
// (or would perform) a mutating call with no signature.
func (r *runner) authProbe(rep *Report, name string, present map[string]bool, tool string, surfaceOK bool,
	args map[string]any, headers map[string]string, what string) {
	if !surfaceOK || !present[tool] {
		reason := "skipped: tool not advertised"
		if !surfaceOK {
			reason = "skipped: tools/list failed"
		}
		r.record(rep, name, StatusSkip, reason, 0)
		return
	}
	start := time.Now()
	var res *mcpclient.CallResult
	var err error
	if headers == nil {
		res, err = r.client.CallTool(r.ctx, tool, args)
	} else {
		res, err = r.client.CallTool(r.ctx, tool, args, mcpclient.WithHeaders(headers))
	}
	d := time.Since(start)
	switch {
	case err == nil && !res.IsError:
		r.record(rep, name, StatusFail,
			fmt.Sprintf("%s was ACCEPTED by the seller (no rejection); a conforming seller must reject unsigned/malformed mutating calls", what), d)
	case err == nil:
		text := toolErrorText(res)
		if looksLikeAuthText(text) {
			r.record(rep, name, StatusPass,
				fmt.Sprintf("%s rejected with auth/signature-class tool error: %s", what, truncate(text, 200)), d)
		} else {
			r.record(rep, name, StatusFail,
				fmt.Sprintf("%s rejected, but not for auth/signature reasons (tool error: %s); cannot confirm the seller enforces signing", what, truncate(text, 200)), d)
		}
	default:
		var cerr *mcpclient.Error
		if errors.As(err, &cerr) && cerr.Kind == mcpclient.KindRPC {
			if looksLikeAuthError(cerr.RPC) {
				r.record(rep, name, StatusPass,
					fmt.Sprintf("%s rejected with auth/signature-class error: JSON-RPC %d (%s)", what, cerr.RPC.Code, cerr.RPC.Message), d)
			} else {
				r.record(rep, name, StatusFail,
					fmt.Sprintf("%s rejected with non-auth error JSON-RPC %d (%s); cannot confirm the seller enforces signing", what, cerr.RPC.Code, cerr.RPC.Message), d)
			}
		} else {
			r.record(rep, name, StatusFail, describeClientError(what, err), d)
		}
	}
}

// toolErrorText joins the text content items of a tool-level error.
func toolErrorText(res *mcpclient.CallResult) string {
	if res == nil {
		return ""
	}
	var parts []string
	for _, c := range res.Content {
		if strings.TrimSpace(c.Text) != "" {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, " ")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// looksLikeAuthError heuristically detects auth/signature rejections.
func looksLikeAuthError(rpc *mcpclient.RPCError) bool {
	if rpc == nil {
		return false
	}
	return looksLikeAuthText(rpc.Message + " " + string(rpc.Data))
}

// looksLikeAuthText heuristically detects auth/signature rejection text,
// whether it arrives as a JSON-RPC error or a tool-level error payload.
func looksLikeAuthText(text string) bool {
	hay := strings.ToLower(text)
	for _, kw := range []string{"auth", "sign", "unauthorized", "forbidden", "401", "403", "authenticate", "credential", "token", "keyid", "jwks"} {
		if strings.Contains(hay, kw) {
			return true
		}
	}
	return false
}

// checkErrorTaxonomy verifies the seller returns structured JSON-RPC
// errors for known-bad calls instead of HTML/plaintext/empty responses.
func (r *runner) checkErrorTaxonomy(rep *Report, tools []mcpclient.Tool) {
	// Unknown tool.
	r.errorProbe(rep, "errors:unknown-tool-structured", probeUnknownTool,
		map[string]any{"brief": "probe"}, "call to a tool that does not exist")

	// Invalid arguments: use create_media_buy when advertised, else the
	// read tool, with a deliberately mistyped argument.
	present := map[string]bool{}
	for _, t := range tools {
		present[t.Name] = true
	}
	tool, args := probeMutatingTool, map[string]any{"start_time": 12345, "end_time": 12345}
	if !present[probeMutatingTool] && present[probeReadTool] {
		tool, args = probeReadTool, map[string]any{"brief": 12345}
	}
	r.errorProbe(rep, "errors:invalid-args-structured", tool, args,
		fmt.Sprintf("call to %s with invalid argument types", tool))
}

func (r *runner) errorProbe(rep *Report, name, tool string, args map[string]any, what string) {
	start := time.Now()
	_, err := r.client.CallTool(r.ctx, tool, args)
	d := time.Since(start)
	if err == nil {
		r.record(rep, name, StatusFail,
			fmt.Sprintf("%s unexpectedly succeeded; expected a structured JSON-RPC error", what), d)
		return
	}
	var cerr *mcpclient.Error
	if errors.As(err, &cerr) && cerr.Kind == mcpclient.KindRPC && cerr.RPC.Message != "" {
		r.record(rep, name, StatusPass,
			fmt.Sprintf("%s returned structured JSON-RPC error %d (%s)", what, cerr.RPC.Code, cerr.RPC.Message), d)
		return
	}
	r.record(rep, name, StatusFail,
		fmt.Sprintf("%s did not return a structured JSON-RPC error: %s", what, describeClientError("", err)), d)
}

// describeClientError renders an *mcpclient.Error as a check detail line.
func describeClientError(what string, err error) string {
	var cerr *mcpclient.Error
	if !errors.As(err, &cerr) {
		if what == "" {
			return err.Error()
		}
		return fmt.Sprintf("%s: %v", what, err)
	}
	prefix := ""
	if what != "" {
		prefix = what + ": "
	}
	switch cerr.Kind {
	case mcpclient.KindTransport:
		return fmt.Sprintf("%stransport failure: %v", prefix, cerr.Err)
	case mcpclient.KindProtocol:
		return fmt.Sprintf("%sprotocol failure: %v", prefix, cerr.Err)
	default:
		return fmt.Sprintf("%s%s", prefix, cerr.Err)
	}
}

func summarize(checks []CheckResult) Summary {
	s := Summary{Total: len(checks)}
	for _, c := range checks {
		switch c.Status {
		case StatusPass:
			s.Passed++
		case StatusFail:
			s.Failed++
		case StatusSkip:
			s.Skipped++
		}
	}
	return s
}
