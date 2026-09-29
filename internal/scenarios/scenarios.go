// Package scenarios implements YAML scenario packs: ordered,
// assertion-checked tool-call flows against a seller MCP endpoint.
//
// A pack is a named collection of scenarios; each scenario is a sequence
// of tool-call steps with per-step assertions, plus optional setup and
// teardown hooks. Steps run sequentially through mcpclient against the
// target URL (a real seller, a mock server, or a replay URL). Every call
// is recorded into a session (one session per scenario) so runs stay
// inspectable after the fact.
//
// Steps can chain values between calls: a step's `save` map extracts
// values from its result into named variables, and later arguments may
// reference them as {{vars.name}}. A variable used as the entire
// argument string keeps its JSON type; embedded uses stringify.
//
// Assertion kinds:
//
//	status: ok | error        call succeeded (no RPC/transport error and
//	                         not isError) or failed
//	response_contains: {...} result JSON must contain the expected subset
//	                   (nested maps subset-match; lists match when every
//	                   expected element matches some actual element)
//	latency_ms_lt: N         step round-trip under N milliseconds
//	error_code: N            JSON-RPC error code (implies status: error)
package scenarios

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/sujanchalla0510/adcp-test/internal/mcpclient"
	"github.com/sujanchalla0510/adcp-test/internal/session"
)

// Assertion kinds.
const (
	AssertStatus          = "status"
	AssertResponseContain = "response_contains"
	AssertLatencyMsLt     = "latency_ms_lt"
	AssertErrorCode       = "error_code"
)

// Status values for the status assertion.
const (
	StatusOK    = "ok"
	StatusError = "error"
)

// Pack is a parsed scenario pack.
type Pack struct {
	Name        string     `yaml:"name" json:"name"`
	Description string     `yaml:"description" json:"description"`
	Scenarios   []Scenario `yaml:"scenarios" json:"scenarios"`
}

// Scenario is one named flow inside a pack.
type Scenario struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description" json:"description"`
	Setup       []Call `yaml:"setup,omitempty" json:"setup,omitempty"`
	Steps       []Step `yaml:"steps" json:"steps"`
	Teardown    []Call `yaml:"teardown,omitempty" json:"teardown,omitempty"`
}

// Call is a bare tool invocation (setup/teardown hooks).
type Call struct {
	Tool      string            `yaml:"tool" json:"tool"`
	Arguments map[string]any    `yaml:"arguments,omitempty" json:"arguments,omitempty"`
	Save      map[string]string `yaml:"save,omitempty" json:"save,omitempty"`
}

// Step is one asserted tool call in a scenario.
type Step struct {
	Name       string            `yaml:"name" json:"name"`
	Tool       string            `yaml:"tool" json:"tool"`
	Arguments  map[string]any    `yaml:"arguments,omitempty" json:"arguments,omitempty"`
	Save       map[string]string `yaml:"save,omitempty" json:"save,omitempty"`
	Assertions []Assertion       `yaml:"assertions,omitempty" json:"assertions,omitempty"`
}

// Assertion is a single-key map: kind -> expected value.
// Kept as a map so new kinds need no schema change.
type Assertion map[string]any

// Kind returns the assertion kind (the single map key).
func (a Assertion) Kind() string {
	for k := range a {
		return k
	}
	return ""
}

// Value returns the expected value for the kind.
func (a Assertion) Value() any {
	for _, v := range a {
		return v
	}
	return nil
}

