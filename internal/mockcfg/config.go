// Package mockcfg is the YAML config model for config-driven mock seller
// services (M4). It parses, validates, and serializes mock configs:
//
//	mocks:
//	  - name: seller
//	    protocol: adcp
//	    listen: :8080
//	    routes:
//	      - match: { tool: get_products }
//	        respond: { file: products.json }        # or inline JSON, sequence:, template:
//	        latency: { p50: 40ms, p99: 250ms }      # or fixed:, or spike:
//	        faults: { error_rate: 0.01, timeout_rate: 0.005, malformed_rate: 0.0 }
//	      - match: { tool: create_media_buy, args: { buyer_ref: "acme*" } }
//	        respond: { template: media-buy.json }   # {{args.path}} placeholders
//	        state_machine: media-buy-lifecycle
//	    record:                                     # proxy mode
//	      upstream: https://real-seller.example/mcp
//	      capture_to: integration.yaml
//
// The singular "mock:" top-level key is accepted as an alias for a
// single-element "mocks:" list, so the design-doc shape parses as-is.
// Compose (several mock services in one file, one process) uses "mocks:".
//
// Response modes are exclusive; a bare mapping under "respond:" is
// treated as an inline JSON body:
//
//	respond: { products: [...] }          # inline body
//	respond: { inline: { products: [...] } }  # same thing, explicit
//	respond: { file: products.json }      # JSON body read from a file
//	respond: { sequence: [a.json, b.json] }   # nth call -> nth response
//	respond: { template: media-buy.json }    # {{args.*}} placeholders
//	respond: { error: { code: -32001, message: rejected } }  # JSON-RPC error
package mockcfg

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Known state machines the mock engine can enforce.
const StateMachineMediaBuyLifecycle = "media-buy-lifecycle"

// Config is the top-level mock configuration document.
type Config struct {
	Mock  *MockService  `yaml:"mock,omitempty" json:"mock,omitempty"`
	Mocks []MockService `yaml:"mocks,omitempty" json:"mocks,omitempty"`

	baseDir string // directory of the file this was loaded from; "" = process CWD
}

// MockService is one mock seller (or signals provider): a listen address
// plus the routes it serves.
type MockService struct {
	Name     string  `yaml:"name" json:"name"`
	Protocol string  `yaml:"protocol,omitempty" json:"protocol,omitempty"`
	Listen   string  `yaml:"listen" json:"listen"`
	Routes   []Route `yaml:"routes,omitempty" json:"routes,omitempty"`
	Record   *Record `yaml:"record,omitempty" json:"record,omitempty"`
}

// Route matches incoming tools/call requests and describes the response.
type Route struct {
	Match        Match    `yaml:"match" json:"match"`
	Respond      Respond  `yaml:"respond" json:"respond"`
	Latency      *Latency `yaml:"latency,omitempty" json:"latency,omitempty"`
	Faults       *Faults  `yaml:"faults,omitempty" json:"faults,omitempty"`
	StateMachine string   `yaml:"state_machine,omitempty" json:"state_machine,omitempty"`
	// InputSchema is the JSON schema advertised for the route's tool in
	// tools/list (e.g. {"type":"object","required":["brief"]}). When
	// unset, the engine advertises a bare {"type":"object"} schema.
	InputSchema any `yaml:"input_schema,omitempty" json:"input_schema,omitempty"`
}

// Match selects requests: exact tool name plus argument patterns. String
// patterns support glob wildcards ("acme*", "prod-???"); nested maps match
// nested argument objects; dotted keys ("creative.id") address nested
// paths.
type Match struct {
	Tool string         `yaml:"tool" json:"tool"`
	Args map[string]any `yaml:"args,omitempty" json:"args,omitempty"`
}

// Respond describes the JSON-RPC result payload for a matched request.
// Exactly one mode is active. Custom unmarshal/marshal keeps the YAML
// round-trip stable (a bare mapping stays a bare mapping).
type Respond struct {
	Inline   any
	File     string
	Sequence []SequenceItem
	Template string
	Error    *ErrorResponse

	inlineSet bool // the explicit "inline:" key was used
}

// ErrorResponse is a structured JSON-RPC error the mock returns instead
// of a result payload. It lets a mock seller simulate rejections —
// unsigned mutating calls, invalid arguments, illegal state moves — the
// way a real seller would.
type ErrorResponse struct {
	Code    int    `yaml:"code" json:"code"`
	Message string `yaml:"message" json:"message"`
	Data    any    `yaml:"data,omitempty" json:"data,omitempty"`
}

