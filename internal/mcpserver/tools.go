package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sujanchalla0510/adcp-test/internal/conformance"
	"github.com/sujanchalla0510/adcp-test/internal/lifecycle"
	"github.com/sujanchalla0510/adcp-test/internal/scenarios"
	"github.com/sujanchalla0510/adcp-test/internal/signdebug"
)

// toolTimeouts bounds each tool's underlying run (the server-wide
// maxToolTimeout is the outer guard).
var toolTimeouts = map[string]time.Duration{
	"run_conformance": 3 * time.Minute,
	"list_scenarios":  30 * time.Second,
	"run_scenario":    10 * time.Minute,
	"debug_signature": 30 * time.Second,
	"lifecycle_check": 5 * time.Minute,
}

// allTools is the tool surface in presentation order.
var allTools = []tool{
	{
		Name:        "run_conformance",
		Description: "Run the 24-check AdCP conformance suite against a seller agent's MCP endpoint. Returns a pass/fail summary plus the full JSON report.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"target":       map[string]any{"type": "string", "description": "Seller MCP endpoint URL (required)"},
				"bearer_token": map[string]any{"type": "string", "description": "Bearer token for the target (optional)"},
				"profile":      map[string]any{"type": "string", "description": "Tool-surface profile: full (default), media-buy, creative, or signals (optional)"},
			},
			"required": []string{"target"},
		},
		Handler: handleRunConformance,
	},
	{
		Name:        "list_scenarios",
		Description: "List the built-in scenario packs (id, name, description, scenario names).",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		Handler:     handleListScenarios,
	},
	{
		Name:        "run_scenario",
		Description: "Run a scenario pack against a seller agent's MCP endpoint. Returns a pass/fail summary plus the full JSON report. Chaos mode only runs against localhost targets.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pack":         map[string]any{"type": "string", "description": "Built-in pack id or path to a pack YAML file (required)"},
				"target":       map[string]any{"type": "string", "description": "Seller MCP endpoint URL (required)"},
				"bearer_token": map[string]any{"type": "string", "description": "Bearer token for the target (optional)"},
				"chaos":        map[string]any{"type": "boolean", "description": "Inject random faults/latency spikes (localhost targets only)"},
			},
			"required": []string{"pack", "target"},
		},
		Handler: handleRunScenario,
	},
	{
		Name:        "debug_signature",
		Description: "Verify an RFC 9421 HTTP message signature offline and reconstruct the signature base. Returns the verdict plus a component-by-component diff. Key material is held in memory only.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"request":       map[string]any{"type": "object", "description": "The signed request: {method, url, headers, header_instances?, body} (required). header_instances is an ordered list of [name, value] pairs for repeated headers (e.g. multi-instance fields with ;bs)."},
				"key_pem":       map[string]any{"type": "string", "description": "PEM-encoded public or private key (required unless jwks_url is set)"},
				"jwks_url":      map[string]any{"type": "string", "description": "https JWKS URL: resolve the signature's keyid to a public key via the JWKS (selects the JWK whose kid matches) instead of using key_pem (optional)"},
				"expected_base": map[string]any{"type": "string", "description": "Signer-computed signature base to diff against (optional)"},
			},
			"required": []string{"request"},
		},
		Handler: handleDebugSignature,
	},
	{
		Name:        "lifecycle_check",
		Description: "Walk one media buy through create -> activate -> pause -> resume -> cancel on a seller agent, then verify the illegal cancelled -> active transition is rejected. Returns a pass/fail summary plus the full JSON report.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"target":       map[string]any{"type": "string", "description": "Seller MCP endpoint URL (required)"},
				"bearer_token": map[string]any{"type": "string", "description": "Bearer token for the target (optional)"},
			},
			"required": []string{"target"},
		},
		Handler: handleLifecycleCheck,
	},
}

// toolByName indexes allTools by name.
func toolByName(name string) *tool {
	for i := range allTools {
		if allTools[i].Name == name {
			return &allTools[i]
		}
	}
	return nil
}

// listTools returns the MCP tools/list payload entries.
func listTools() []map[string]any {
	out := make([]map[string]any, 0, len(allTools))
	for _, t := range allTools {
		out = append(out, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": t.InputSchema,
		})
	}
	return out
}

// toolArgs decodes the arguments object into v.
func toolArgs(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	return nil
}

// reportEnvelope wraps a tool's summary and full report so agents get both
// the headline and the machine-readable detail.
type reportEnvelope struct {
	Summary string `json:"summary"`
	Passed  bool   `json:"passed"`
	Report  any    `json:"report"`
}