// ParsePack parses pack YAML.
func ParsePack(data []byte) (*Pack, error) {
	var p Pack
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(false)
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("scenarios: parse pack: %w", err)
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

// Validate checks pack structure.
func (p *Pack) Validate() error {
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("scenarios: pack name is required")
	}
	if len(p.Scenarios) == 0 {
		return fmt.Errorf("scenarios: pack %q has no scenarios", p.Name)
	}
	for i, s := range p.Scenarios {
		if strings.TrimSpace(s.Name) == "" {
			return fmt.Errorf("scenarios: pack %q scenario[%d] has no name", p.Name, i)
		}
		if len(s.Steps) == 0 {
			return fmt.Errorf("scenarios: pack %q scenario %q has no steps", p.Name, s.Name)
		}
		for j, st := range s.Steps {
			if strings.TrimSpace(st.Tool) == "" {
				return fmt.Errorf("scenarios: pack %q scenario %q step[%d] has no tool", p.Name, s.Name, j)
			}
			for k, a := range st.Assertions {
				switch a.Kind() {
				case AssertStatus, AssertResponseContain, AssertLatencyMsLt, AssertErrorCode:
				default:
					return fmt.Errorf("scenarios: pack %q scenario %q step[%d] assertion[%d]: unknown kind %q",
						p.Name, s.Name, j, k, a.Kind())
				}
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Reports
// ---------------------------------------------------------------------------

// AssertionResult is the evaluated outcome of one assertion.
type AssertionResult struct {
	Kind   string `json:"kind"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail,omitempty"`
}

// StepResult is the outcome of one executed step.
type StepResult struct {
	Name       string            `json:"name"`
	Tool       string            `json:"tool"`
	Passed     bool              `json:"passed"`
	LatencyMs  float64           `json:"latency_ms"`
	Assertions []AssertionResult `json:"assertions"`
	Error      string            `json:"error,omitempty"` // transport/RPC failure
	Chaos      *ChaosMark        `json:"chaos,omitempty"` // set when chaos injection touched this step
}

// ScenarioResult is the outcome of one scenario.
type ScenarioResult struct {
	Name          string       `json:"name"`
	Passed        bool         `json:"passed"`
	SessionID     string       `json:"session_id"`
	SetupError    string       `json:"setup_error,omitempty"`
	TeardownError string       `json:"teardown_error,omitempty"`
	Steps         []StepResult `json:"steps"`
}

// Summary aggregates a report.
type Summary struct {
	Total  int `json:"total"`
	Passed int `json:"passed"`
	Failed int `json:"failed"`
}

// Report is the full pack run outcome.
type Report struct {
	Pack      string           `json:"pack"`
	TargetURL string           `json:"target_url"`
	StartedAt time.Time        `json:"started_at"`
	Summary   Summary          `json:"summary"`
	Scenarios []ScenarioResult `json:"scenarios"`
	Chaos     *ChaosSummary    `json:"chaos,omitempty"`
}

// AllPassed reports whether every scenario passed.
func (r *Report) AllPassed() bool {
	return r.Summary.Failed == 0 && r.Summary.Total > 0
}

// ---------------------------------------------------------------------------
// Runner
// ---------------------------------------------------------------------------

// Event kinds for progress callbacks.
const (
	EventScenarioStarted  = "scenario_started"
	EventStepStarted      = "step_started"
	EventStepFinished     = "step_finished"
	EventScenarioFinished = "scenario_finished"
)

// Event is a live progress notification from the runner.
type Event struct {
	Type           string          `json:"type"`
	Pack           string          `json:"pack"`
	Scenario       string          `json:"scenario"`
	Step           string          `json:"step,omitempty"`
	StepIndex      int             `json:"step_index,omitempty"`
	StepResult     *StepResult     `json:"step_result,omitempty"`
	ScenarioResult *ScenarioResult `json:"scenario_result,omitempty"`
}

// Options tunes a pack run.
type Options struct {
	// Timeout is the per-step request timeout; mcpclient default when <= 0.
	Timeout time.Duration
	// BearerToken is sent as Authorization: Bearer when non-empty.
	BearerToken string
	// Store receives one recorded session per scenario; created when nil.
	Store *session.Store
	// OnEvent receives live progress; may be nil.
	OnEvent func(Event)
	// Chaos layers fault/latency injection over the run; nil disables it.
	Chaos *ChaosOptions
	// AllowRemote bypasses the chaos localhost guard. Without it, a chaos
	// run against a non-localhost target is refused.
	AllowRemote bool
}

// Run executes every scenario in pack against target, sequentially.
func Run(ctx context.Context, pack *Pack, target string, opts Options) (*Report, error) {
	if pack == nil {
		return nil, fmt.Errorf("scenarios: nil pack")
	}
	if strings.TrimSpace(target) == "" {
		return nil, fmt.Errorf("scenarios: target URL is required")
	}
	if opts.Chaos != nil && opts.Chaos.Enabled && !opts.AllowRemote && !isLocalhost(target) {
		return nil, fmt.Errorf("scenarios: refusing non-localhost target %q for chaos run (pass AllowRemote to override)", target)
	}
	store := opts.Store
	if store == nil {
		store = session.NewStore()
	}
	chaos := newChaosInjector(opts.Chaos)
	rep := &Report{
		Pack:      pack.Name,
		TargetURL: target,
		StartedAt: time.Now().UTC(),
	}
	for i := range pack.Scenarios {
		sr := runScenario(ctx, &pack.Scenarios[i], target, store, opts, chaos, func(e Event) {
			e.Pack = pack.Name
			if opts.OnEvent != nil {
				opts.OnEvent(e)
			}
		})
		rep.Scenarios = append(rep.Scenarios, *sr)
		if sr.Passed {
			rep.Summary.Passed++
		} else {
			rep.Summary.Failed++
		}
		rep.Summary.Total++
	}
	if chaos != nil {
		survived, total := 0, 0
		for i := range rep.Scenarios {
			for j := range rep.Scenarios[i].Steps {
				st := &rep.Scenarios[i].Steps[j]
				total++
				if st.Chaos != nil && st.Passed {
					survived++
				}
			}
		}
		rep.Chaos = chaos.summary(survived, total)
	}
	return rep, nil
}

// runScenario executes one scenario: setup hooks, steps, teardown hooks.
func runScenario(ctx context.Context, sc *Scenario, target string, store *session.Store, opts Options, chaos *chaosInjector, emit func(Event)) *ScenarioResult {
	sess := store.New(target)
	vars := map[string]any{}
	sr := &ScenarioResult{Name: sc.Name, SessionID: sess.ID, Passed: true}

	emit(Event{Type: EventScenarioStarted, Scenario: sc.Name})

	client := &mcpclient.Client{Endpoint: target, Timeout: opts.Timeout, BearerToken: opts.BearerToken}

	// Setup hooks: failures abort the scenario.
	for _, c := range sc.Setup {
		if err := execHook(ctx, client, sess, c, vars, chaos); err != nil {
			sr.SetupError = err.Error()
			sr.Passed = false
			finishScenario(sr, emit)
			return sr
		}
	}

	for i := range sc.Steps {
		st := &sc.Steps[i]
		emit(Event{Type: EventStepStarted, Scenario: sc.Name, Step: stepName(st, i), StepIndex: i})
		res := execStep(ctx, client, sess, st, i, vars, opts, chaos)
		sr.Steps = append(sr.Steps, *res)
		if !res.Passed {
			sr.Passed = false
		}
		emit(Event{Type: EventStepFinished, Scenario: sc.Name, Step: stepName(st, i), StepIndex: i, StepResult: res})
	}

	// Teardown hooks: best-effort cleanup. A hook whose arguments still
	// reference unset variables (e.g. the scenario failed before the
	// save step) is skipped rather than sent with a literal
	// "{{vars.*}}" placeholder.
	for _, c := range sc.Teardown {
		if hasUnresolvedVars(c.Arguments, vars) {
			continue
		}
		if err := execHook(ctx, client, sess, c, vars, chaos); err != nil {
			sr.TeardownError = err.Error()
			break
		}
	}

	finishScenario(sr, emit)
	return sr
}

// hasUnresolvedVars reports whether any string in args references a
// variable that was never saved.
func hasUnresolvedVars(args map[string]any, vars map[string]any) bool {
	found := false
	var walk func(v any)
	walk = func(v any) {
		if found {
			return
		}
		switch t := v.(type) {
		case string:
			for _, m := range varRefRe.FindAllStringSubmatch(t, -1) {
				if _, ok := vars[m[1]]; !ok {
					found = true
					return
				}
			}
		case map[string]any:
			for _, item := range t {
				walk(item)
			}
		case []any:
			for _, item := range t {
				walk(item)
			}
		}
	}
	walk(args)
	return found
}

func finishScenario(sr *ScenarioResult, emit func(Event)) {
	emit(Event{Type: EventScenarioFinished, Scenario: sr.Name, ScenarioResult: sr})
}

func stepName(st *Step, i int) string {
	if st.Name != "" {
		return st.Name
	}
	return fmt.Sprintf("step %d (%s)", i+1, st.Tool)
}

// execHook runs a setup/teardown call without assertions.
func execHook(ctx context.Context, client *mcpclient.Client, sess *session.Session, c Call, vars map[string]any, chaos *chaosInjector) error {
	args := renderArgs(c.Arguments, vars)
	out, in := recordExchange(sess, c.Tool, args)
	if chaos != nil {
		if mark := chaos.beforeCall(ctx); mark != nil && mark.Injected == "dropped-call" {
			sess.Append(session.NewErrorStep("tools/call", c.Tool, in.RequestID, mark.Detail, 0))
			_ = out
			return fmt.Errorf("hook %s: %s", c.Tool, mark.Detail)
		}
	}
	start := time.Now()
	res, err := client.CallTool(ctx, c.Tool, args)
	dur := time.Since(start)
	if err != nil {
		sess.Append(session.NewErrorStep("tools/call", c.Tool, in.RequestID, err.Error(), dur))
		_ = out
		return fmt.Errorf("hook %s: %w", c.Tool, err)
	}
	sess.Append(session.NewInStep("tools/call", c.Tool, in.RequestID, res.Raw, dur))
	extractVars(vars, c.Save, res.Raw)
	return nil
}

// execStep runs one asserted step and evaluates its assertions.
func execStep(ctx context.Context, client *mcpclient.Client, sess *session.Session, st *Step, idx int, vars map[string]any, opts Options, chaos *chaosInjector) *StepResult {
	res := &StepResult{Name: stepName(st, idx), Tool: st.Tool, Passed: true}
	args := renderArgs(st.Arguments, vars)
	_, in := recordExchange(sess, st.Tool, args)

	// Chaos injection happens before the call: a spike sleeps (recorded
	// on the step), a dropped call fails the step without sending.
	if chaos != nil {
		if mark := chaos.beforeCall(ctx); mark != nil {
			res.Chaos = mark
			if mark.Injected == "dropped-call" {
				sess.Append(session.NewErrorStep("tools/call", st.Tool, in.RequestID, mark.Detail, 0))
				res.Error = mark.Detail
				evalAll(res, st.Assertions, nil, markErr(mark.Detail), 0, vars)
				return res
			}
		}
	}

	start := time.Now()
	callRes, err := client.CallTool(ctx, st.Tool, args)
	dur := time.Since(start)
	res.LatencyMs = float64(dur) / float64(time.Millisecond)

	if err != nil {
		sess.Append(session.NewErrorStep("tools/call", st.Tool, in.RequestID, err.Error(), dur))
		res.Error = err.Error()
	} else {
		sess.Append(session.NewInStep("tools/call", st.Tool, in.RequestID, callRes.Raw, dur))
		extractVars(vars, st.Save, callRes.Raw)
	}

	evalAll(res, st.Assertions, callRes, err, res.LatencyMs, vars)
	return res
}

// evalAll evaluates every assertion against a step outcome.
func evalAll(res *StepResult, assertions []Assertion, callRes *mcpclient.CallResult, err error, latencyMs float64, vars map[string]any) {
	for _, a := range assertions {
		ar := evalAssertion(a, callRes, err, latencyMs, vars)
		res.Assertions = append(res.Assertions, ar)
		if !ar.Passed {
			res.Passed = false
		}
	}
}

// markErr builds an error for a chaos-dropped call.
func markErr(detail string) error {
	return fmt.Errorf("%s", detail)
}

// recordExchange appends the out step for a call and returns it plus a
// prepared in-step request id. The matching in step is appended by the
// caller once the outcome is known.
func recordExchange(sess *session.Session, tool string, args map[string]any) (out *session.Step, in *session.Step) {
	reqBody, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "tools/call",
		"params":  map[string]any{"name": tool, "arguments": args},
	})
	reqID := fmt.Sprintf("scn-%d", time.Now().UnixNano())
	out = session.NewOutStep("tools/call", tool, reqID, reqBody, nil)
	sess.Append(out)
	in = &session.Step{RequestID: reqID}
	return out, in
}

// ---------------------------------------------------------------------------
// Variables: {{vars.name}}
// ---------------------------------------------------------------------------

var varRefRe = regexp.MustCompile(`\{\{\s*vars\.([A-Za-z0-9_]+)\s*\}\}`)

// renderArgs substitutes {{vars.name}} references in arguments. A string
// that is exactly one reference keeps the variable's JSON type; embedded
// references are stringified.
func renderArgs(args map[string]any, vars map[string]any) map[string]any {
	if args == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(args))
	for k, v := range args {
		out[k] = renderValue(v, vars)
	}
	return out
}

// asMap converts decoded maps to map[string]any regardless of whether
// the decoder produced the named Assertion type, a plain map, or a
// map[any]any.
func asMap(v any) (map[string]any, bool) {
	switch t := v.(type) {
	case map[string]any:
		return t, true
	case Assertion:
		return map[string]any(t), true
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, item := range t {
			ks, ok := k.(string)
			if !ok {
				return nil, false
			}
			out[ks] = item
		}
		return out, true
	}
	return nil, false
}

func renderValue(v any, vars map[string]any) any {
	switch t := v.(type) {
	case string:
		if m := varRefRe.FindStringSubmatch(t); m != nil && strings.TrimSpace(t) == m[0] {
			if val, ok := vars[m[1]]; ok {
				return val
			}
			return t // unknown var: leave as-is; the call will likely fail loudly
		}
		return varRefRe.ReplaceAllStringFunc(t, func(match string) string {
			name := varRefRe.FindStringSubmatch(match)[1]
			if val, ok := vars[name]; ok {
				return stringify(val)
			}
			return match
		})
	case map[string]any, Assertion, map[any]any:
		m, _ := asMap(v)
		out := make(map[string]any, len(m))
		for k, item := range m {
			out[k] = renderValue(item, vars)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = renderValue(item, vars)
		}
		return out
	default:
		return v
	}
}

func stringify(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(b)
	}
}

