package mockcfg

import (
	"strings"
	"testing"
)

// TestRespondErrorMode covers the M6 "error" respond mode: parse,
// validate, marshal round-trip, and rejection of bad shapes.
func TestRespondErrorMode(t *testing.T) {
	cfg, err := Parse([]byte(`
mocks:
  - name: seller
    listen: :8080
    routes:
      - match: { tool: create_media_buy, args: { start_time: 12345 } }
        respond: { error: { code: -32001, message: "unsigned mutating call rejected" } }
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	rt := cfg.Services()[0].Routes[0]
	if rt.Respond.Error == nil {
		t.Fatal("expected error respond mode to be set")
	}
	if rt.Respond.Error.Code != -32001 || rt.Respond.Error.Message != "unsigned mutating call rejected" {
		t.Fatalf("unexpected error response: %+v", rt.Respond.Error)
	}

	// Marshal round-trip preserves the error mode.
	data, err := cfg.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "error:") {
		t.Fatalf("marshaled config lost the error mode:\n%s", data)
	}
	cfg2, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg2.Services()[0].Routes[0].Respond.Error; got == nil || got.Code != -32001 {
		t.Fatalf("round-trip lost error mode: %+v", got)
	}
}

func TestRespondErrorModeInvalid(t *testing.T) {
	for name, yamlDoc := range map[string]string{
		"missing code":    "mocks:\n  - name: s\n    listen: :9090\n    routes:\n      - match: { tool: t }\n        respond: { error: { message: x } }\n",
		"missing message": "mocks:\n  - name: s\n    listen: :9090\n    routes:\n      - match: { tool: t }\n        respond: { error: { code: -32001 } }\n",
		"not a mapping":   "mocks:\n  - name: s\n    listen: :9090\n    routes:\n      - match: { tool: t }\n        respond: { error: -32001 }\n",
		"two modes":       "mocks:\n  - name: s\n    listen: :9090\n    routes:\n      - match: { tool: t }\n        respond: { inline: { a: 1 }, error: { code: -1, message: x } }\n",
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := Parse([]byte(yamlDoc))
			if err == nil {
				if verr := cfg.Validate(); verr == nil {
					t.Fatal("expected parse or validation error")
				}
			}
		})
	}
}

func TestRouteInputSchema(t *testing.T) {
	cfg, err := Parse([]byte(`
mocks:
  - name: seller
    listen: :8080
    routes:
      - match: { tool: get_products }
        input_schema: { type: object, required: [brief], properties: { brief: { type: string } } }
        respond: { inline: { products: [] } }
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	rt := cfg.Services()[0].Routes[0]
	m, ok := rt.InputSchema.(map[string]any)
	if !ok {
		t.Fatalf("input_schema not a mapping: %T", rt.InputSchema)
	}
	if m["type"] != "object" {
		t.Fatalf("unexpected schema: %v", m)
	}

	// Non-mapping input_schema fails validation.
	bad, err := Parse([]byte(
		"mocks:\n  - name: s\n    listen: :9090\n    routes:\n      - match: { tool: t }\n        input_schema: [not, a, mapping]\n        respond: { inline: { a: 1 } }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := bad.Validate(); err == nil {
		t.Fatal("expected validation error for non-mapping input_schema")
	}
}

// FuzzParse feeds arbitrary bytes to the config parser: it must never
// panic, whatever it returns.
func FuzzParse(f *testing.F) {
	seeds := []string{
		"mocks:\n  - name: s\n    listen: :9090\n    routes:\n      - match: { tool: t }\n        respond: { inline: { a: 1 } }\n",
		"mocks:\n  - name: s\n    listen: :9090\n    routes:\n      - match: { tool: t }\n        respond: { error: { code: -32001, message: nope } }\n",
		"mocks:\n  - name: s\n    listen: :9090\n    routes:\n      - match: { tool: t }\n        respond: { template: \"{{args.x}} {{now}} {{state}} {{entity_id}} {{call}} {{tool}} {{id}}\" }\n",
		"not yaml at all {{{",
		"",
		"mocks: [1, 2, 3]",
		"respond: { error: { code: [nested], message: { deep: [1, {x: y}] } } }",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		cfg, err := Parse(data)
		if err != nil {
			return
		}
		_ = cfg.Validate()
		_, _ = cfg.Marshal()
	})
}
