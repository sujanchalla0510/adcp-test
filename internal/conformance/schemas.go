package conformance

import (
	"encoding/json"
	"strings"
)

var jsonUnmarshal = json.Unmarshal

// ExpectedTool describes one AdCP tool the conformance checker looks for.
//
// The required set below is the documented AdCP media-buy protocol core
// (v3-era task list: get_adcp_capabilities, get_products,
// list_creative_formats, create_media_buy, update_media_buy, get_media_buys,
// sync_creatives, sync_catalogs, list_creatives, get_media_buy_delivery,
// provide_performance_feedback). Optional tools belong to sibling protocols
// (signals, accounts) or older revisions; their absence is informational,
// never a failure.
type ExpectedTool struct {
	// Name is the MCP tool name.
	Name string
	// Required means a missing tool fails the tool-surface check.
	Required bool
	// ExpectedRequired lists input fields the tool's declared inputSchema
	// must require. Kept minimal on purpose: only fields that are stable
	// across spec revisions are asserted. Empty means "structural check
	// only" (valid object schema).
	ExpectedRequired []string
	// Description is human context for reports.
	Description string
}

// CoreTools is the expected AdCP seller tool surface.
var CoreTools = []ExpectedTool{
	{Name: "get_adcp_capabilities", Required: true, Description: "agent capabilities / supported protocols"},
	{Name: "get_products", Required: true, ExpectedRequired: []string{"brief"}, Description: "product discovery via natural-language brief"},
	{Name: "list_creative_formats", Required: true, Description: "creative format specifications"},
	{Name: "create_media_buy", Required: true, ExpectedRequired: []string{"account", "brand", "start_time", "end_time"}, Description: "create a media buy (mutating)"},
	{Name: "update_media_buy", Required: true, Description: "modify a media buy (mutating)"},
	{Name: "get_media_buys", Required: true, Description: "retrieve media-buy state"},
	{Name: "sync_creatives", Required: true, Description: "upload creative assets (mutating)"},
	{Name: "sync_catalogs", Required: true, Description: "sync product catalog feeds (mutating)"},
	{Name: "list_creatives", Required: true, Description: "query the creative library"},
	{Name: "get_media_buy_delivery", Required: true, Description: "delivery / performance reporting"},
	{Name: "provide_performance_feedback", Required: true, Description: "share outcomes back with the publisher"},
	// Optional: documented but protocol- or revision-specific.
	{Name: "get_signals", Required: false, Description: "audience signals lookup (signals protocol)"},
	{Name: "activate_signal", Required: false, Description: "activate audience signals (signals protocol)"},
	{Name: "list_authorized_properties", Required: false, Description: "publisher properties (older capability alias)"},
	{Name: "sync_accounts", Required: false, Description: "account onboarding"},
	{Name: "build_creative", Required: false, Description: "AI creative builder"},
	{Name: "check_governance", Required: false, Description: "governance checks"},
	{Name: "log_event", Required: false, Description: "conversion event logging"},
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