// extractVars saves values from a result into vars. Each save entry maps
// a variable name to a dotted path into the decoded result object
// (e.g. "media_buy_id" or "creative.status").
func extractVars(vars map[string]any, save map[string]string, raw json.RawMessage) {
	if len(save) == 0 || len(raw) == 0 {
		return
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return
	}
	for name, path := range save {
		if v, ok := lookupPath(decoded, strings.Split(path, ".")); ok {
			vars[name] = v
		}
	}
}

// lookupPath walks decoded JSON along a dotted path. Numeric segments
// index into arrays.
func lookupPath(v any, parts []string) (any, bool) {
	cur := v
	for _, p := range parts {
		m, ok := asMap(cur)
		if !ok {
			// Arrays are indexed by numeric segments.
			if arr, ok := cur.([]any); ok {
				i, err := strconv.Atoi(p)
				if err != nil || i < 0 || i >= len(arr) {
					return nil, false
				}
				cur = arr[i]
				continue
			}
			return nil, false
		}
		next, ok := m[p]
		if !ok {
			return nil, false
		}
		cur = next
	}
	return cur, true
}

// ---------------------------------------------------------------------------
// Assertion evaluation
// ---------------------------------------------------------------------------

// evalAssertion checks one assertion against a step outcome.
func evalAssertion(a Assertion, res *mcpclient.CallResult, callErr error, latencyMs float64, vars map[string]any) AssertionResult {
	ar := AssertionResult{Kind: a.Kind(), Passed: false}
	switch a.Kind() {
	case AssertStatus:
		want, _ := a.Value().(string)
		failed := callErr != nil || (res != nil && res.IsError)
		switch want {
		case StatusOK:
			ar.Passed = !failed
			if !ar.Passed {
				ar.Detail = describeFailure(res, callErr)
			}
		case StatusError:
			ar.Passed = failed
			if !ar.Passed {
				ar.Detail = "expected an error but the call succeeded"
			}
		default:
			ar.Detail = fmt.Sprintf("unknown status value %q (want ok|error)", want)
		}
	case AssertErrorCode:
		want := toFloat(a.Value())
		var rpcErr *mcpclient.Error
		if errors.As(callErr, &rpcErr) && rpcErr.RPC != nil && float64(rpcErr.RPC.Code) == want {
			ar.Passed = true
		} else {
			ar.Detail = fmt.Sprintf("expected JSON-RPC error code %v, got %s", a.Value(), describeFailure(res, callErr))
		}
	case AssertLatencyMsLt:
		limit := toFloat(a.Value())
		ar.Passed = latencyMs < limit
		if !ar.Passed {
			ar.Detail = fmt.Sprintf("latency %.1f ms >= limit %.1f ms", latencyMs, limit)
		}
	case AssertResponseContain:
		expected, ok := asMap(a.Value())
		if !ok {
			ar.Detail = "response_contains needs a mapping"
			break
		}
		if callErr != nil || res == nil {
			ar.Detail = fmt.Sprintf("no response to inspect: %s", describeFailure(res, callErr))
			break
		}
		var actual any
		if err := json.Unmarshal(res.Raw, &actual); err != nil {
			ar.Detail = fmt.Sprintf("response is not JSON: %v", err)
			break
		}
		expected = renderExpected(expected, vars)
		if problem := subsetProblem(renderToJSON(expected), actual, "$"); problem != "" {
			ar.Passed = false
			ar.Detail = problem
		} else {
			ar.Passed = true
		}
	default:
		ar.Detail = fmt.Sprintf("unknown assertion kind %q", a.Kind())
	}
	return ar
}