// SequenceItem is one step of a sequenced response: a file path (bare
// string) or an inline payload.
type SequenceItem struct {
	File   string
	Inline any

	inlineSet bool
}

// Latency describes the artificial delay profile for a route: exactly one
// of fixed or a p50/p99 distribution, optionally with periodic spikes.
type Latency struct {
	Fixed string        `yaml:"fixed,omitempty" json:"fixed,omitempty"`
	P50   string        `yaml:"p50,omitempty" json:"p50,omitempty"`
	P99   string        `yaml:"p99,omitempty" json:"p99,omitempty"`
	Spike *SpikeProfile `yaml:"spike,omitempty" json:"spike,omitempty"`
}

// SpikeProfile adds extra latency during periodic spike windows.
type SpikeProfile struct {
	Every string `yaml:"every" json:"every"`
	For   string `yaml:"for" json:"for"`
	Add   string `yaml:"add" json:"add"`
}

// Faults are per-route fault-injection probabilities in [0,1].
type Faults struct {
	ErrorRate     float64 `yaml:"error_rate" json:"error_rate"`
	TimeoutRate   float64 `yaml:"timeout_rate" json:"timeout_rate"`
	MalformedRate float64 `yaml:"malformed_rate" json:"malformed_rate"`
}

// Record configures proxy mode: forward to a real upstream seller and
// capture the traffic so a mock config can be generated from it.
type Record struct {
	Upstream  string `yaml:"upstream" json:"upstream"`
	CaptureTo string `yaml:"capture_to" json:"capture_to"`
}

// ---------------------------------------------------------------------------
// Parsing / serialization
// ---------------------------------------------------------------------------

// Parse parses YAML config bytes. Unknown fields are rejected so typos in
// hand-written configs fail loudly.
func Parse(data []byte) (*Config, error) {
	c := &Config{}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil {
		return nil, fmt.Errorf("mockcfg: parse: %w", err)
	}
	return c, nil
}

// LoadFile reads path, parses it, and remembers its directory so relative
// response-file paths resolve against it.
func LoadFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("mockcfg: read %s: %w", path, err)
	}
	c, err := Parse(data)
	if err != nil {
		return nil, err
	}
	if dir := filepath.Dir(path); dir != "" {
		c.baseDir = dir
	}
	return c, nil
}

// Marshal serializes the config back to YAML. Parse -> Marshal ->
// Parse is stable (canonical form).
func (c *Config) Marshal() ([]byte, error) {
	out, err := yaml.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("mockcfg: marshal: %w", err)
	}
	return out, nil
}

// BaseDir is the directory relative response-file paths resolve against.
func (c *Config) BaseDir() string {
	if c.baseDir == "" {
		return "."
	}
	return c.baseDir
}

// Resolve resolves p against the config's base directory.
func (c *Config) Resolve(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(c.BaseDir(), p)
}

