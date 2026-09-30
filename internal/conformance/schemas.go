package conformance

import (
	"encoding/json"
	"strings"
)

var jsonUnmarshal = json.Unmarshal

// ExpectedTool describes one AdCP tool the conformance checker looks for.
//
// The profile system selects the required set per run, grounded in the
// AdCP spec's docs/protocol/required-tasks.mdx ("Required tasks by
// protocol"). Profiles:
//   - "full": historical strict behavior — every tool marked Required
//     below. A superset of the spec; use it for a complete seller.
//   - "media-buy": media-buy sales agent profile (get_adcp_capabilities
//   - get_products, create_media_buy, update_media_buy, get_media_buys,
//     get_media_buy_delivery, provide_performance_feedback).
//   - "creative": creative agent profile (get_adcp_capabilities;
//     list_transformers/list_creatives/sync_creatives are conditional on
//     capabilities, so their absence is informational).
//   - "signals": signal agent profile (get_adcp_capabilities,
//     get_signals, activate_signal).
type ExpectedTool struct {
	// Name is the MCP tool name.
	Name string
	// Required means a missing tool fails the tool-surface check under
	// the "full" profile (historical strict behavior).
	Required bool
	// ExpectedRequired lists input fields the tool's declared inputSchema
	// must require, taken from the AdCP 3.1 task schemas (e.g. the 3.1
	// get-products-request schema requires buying_mode; brief is
	// optional). Empty means "structural check only".
	ExpectedRequired []string
	// Profiles lists the agent profiles for which this tool is required.
	Profiles []string
	// Description is human context for reports.
	Description string
}

// CoreTools is the expected AdCP seller tool surface.
var CoreTools = []ExpectedTool{
	{Name: "get_adcp_capabilities", Required: true, Profiles: []string{"full", "media-buy", "creative", "signals"}, Description: "agent capabilities / supported protocols"},
	{Name: "get_products", Required: true, ExpectedRequired: []string{"buying_mode"}, Profiles: []string{"full", "media-buy"}, Description: "product discovery (brief is optional per the 3.1 schema; buying_mode is required)"},
	{Name: "list_creative_formats", Required: true, Profiles: []string{"full"}, Description: "creative format specifications"},
	{Name: "create_media_buy", Required: true, ExpectedRequired: []string{"idempotency_key", "account", "brand", "start_time", "end_time"}, Profiles: []string{"full", "media-buy"}, Description: "create a media buy (mutating)"},
	{Name: "update_media_buy", Required: true, Profiles: []string{"full", "media-buy"}, Description: "modify a media buy (mutating)"},
	{Name: "get_media_buys", Required: true, Profiles: []string{"full", "media-buy"}, Description: "retrieve media-buy state"},
	{Name: "sync_creatives", Required: true, Profiles: []string{"full"}, Description: "upload creative assets (mutating; conditional in the media-buy profile)"},
	{Name: "sync_catalogs", Required: true, Profiles: []string{"full"}, Description: "sync product catalog feeds (mutating)"},
	{Name: "list_creatives", Required: true, Profiles: []string{"full"}, Description: "query the creative library"},
	{Name: "get_media_buy_delivery", Required: true, Profiles: []string{"full", "media-buy"}, Description: "delivery / performance reporting"},
	{Name: "provide_performance_feedback", Required: true, Profiles: []string{"full", "media-buy"}, Description: "share outcomes back with the publisher"},
	// Optional: documented but protocol- or revision-specific.
	{Name: "get_signals", Required: false, Profiles: []string{"full", "signals"}, Description: "audience signals lookup (signals protocol)"},
	{Name: "activate_signal", Required: false, Profiles: []string{"full", "signals"}, Description: "activate audience signals (signals protocol)"},
	{Name: "list_authorized_properties", Required: false, Profiles: []string{"full"}, Description: "publisher properties (older capability alias)"},
	{Name: "sync_accounts", Required: false, Profiles: []string{"full"}, Description: "account onboarding"},
	{Name: "build_creative", Required: false, Profiles: []string{"full"}, Description: "AI creative builder"},
	{Name: "check_governance", Required: false, Profiles: []string{"full"}, Description: "governance checks"},
	{Name: "log_event", Required: false, Profiles: []string{"full"}, Description: "conversion event logging"},
}

// expectedToolByName indexes CoreTools by name.
func expectedToolByName() map[string]ExpectedTool {
	m := make(map[string]ExpectedTool, len(CoreTools))
	for _, t := range CoreTools {
		m[t.Name] = t
	}
	return m
}

// probeToolNames are the tools used for behavioral probes.
const (
	probeMutatingTool = "create_media_buy" // unsigned/malformed-signature probes
	probeReadTool     = "get_products"     // read-only behavior documentation
	probeUnknownTool  = "__adcp_test_no_such_tool__"
)

// validateInputSchema checks a tool's declared inputSchema against the
// embedded expectations: it must be a JSON object schema, and every field
// in expected.required must be declared (in required, or in properties
// when no required array exists). Returns human-readable problems; empty
// means the schema is acceptable.
func validateInputSchema(expected ExpectedTool, raw []byte) []string {
	var problems []string
	if len(raw) == 0 {
		// No schema declared: acceptable for tools with no required input,
		// a problem only when we expect required fields.
		if len(expected.ExpectedRequired) > 0 {
			problems = append(problems, "no inputSchema declared, expected required fields: "+joinQuoted(expected.ExpectedRequired))
		}
		return problems
	}
	var schema struct {
		Type       string         `json:"type"`
		Properties map[string]any `json:"properties"`
		Required   []string       `json:"required"`
		Raw        map[string]any `json:"-"`
	}
	var generic map[string]any
	if err := jsonUnmarshal(raw, &generic); err != nil {
		return []string{"inputSchema is not valid JSON: " + err.Error()}
	}
	_ = jsonUnmarshal(raw, &schema)
	if schema.Type != "" && schema.Type != "object" {
		problems = append(problems, "inputSchema type is "+quote(schema.Type)+", want \"object\"")
	}
	declared := map[string]bool{}
	for _, r := range schema.Required {
		declared[r] = true
	}
	for p := range schema.Properties {
		if _, ok := declared[p]; !ok && len(schema.Required) == 0 {
			// No required array at all: properties count as declared.
			declared[p] = true
		}
	}
	for _, want := range expected.ExpectedRequired {
		if !declared[want] {
			problems = append(problems, "inputSchema does not declare required field "+quote(want))
		}
	}
	return problems
}

func quote(s string) string { return `"` + s + `"` }

func joinQuoted(ss []string) string {
	q := make([]string, len(ss))
	for i, s := range ss {
		q[i] = quote(s)
	}
	return strings.Join(q, ", ")
}