// handleRunConformance implements the run_conformance tool.
func handleRunConformance(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		Target      string `json:"target"`
		BearerToken string `json:"bearer_token"`
		Profile     string `json:"profile"`
	}
	if err := toolArgs(raw, &args); err != nil {
		return nil, err
	}
	if args.Target == "" {
		return nil, fmt.Errorf("target is required")
	}
	tctx, cancel := context.WithTimeout(ctx, toolTimeouts["run_conformance"])
	defer cancel()
	rep, err := conformance.Run(tctx, args.Target, conformance.Options{BearerToken: args.BearerToken, Profile: args.Profile})
	if err != nil {
		return nil, err
	}
	return reportEnvelope{
		Summary: fmt.Sprintf("conformance: %d passed, %d failed, %d skipped of %d checks against %s",
			rep.Summary.Passed, rep.Summary.Failed, rep.Summary.Skipped, rep.Summary.Total, rep.TargetURL),
		Passed: rep.AllPassed(),
		Report: rep,
	}, nil
}

// handleListScenarios implements the list_scenarios tool.
func handleListScenarios(ctx context.Context, raw json.RawMessage) (any, error) {
	metas, err := scenarios.BuiltinPacks()
	if err != nil {
		return nil, err
	}
	return map[string]any{"packs": metas}, nil
}

// handleRunScenario implements the run_scenario tool.
func handleRunScenario(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		Pack        string `json:"pack"`
		Target      string `json:"target"`
		BearerToken string `json:"bearer_token"`
		Chaos       bool   `json:"chaos"`
	}
	if err := toolArgs(raw, &args); err != nil {
		return nil, err
	}
	if args.Pack == "" || args.Target == "" {
		return nil, fmt.Errorf("pack and target are required")
	}
	pack, err := scenarios.ResolvePack(args.Pack)
	if err != nil {
		return nil, err
	}
	var chaosOpts *scenarios.ChaosOptions
	if args.Chaos {
		chaosOpts = &scenarios.ChaosOptions{Enabled: true}
	}
	tctx, cancel := context.WithTimeout(ctx, toolTimeouts["run_scenario"])
	defer cancel()
	rep, err := scenarios.Run(tctx, pack, args.Target, scenarios.Options{
		BearerToken: args.BearerToken,
		Chaos:       chaosOpts,
	})
	if err != nil {
		return nil, err
	}
	return reportEnvelope{
		Summary: fmt.Sprintf("scenario pack %q: passed=%v against %s", pack.Name, rep.AllPassed(), args.Target),
		Passed:  rep.AllPassed(),
		Report:  rep,
	}, nil
}

// debugSignatureArgs are the debug_signature tool arguments.
type debugSignatureArgs struct {
	Request      signdebug.Request `json:"request"`
	KeyPEM       string            `json:"key_pem"`
	ExpectedBase string            `json:"expected_base"`
	// JWKSURL, when set, resolves the signature's keyid via the JWKS at
	// this https URL instead of using key_pem.
	JWKSURL string `json:"jwks_url"`
}

// handleDebugSignature implements the debug_signature tool.
func handleDebugSignature(ctx context.Context, raw json.RawMessage) (any, error) {
	var args debugSignatureArgs
	if err := toolArgs(raw, &args); err != nil {
		return nil, err
	}
	if args.Request.Method == "" || args.Request.URL == "" {
		return nil, fmt.Errorf("request.method and request.url are required")
	}
	if args.KeyPEM == "" && args.JWKSURL == "" {
		return nil, fmt.Errorf("key_pem or jwks_url is required")
	}
	rep := signdebug.Verify(&args.Request, []byte(args.KeyPEM),
		signdebug.Options{ExpectedBase: args.ExpectedBase, JWKSURL: args.JWKSURL})
	return reportEnvelope{
		Summary: fmt.Sprintf("signature verification: %s (%s)", rep.Verdict, rep.Summary),
		Passed:  rep.Verdict == "valid",
		Report:  rep,
	}, nil
}

// handleLifecycleCheck implements the lifecycle_check tool.
func handleLifecycleCheck(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		Target      string `json:"target"`
		BearerToken string `json:"bearer_token"`
	}
	if err := toolArgs(raw, &args); err != nil {
		return nil, err
	}
	if args.Target == "" {
		return nil, fmt.Errorf("target is required")
	}
	tctx, cancel := context.WithTimeout(ctx, toolTimeouts["lifecycle_check"])
	defer cancel()
	rep, err := lifecycle.Run(tctx, args.Target, lifecycle.Options{BearerToken: args.BearerToken})
	if err != nil {
		return nil, err
	}
	return reportEnvelope{
		Summary: fmt.Sprintf("lifecycle: passed=%v against %s", rep.AllPassed(), args.Target),
		Passed:  rep.AllPassed(),
		Report:  rep,
	}, nil
}