// Services returns the normalized service list: "mocks:", or the
// singular "mock:" alias wrapped in a one-element list.
func (c *Config) Services() []MockService {
	if len(c.Mocks) > 0 {
		return c.Mocks
	}
	if c.Mock != nil {
		return []MockService{*c.Mock}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Respond (un)marshaling
// ---------------------------------------------------------------------------

var respondModeKeys = []string{"file", "sequence", "template", "inline", "error"}

// UnmarshalYAML implements yaml.Unmarshaler.
func (r *Respond) UnmarshalYAML(value *yaml.Node) error {
	var m map[string]any
	if err := value.Decode(&m); err != nil {
		return fmt.Errorf("respond: expected a mapping: %w", err)
	}
	rr, err := respondFromMap(m)
	if err != nil {
		return err
	}
	*r = rr
	return nil
}

// MarshalYAML implements yaml.Marshaler.
func (r Respond) MarshalYAML() (any, error) {
	return r.toMap(), nil
}

// UnmarshalJSON mirrors the YAML mapping semantics so the visual editor
// can round-trip its model through JSON.
func (r *Respond) UnmarshalJSON(data []byte) error {
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("respond: expected an object: %w", err)
	}
	rr, err := respondFromMap(m)
	if err != nil {
		return err
	}
	*r = rr
	return nil
}

// MarshalJSON mirrors MarshalYAML.
func (r Respond) MarshalJSON() ([]byte, error) {
	return json.Marshal(r.toMap())
}

func respondFromMap(m map[string]any) (Respond, error) {
	var seen []string
	for _, k := range respondModeKeys {
		if _, ok := m[k]; ok {
			seen = append(seen, k)
		}
	}
	if len(seen) > 1 {
		return Respond{}, fmt.Errorf("respond: only one of file/sequence/template/inline/error allowed, got %s", strings.Join(seen, ", "))
	}
	if len(seen) == 0 {
		// A bare mapping is an inline JSON body.
		return Respond{Inline: m}, nil
	}
	switch seen[0] {
	case "file":
		s, ok := m["file"].(string)
		if !ok || strings.TrimSpace(s) == "" {
			return Respond{}, fmt.Errorf("respond: file must be a non-empty string")
		}
		return Respond{File: s}, nil
	case "template":
		s, ok := m["template"].(string)
		if !ok || strings.TrimSpace(s) == "" {
			return Respond{}, fmt.Errorf("respond: template must be a non-empty string")
		}
		return Respond{Template: s}, nil
	case "inline":
		return Respond{Inline: m["inline"], inlineSet: true}, nil
	case "sequence":
		lst, ok := m["sequence"].([]any)
		if !ok {
			return Respond{}, fmt.Errorf("respond: sequence must be a list")
		}
		if len(lst) == 0 {
			return Respond{}, fmt.Errorf("respond: sequence must not be empty")
		}
		items := make([]SequenceItem, 0, len(lst))
		for i, v := range lst {
			it, err := seqItemFromAny(v)
			if err != nil {
				return Respond{}, fmt.Errorf("respond: sequence[%d]: %w", i, err)
			}
			items = append(items, it)
		}
		return Respond{Sequence: items}, nil
	case "error":
		em, ok := m["error"].(map[string]any)
		if !ok {
			return Respond{}, fmt.Errorf("respond: error must be a mapping with code and message")
		}
		e := &ErrorResponse{}
		if c, ok := em["code"]; ok {
			f, ok := toFloatAny(c)
			if !ok {
				return Respond{}, fmt.Errorf("respond: error.code must be a number")
			}
			e.Code = int(f)
		} else {
			return Respond{}, fmt.Errorf("respond: error.code is required")
		}
		msg, ok := em["message"].(string)
		if !ok || strings.TrimSpace(msg) == "" {
			return Respond{}, fmt.Errorf("respond: error.message must be a non-empty string")
		}
		e.Message = msg
		if d, ok := em["data"]; ok {
			e.Data = d
		}
		return Respond{Error: e}, nil
	}
	return Respond{}, fmt.Errorf("respond: unreachable")
}

// toFloatAny converts YAML/JSON numbers to float64.
func toFloatAny(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case float64:
		return n, true
	case float32:
		return float64(n), true
	}
	return 0, false
}

func (r Respond) toMap() map[string]any {
	switch {
	case r.Error != nil:
		em := map[string]any{"code": r.Error.Code, "message": r.Error.Message}
		if r.Error.Data != nil {
			em["data"] = r.Error.Data
		}
		return map[string]any{"error": em}
	case r.File != "":
		return map[string]any{"file": r.File}
	case r.Template != "":
		return map[string]any{"template": r.Template}
	case len(r.Sequence) > 0:
		lst := make([]any, 0, len(r.Sequence))
		for _, it := range r.Sequence {
			lst = append(lst, it.toAny())
		}
		return map[string]any{"sequence": lst}
	case r.inlineSet:
		return map[string]any{"inline": r.Inline}
	case r.Inline != nil:
		if mm, ok := r.Inline.(map[string]any); ok {
			return mm
		}
		return map[string]any{"inline": r.Inline}
	default:
		return map[string]any{}
	}
}

// HasResponse reports whether any response mode is configured.
func (r Respond) HasResponse() bool {
	return r.File != "" || r.Template != "" || len(r.Sequence) > 0 || r.inlineSet || r.Inline != nil || r.Error != nil
}

func seqItemFromAny(v any) (SequenceItem, error) {
	switch t := v.(type) {
	case string:
		if strings.TrimSpace(t) == "" {
			return SequenceItem{}, fmt.Errorf("empty file path")
		}
		return SequenceItem{File: t}, nil
	case map[string]any:
		if f, ok := t["file"]; ok && len(t) == 1 {
			fs, ok := f.(string)
			if !ok || strings.TrimSpace(fs) == "" {
				return SequenceItem{}, fmt.Errorf("file must be a non-empty string")
			}
			return SequenceItem{File: fs}, nil
		}
		if in, ok := t["inline"]; ok && len(t) == 1 {
			return SequenceItem{Inline: in, inlineSet: true}, nil
		}
		return SequenceItem{Inline: t}, nil
	default:
		// A bare scalar (number, bool) is an inline payload.
		return SequenceItem{Inline: v, inlineSet: true}, nil
	}
}

