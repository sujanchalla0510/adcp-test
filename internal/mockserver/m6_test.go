package mockserver

import (
	"encoding/json"
	"testing"

	"github.com/sujanchalla0510/adcp-test/internal/mockcfg"
)

// TestErrorRespondMode verifies a route with respond.error answers with
// a structured JSON-RPC error instead of a result payload.
func TestErrorRespondMode(t *testing.T) {
	cfg, err := mockcfg.Parse([]byte(`
mocks:
  - name: seller
    listen: :0
    routes:
      - match: { tool: create_media_buy, args: { start_time: 12345 } }
        respond: { error: { code: -32001, message: "unsigned mutating call rejected", data: { reason: auth } } }
      - match: { tool: create_media_buy }
        respond: { inline: { media_buy_id: mb-1, status: draft } }
`))
	if err != nil {
		t.Fatal(err)
	}
	srv := startTestServer(t, cfg.Services()[0], Options{})

	// Probe-shaped args hit the error route.
	r := callTool(t, srv.URL(), "create_media_buy",
		map[string]any{"buyer_ref": "adcp-test-probe-INVALID", "start_time": 12345, "end_time": 12345})
	if r.Err == nil {
		t.Fatalf("expected RPC error, got result: %v", r.Result)
	}
	if code, _ := r.Err["code"].(float64); code != -32001 {
		t.Fatalf("expected code -32001, got %v", r.Err)
	}
	if r.Err["message"] != "unsigned mutating call rejected" {
		t.Fatalf("unexpected message: %v", r.Err["message"])
	}

	// Ordinary args fall through to the generic route.
	r2 := callTool(t, srv.URL(), "create_media_buy",
		map[string]any{"buyer_ref": "acme", "start_time": "2026-10-01T00:00:00Z"})
	mustNoError(t, r2)
}

// TestInputSchemaAdvertised verifies a route's input_schema is served in
// tools/list, and that routes without one keep the bare object schema.
func TestInputSchemaAdvertised(t *testing.T) {
	cfg, err := mockcfg.Parse([]byte(`
mocks:
  - name: seller
    listen: :0
    routes:
      - match: { tool: get_products }
        input_schema: { type: object, required: [brief], properties: { brief: { type: string } } }
        respond: { inline: { products: [] } }
      - match: { tool: get_media_buys }
        respond: { inline: { media_buys: [] } }
`))
	if err != nil {
		t.Fatal(err)
	}
	srv := startTestServer(t, cfg.Services()[0], Options{})

	r := call(t, srv.URL(), "tools/list", map[string]any{})
	mustNoError(t, r)
	tools, ok := r.Result.(map[string]any)["tools"].([]any)
	if !ok || len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %v", r.Result)
	}
	byName := map[string]map[string]any{}
	for _, to := range tools {
		m := to.(map[string]any)
		byName[m["name"].(string)] = m
	}
	schema, ok := byName["get_products"]["inputSchema"].(map[string]any)
	if !ok {
		t.Fatalf("get_products inputSchema missing: %v", byName["get_products"])
	}
	req, _ := schema["required"].([]any)
	if len(req) != 1 || req[0] != "brief" {
		t.Fatalf("unexpected required: %v", schema["required"])
	}
	bare, ok := byName["get_media_buys"]["inputSchema"].(map[string]any)
	if !ok || bare["type"] != "object" || len(bare) != 1 {
		t.Fatalf("expected bare object schema, got %v", byName["get_media_buys"]["inputSchema"])
	}
}

// FuzzRenderTemplate feeds arbitrary strings to the template renderer:
// it must never panic (returning an error is fine).
func FuzzRenderTemplate(f *testing.F) {
	seeds := []string{
		`{"id": "{{args.id}}"}`,
		`{"t": "{{tool}}", "n": "{{now}}", "c": {{call}}, "s": "{{state}}", "e": "{{entity_id}}", "i": {{id}}}`,
		`no placeholders`,
		`{{unclosed`,
		`stray }} brace`,
		`{{}}`,
		`{{args.}}`,
		`{{nope}}`,
		"{{args.a}}\x00{{args.b}}",
	}
	ctx := templateContext{
		Tool:     "create_media_buy",
		ID:       json.RawMessage(`7`),
		Args:     map[string]any{"id": "x", "nested": map[string]any{"v": 1}},
		Call:     3,
		State:    "active",
		EntityID: "mb-1",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, tmpl string) {
		_, _ = renderTemplate(tmpl, ctx)
	})
}

// FuzzMatchArgs feeds arbitrary pattern/argument shapes to the route
// matcher: it must never panic.
func FuzzMatchArgs(f *testing.F) {
	seeds := []string{
		`{"pattern": {"tool": "x"}, "args": {"a": 1}}`,
		`{"pattern": {"a.b": "v*"}, "args": {"a": {"b": "v123"}}}`,
		`{"pattern": null, "args": null}`,
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		var doc struct {
			Pattern map[string]any `json:"pattern"`
			Args    map[string]any `json:"args"`
		}
		if err := json.Unmarshal([]byte(raw), &doc); err != nil {
			return
		}
		_ = matchArgs(doc.Pattern, doc.Args)
	})
}