// renderExpected applies {{vars.*}} substitution to expected assertion
// values (strings only).
func renderExpected(m map[string]any, vars map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = renderValue(v, vars)
	}
	return out
}

// renderToJSON round-trips v through JSON so YAML-decoded numbers
// compare equal to JSON-decoded numbers.
func renderToJSON(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return v
	}
	return out
}

func describeFailure(res *mcpclient.CallResult, err error) string {
	if err != nil {
		return err.Error()
	}
	if res != nil && res.IsError {
		return "tool returned isError"
	}
	return "no error"
}

// subsetProblem returns "" when expected is a subset of actual, else a
// human-readable mismatch at the given JSON path.
func subsetProblem(expected, actual any, path string) string {
	if exp, ok := asMap(expected); ok {
		act, ok := asMap(actual)
		if !ok {
			return fmt.Sprintf("%s: expected object, got %s", path, kindOf(actual))
		}
		for k, ev := range exp {
			av, ok := act[k]
			if !ok {
				return fmt.Sprintf("%s.%s: missing (expected %s)", path, k, shortJSON(ev))
			}
			if p := subsetProblem(ev, av, path+"."+k); p != "" {
				return p
			}
		}
		return ""
	}
	switch exp := expected.(type) {
	case []any:
		act, ok := actual.([]any)
		if !ok {
			return fmt.Sprintf("%s: expected array, got %s", path, kindOf(actual))
		}
		for i, ev := range exp {
			matched := false
			for _, av := range act {
				if subsetProblem(ev, av, path) == "" {
					matched = true
					break
				}
			}
			if !matched {
				return fmt.Sprintf("%s[%d]: no element matches %s", path, i, shortJSON(ev))
			}
		}
		return ""
	default:
		if !scalarEqual(expected, actual) {
			return fmt.Sprintf("%s: expected %s, got %s", path, shortJSON(expected), shortJSON(actual))
		}
		return ""
	}
}

// scalarEqual compares JSON scalars with numeric normalization.
func scalarEqual(a, b any) bool {
	af, aok := toFloatOK(a)
	bf, bok := toFloatOK(b)
	if aok && bok {
		return af == bf
	}
	return reflect.DeepEqual(a, b)
}

func toFloatOK(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	}
	return 0, false
}

func toFloat(v any) float64 {
	f, _ := toFloatOK(v)
	return f
}

func kindOf(v any) string {
	if v == nil {
		return "null"
	}
	switch v.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case bool:
		return "boolean"
	default:
		if _, ok := toFloatOK(v); ok {
			return "number"
		}
		return fmt.Sprintf("%T", v)
	}
}

func shortJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	if len(b) > 120 {
		return string(b[:117]) + "..."
	}
	return string(b)
}