func (it SequenceItem) toAny() any {
	switch {
	case it.File != "":
		return it.File
	case it.inlineSet:
		return map[string]any{"inline": it.Inline}
	case it.Inline != nil:
		if mm, ok := it.Inline.(map[string]any); ok {
			return mm
		}
		return map[string]any{"inline": it.Inline}
	default:
		return nil
	}
}

// ---------------------------------------------------------------------------
// Template placeholder syntax
// ---------------------------------------------------------------------------

// templateRoots are the allowed {{...}} placeholder roots.
var templateRoots = map[string]bool{
	"args": true, "tool": true, "id": true, "now": true,
	"call": true, "state": true, "entity_id": true,
}

var templatePathSegment = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// ValidateTemplateSyntax checks that every {{...}} placeholder in tmpl is
// well-formed and rooted at a known name. It does not require the
// referenced paths to exist.
func ValidateTemplateSyntax(tmpl string) error {
	rest := tmpl
	for {
		open := strings.Index(rest, "{{")
		if open < 0 {
			if strings.Contains(rest, "}}") {
				return fmt.Errorf("template: stray closing delimiter without opening")
			}
			return nil
		}
		if strings.Contains(rest[:open], "}}") {
			return fmt.Errorf("template: stray closing delimiter without opening")
		}
		after := rest[open+2:]
		close := strings.Index(after, "}}")
		if close < 0 {
			return fmt.Errorf("template: unclosed placeholder")
		}
		name := strings.TrimSpace(after[:close])
		if name == "" {
			return fmt.Errorf("template: empty placeholder")
		}
		parts := strings.Split(name, ".")
		if !templateRoots[parts[0]] {
			return fmt.Errorf("template: unknown placeholder root %q (want one of args, tool, id, now, call, state, entity_id)", parts[0])
		}
		for _, p := range parts {
			if !templatePathSegment.MatchString(p) {
				return fmt.Errorf("template: bad placeholder path segment %q in %q", p, name)
			}
		}
		if parts[0] == "args" && len(parts) < 2 {
			return fmt.Errorf("template: {{args}} needs a field path, e.g. {{args.buyer_ref}}")
		}
		rest = after[close+2:]
	}
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

// Validate checks the whole config and returns every problem found.
func (c *Config) Validate() error {
	var errs []error
	add := func(format string, a ...any) {
		errs = append(errs, fmt.Errorf(format, a...))
	}

	if c.Mock != nil && len(c.Mocks) > 0 {
		add("use either mock: or mocks:, not both")
	}
	svcs := c.Services()
	if len(svcs) == 0 {
		add("no mock services defined (need mock: or mocks:)")
	}
	seenListen := map[string]string{}
	for i, svc := range svcs {
		p := fmt.Sprintf("mocks[%d]", i)
		if strings.TrimSpace(svc.Name) == "" {
			add("%s: name is required", p)
		}
		if svc.Protocol != "" && svc.Protocol != "adcp" {
			add("%s: unsupported protocol %q (want adcp)", p, svc.Protocol)
		}
		if err := validateListen(svc.Listen); err != nil {
			add("%s: %v", p, err)
		} else if !isEphemeralListen(svc.Listen) {
			// Port 0 = OS-assigned ephemeral port: duplicates are fine.
			if prev, dup := seenListen[svc.Listen]; dup {
				add("%s: listen %q duplicates %s", p, svc.Listen, prev)
			} else {
				seenListen[svc.Listen] = p
			}
		}
		if svc.Record != nil {
			rp := p + ".record"
			if strings.TrimSpace(svc.Record.Upstream) == "" {
				add("%s: upstream is required", rp)
			} else if u, err := url.Parse(svc.Record.Upstream); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				add("%s: upstream %q is not a valid http(s) URL", rp, svc.Record.Upstream)
			}
			// capture_to is optional: without it the generated config is
			// returned in-memory (API) or printed to stdout (CLI).
		}
		for j, rt := range svc.Routes {
			validateRoute(p+fmt.Sprintf(".routes[%d]", j), rt, add)
		}
	}
	return errors.Join(errs...)
}

