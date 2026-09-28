package mockcfg

import (
	"strings"
	"testing"
)

const sampleYAML = `
mocks:
  - name: seller
    protocol: adcp
    listen: :8080
    routes:
      - match: { tool: get_products }
        respond: { file: products.json }
        latency: { p50: 40ms, p99: 250ms }
        faults: { error_rate: 0.01, timeout_rate: 0.005, malformed_rate: 0.0 }
      - match: { tool: create_media_buy, args: { buyer_ref: "acme*" } }
        respond: { template: media-buy.json }
        state_machine: media-buy-lifecycle
      - match: { tool: ping }
        respond: { inline: { ok: true } }
      - match: { tool: seq }
        respond: { sequence: [first.json, { inline: { n: 2 } }] }
    record:
      upstream: https://real-seller.example/mcp
      capture_to: integration.yaml
  - name: signals
    listen: :8081
    routes:
      - match: { tool: get_signals }
        respond:
          signals: []
        latency: { fixed: 25ms }
`

func TestParseSample(t *testing.T) {
	c, err := Parse([]byte(sampleYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	svcs := c.Services()
	if len(svcs) != 2 {
		t.Fatalf("Services() = %d, want 2", len(svcs))
	}
	if svcs[0].Name != "seller" || svcs[0].Listen != ":8080" {
		t.Fatalf("service[0] = %+v", svcs[0])
	}
	if len(svcs[0].Routes) != 4 {
		t.Fatalf("routes = %d, want 4", len(svcs[0].Routes))
	}
	r0 := svcs[0].Routes[0]
	if r0.Respond.File != "products.json" {
		t.Fatalf("route0 respond file = %q", r0.Respond.File)
	}
	if r0.Latency.P50 != "40ms" || r0.Latency.P99 != "250ms" {
		t.Fatalf("route0 latency = %+v", r0.Latency)
	}
	if r0.Faults.ErrorRate != 0.01 || r0.Faults.TimeoutRate != 0.005 {
		t.Fatalf("route0 faults = %+v", r0.Faults)
	}
	r1 := svcs[0].Routes[1]
	if r1.Respond.Template != "media-buy.json" {
		t.Fatalf("route1 template = %q", r1.Respond.Template)
	}
	if r1.Match.Args["buyer_ref"] != "acme*" {
		t.Fatalf("route1 args = %v", r1.Match.Args)
	}
	if r1.StateMachine != StateMachineMediaBuyLifecycle {
		t.Fatalf("route1 state machine = %q", r1.StateMachine)
	}
	r2 := svcs[0].Routes[2]
	body, ok := r2.Respond.Inline.(map[string]any)
	if !ok || body["ok"] != true {
		t.Fatalf("route2 inline = %#v", r2.Respond.Inline)
	}
	r3 := svcs[0].Routes[3]
	if len(r3.Respond.Sequence) != 2 || r3.Respond.Sequence[0].File != "first.json" {
		t.Fatalf("route3 sequence = %+v", r3.Respond.Sequence)
	}
	if r3.Respond.Sequence[1].Inline.(map[string]any)["n"] != 2 {
		t.Fatalf("route3 sequence[1] = %+v", r3.Respond.Sequence[1])
	}
	if svcs[0].Record.Upstream != "https://real-seller.example/mcp" {
		t.Fatalf("record = %+v", svcs[0].Record)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestParseSingularMockAlias(t *testing.T) {
	c, err := Parse([]byte("mock:\n  name: s\n  listen: :9090\n  routes:\n    - match: { tool: t }\n      respond: { inline: { a: 1 } }\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	svcs := c.Services()
	if len(svcs) != 1 || svcs[0].Name != "s" {
		t.Fatalf("Services() = %+v", svcs)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestBareMappingIsInline(t *testing.T) {
	c, err := Parse([]byte("mocks:\n  - name: s\n    listen: :9090\n    routes:\n      - match: { tool: t }\n        respond:\n          products:\n            - id: p1\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	body := c.Services()[0].Routes[0].Respond.Inline.(map[string]any)
	prods := body["products"].([]any)
	if prods[0].(map[string]any)["id"] != "p1" {
		t.Fatalf("inline body = %#v", body)
	}
}

func TestRoundTripStable(t *testing.T) {
	c1, err := Parse([]byte(sampleYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	out, err := c1.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	c2, err := Parse(out)
	if err != nil {
		t.Fatalf("re-Parse: %v\n%s", err, out)
	}
	out2, err := c2.Marshal()
	if err != nil {
		t.Fatalf("re-Marshal: %v", err)
	}
	if string(out) != string(out2) {
		t.Fatalf("round trip not stable:\n--- first ---\n%s\n--- second ---\n%s", out, out2)
	}
	// Spot-check semantics survived: sequence order, template, faults.
	r := c2.Services()[0].Routes[3]
	if len(r.Respond.Sequence) != 2 || r.Respond.Sequence[0].File != "first.json" {
		t.Fatalf("sequence after round trip = %+v", r.Respond.Sequence)
	}
}

func TestValidationFailures(t *testing.T) {
	cases := map[string]string{
		"no services":       "mocks: []\n",
		"missing tool":      "mocks:\n  - name: s\n    listen: :9090\n    routes:\n      - match: {}\n        respond: { inline: {} }\n",
		"no respond":        "mocks:\n  - name: s\n    listen: :9090\n    routes:\n      - match: { tool: t }\n",
		"two modes":         "mocks:\n  - name: s\n    listen: :9090\n    routes:\n      - match: { tool: t }\n        respond: { file: a.json, template: b.json }\n",
		"bad listen":        "mocks:\n  - name: s\n    listen: notaport\n    routes: []\n",
		"p99 < p50":         "mocks:\n  - name: s\n    listen: :9090\n    routes:\n      - match: { tool: t }\n        respond: { inline: {} }\n        latency: { p50: 250ms, p99: 40ms }\n",
		"fault > 1":         "mocks:\n  - name: s\n    listen: :9090\n    routes:\n      - match: { tool: t }\n        respond: { inline: {} }\n        faults: { error_rate: 1.5 }\n",
		"faults sum > 1":    "mocks:\n  - name: s\n    listen: :9090\n    routes:\n      - match: { tool: t }\n        respond: { inline: {} }\n        faults: { error_rate: 0.6, timeout_rate: 0.5 }\n",
		"bad state machine": "mocks:\n  - name: s\n    listen: :9090\n    routes:\n      - match: { tool: t }\n        respond: { inline: {} }\n        state_machine: bogus\n",
		"bad upstream":      "mocks:\n  - name: s\n    listen: :9090\n    routes: []\n    record: { upstream: \"ftp://x\", capture_to: c.yaml }\n",
		"both mock forms":   "mock:\n  name: a\n  listen: :9090\nmocks:\n  - name: b\n    listen: :9091\n",
		"dup listen":        "mocks:\n  - name: a\n    listen: :9090\n  - name: b\n    listen: :9090\n",
		"bad duration":      "mocks:\n  - name: s\n    listen: :9090\n    routes:\n      - match: { tool: t }\n        respond: { inline: {} }\n        latency: { fixed: soon }\n",
		"empty sequence":    "mocks:\n  - name: s\n    listen: :9090\n    routes:\n      - match: { tool: t }\n        respond: { sequence: [] }\n",
	}
	for name, doc := range cases {
		c, err := Parse([]byte(doc))
		if err != nil {
			continue // parse-time rejection is also fine
		}
		if err := c.Validate(); err == nil {
			t.Errorf("%s: expected validation error, got nil", name)
		}
	}
}

func TestValidateTemplateSyntax(t *testing.T) {
	valid := []string{
		`{"id": "{{args.media_buy_id}}", "at": "{{now}}"}`,
		`{{tool}} {{id}} {{call}} {{state}} {{entity_id}}`,
		`{{args.creative.id}}`,
		`no placeholders at all`,
	}
	for _, v := range valid {
		if err := ValidateTemplateSyntax(v); err != nil {
			t.Errorf("valid template %q: %v", v, err)
		}
	}
	invalid := []string{
		`{{unclosed`,
		`stray }} here`,
		`{{}}`,
		`{{bogus}}`,
		`{{args}}`,
		`{{args.}}`,
		`{{args.bad segment}}`,
	}
	for _, v := range invalid {
		if err := ValidateTemplateSyntax(v); err == nil {
			t.Errorf("invalid template %q: expected error", v)
		}
	}
}

func TestUnknownFieldRejected(t *testing.T) {
	_, err := Parse([]byte("mocks:\n  - name: s\n    listen: :9090\n    bogus_key: 1\n"))
	if err == nil || !strings.Contains(err.Error(), "bogus_key") {
		t.Fatalf("expected strict unknown-field error, got %v", err)
	}
}

func TestResolve(t *testing.T) {
	c := &Config{}
	c.baseDir = "/tmp/x"
	if got := c.Resolve("a.json"); got != "/tmp/x/a.json" {
		t.Fatalf("Resolve = %q", got)
	}
	if got := c.Resolve("/abs/b.json"); got != "/abs/b.json" {
		t.Fatalf("Resolve abs = %q", got)
	}
}