func validateListen(listen string) error {
	if strings.TrimSpace(listen) == "" {
		return fmt.Errorf("listen is required (e.g. \":8080\")")
	}
	host, portStr, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("listen %q: want host:port, e.g. \":8080\"", listen)
	}
	_ = host
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 0 || port > 65535 {
		return fmt.Errorf("listen %q: bad port", listen)
	}
	return nil
}

// isEphemeralListen reports whether listen uses port 0 (OS-assigned).
func isEphemeralListen(listen string) bool {
	_, portStr, err := net.SplitHostPort(listen)
	if err != nil {
		return false
	}
	return portStr == "0"
}

func validateRoute(p string, rt Route, add func(string, ...any)) {
	if strings.TrimSpace(rt.Match.Tool) == "" {
		add("%s: match.tool is required", p)
	}
	if !rt.Respond.HasResponse() {
		add("%s: respond needs one of file/sequence/template/inline/error (or a bare inline JSON mapping)", p)
	}
	if rt.Respond.Template != "" {
		if err := ValidateTemplateSyntax(rt.Respond.Template); err != nil {
			// The template key holds a file path, so only flag it when the
			// path itself looks like template source (contains braces).
			if strings.Contains(rt.Respond.Template, "{{") {
				add("%s: respond.template: %v", p, err)
			}
		}
	}
	for i, it := range rt.Respond.Sequence {
		if it.File == "" && it.Inline == nil && !it.inlineSet {
			add("%s: respond.sequence[%d]: empty entry", p, i)
		}
	}
	if rt.Latency != nil {
		validateLatency(p+".latency", rt.Latency, add)
	}
	if rt.Faults != nil {
		fp := p + ".faults"
		for name, v := range map[string]float64{
			"error_rate": rt.Faults.ErrorRate, "timeout_rate": rt.Faults.TimeoutRate, "malformed_rate": rt.Faults.MalformedRate,
		} {
			if v < 0 || v > 1 {
				add("%s: %s must be in [0,1], got %v", fp, name, v)
			}
		}
		if rt.Faults.ErrorRate+rt.Faults.TimeoutRate+rt.Faults.MalformedRate > 1 {
			add("%s: fault rates sum to more than 1", fp)
		}
	}
	if rt.StateMachine != "" && rt.StateMachine != StateMachineMediaBuyLifecycle {
		add("%s: unknown state_machine %q", p, rt.StateMachine)
	}
	if rt.InputSchema != nil {
		if _, ok := rt.InputSchema.(map[string]any); !ok {
			add("%s: input_schema must be a mapping (a JSON schema object)", p)
		}
	}
}

func validateLatency(p string, l *Latency, add func(string, ...any)) {
	parse := func(name, s string) (time.Duration, bool) {
		d, err := time.ParseDuration(s)
		if err != nil {
			add("%s: %s %q: %v", p, name, s, err)
			return 0, false
		}
		if d < 0 {
			add("%s: %s must not be negative", p, name)
			return 0, false
		}
		return d, true
	}
	if l.Fixed != "" {
		if l.P50 != "" || l.P99 != "" {
			add("%s: use either fixed: or p50:/p99:, not both", p)
		}
		parse("fixed", l.Fixed)
	} else if l.P50 != "" || l.P99 != "" {
		p50, ok50 := parse("p50", orDefault(l.P50, "0s"))
		p99, ok99 := parse("p99", orDefault(l.P99, "0s"))
		if ok50 && ok99 && l.P50 != "" && l.P99 != "" && p99 < p50 {
			add("%s: p99 (%s) must be >= p50 (%s)", p, l.P99, l.P50)
		}
		if l.P50 != "" && ok50 && p50 == 0 {
			add("%s: p50 must be positive", p)
		}
	} else if l.Spike == nil {
		add("%s: empty latency profile (need fixed:, p50:/p99:, or spike:)", p)
	}
	if l.Spike != nil {
		sp := l.Spike
		every, ok1 := parse("spike.every", sp.Every)
		for_, ok2 := parse("spike.for", sp.For)
		addD, ok3 := parse("spike.add", sp.Add)
		_ = addD
		if ok1 && every == 0 {
			add("%s: spike.every must be positive", p)
		}
		if ok2 && ok3 && for_ >= every {
			add("%s: spike.for (%s) must be shorter than spike.every (%s)", p, sp.For, sp.Every)
		}
	}
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
