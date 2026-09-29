package scenarios

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sujanchalla0510/adcp-test/internal/mockcfg"
	"github.com/sujanchalla0510/adcp-test/internal/mockserver"
	"github.com/sujanchalla0510/adcp-test/internal/session"
)

// packTestServer starts a mock seller implementing the routes the
// built-in packs exercise: product discovery, media-buy creation (with a
// budget-limit rejection route), creative sync (with a policy-rejection
// route), delivery reads, and the media-buy lifecycle state machine on
// update_media_buy.
func packTestServer(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	templates := map[string]string{
		"create.json":   `{"media_buy_id": "mb-{{call}}", "status": "draft", "product_id": "{{args.product_id}}"}`,
		"delivery.json": `{"media_buy_id": "{{args.media_buy_id}}", "impressions": 1250, "spend_micros": 312500}`,
		"update.json":   `{"media_buy_id": "{{entity_id}}", "status": "{{state}}"}`,
	}
	for name, body := range templates {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	svc := mockcfg.MockService{
		Name:   "pack-seller",
		Listen: ":0",
		Routes: []mockcfg.Route{
			{
				Match:   mockcfg.Match{Tool: "get_products"},
				Respond: mockcfg.Respond{Inline: map[string]any{"products": []any{map[string]any{"product_id": "prod-web-banner", "name": "Web Banner 300x250"}}}},
			},
			{
				Match:   mockcfg.Match{Tool: "create_media_buy", Args: map[string]any{"budget_micros": 999999999999}},
				Respond: mockcfg.Respond{Inline: map[string]any{"status": "rejected", "reason": "budget_micros exceeds product maximum"}},
			},
			{
				Match:   mockcfg.Match{Tool: "create_media_buy"},
				Respond: mockcfg.Respond{Template: "create.json"},
			},
			{
				Match:   mockcfg.Match{Tool: "sync_creatives", Args: map[string]any{"creative.format": "banned"}},
				Respond: mockcfg.Respond{Inline: map[string]any{"status": "rejected", "reason": "policy violation: banned creative format"}},
			},
			{
				Match:   mockcfg.Match{Tool: "sync_creatives"},
				Respond: mockcfg.Respond{Inline: map[string]any{"creative_id": "cr-1", "status": "approved"}},
			},
			{
				Match:   mockcfg.Match{Tool: "get_media_buy_delivery"},
				Respond: mockcfg.Respond{Template: "delivery.json"},
			},
			{
				Match:        mockcfg.Match{Tool: "update_media_buy"},
				Respond:      mockcfg.Respond{Template: "update.json"},
				StateMachine: "media-buy-lifecycle",
			},
		},
	}
	srv, err := mockserver.New(svc, mockserver.Options{RandSeed: 1, BaseDir: dir})
	if err != nil {
		t.Fatalf("mockserver.New: %v", err)
	}
	url, err := srv.Start()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return url
}

func TestBuiltinPacksParse(t *testing.T) {
	metas, err := BuiltinPacks()
	if err != nil {
		t.Fatalf("BuiltinPacks: %v", err)
	}
	if len(metas) != 5 {
		t.Fatalf("got %d built-in packs, want 5", len(metas))
	}
	for _, m := range metas {
		if m.ScenarioCount == 0 {
			t.Errorf("pack %q has no scenarios", m.ID)
		}
	}
}

func TestBuiltinPacksPassAgainstMock(t *testing.T) {
	url := packTestServer(t)
	for _, id := range builtinPackIDs {
		p, err := LoadBuiltin(id)
		if err != nil {
			t.Fatalf("LoadBuiltin(%q): %v", id, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		rep, err := Run(ctx, p, url, Options{Store: session.NewStore()})
		cancel()
		if err != nil {
			t.Fatalf("Run(%q): %v", id, err)
		}
		if !rep.AllPassed() {
			t.Errorf("pack %q did not pass:\n%s", id, reportFailures(rep))
		}
	}
}

// reportFailures renders failing steps/assertions for test diagnostics.
func reportFailures(rep *Report) string {
	var b strings.Builder
	for _, s := range rep.Scenarios {
		if s.Passed {
			continue
		}
		b.WriteString("  scenario " + s.Name + "\n")
		for _, st := range s.Steps {
			if st.Passed {
				continue
			}
			b.WriteString("    step " + st.Name + " err=" + st.Error + "\n")
			for _, a := range st.Assertions {
				if !a.Passed {
					b.WriteString("      assertion " + a.Kind + ": " + a.Detail + "\n")
				}
			}
		}
	}
	return b.String()
}

func TestVarsChaining(t *testing.T) {
	url := packTestServer(t)
	packYAML := `
name: chaining
description: save/vars round trip
scenarios:
  - name: chain
    steps:
      - name: create
        tool: create_media_buy
        arguments: {product_id: prod-web-banner, budget_micros: 1000}
        save: {buy: media_buy_id}
        assertions:
          - status: ok
      - name: read back
        tool: get_media_buy_delivery
        arguments: {media_buy_id: "{{vars.buy}}"}
        assertions:
          - status: ok
          - response_contains: {media_buy_id: "{{vars.buy}}"}
`
	p, err := ParsePack([]byte(packYAML))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rep, err := Run(ctx, p, url, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.AllPassed() {
		t.Fatalf("chaining pack failed:\n%s", reportFailures(rep))
	}
}

func TestSessionRecording(t *testing.T) {
	url := packTestServer(t)
	p, err := LoadBuiltin("budget-limit")
	if err != nil {
		t.Fatal(err)
	}
	store := session.NewStore()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rep, err := Run(ctx, p, url, Options{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.AllPassed() {
		t.Fatalf("budget-limit failed:\n%s", reportFailures(rep))
	}
	sess, ok := store.Get(rep.Scenarios[0].SessionID)
	if !ok {
		t.Fatal("scenario session missing from store")
	}
	// 2 steps + 1 teardown hook = 3 exchanges = 6 half-steps.
	if got := sess.Len(); got != 6 {
		t.Fatalf("session has %d steps, want 6 (out+in per exchange)", got)
	}
	for i, st := range sess.Snapshot() {
		wantDir := session.DirectionOut
		if i%2 == 1 {
			wantDir = session.DirectionIn
		}
		if st.Direction != wantDir {
			t.Errorf("step %d direction = %q, want %q", i, st.Direction, wantDir)
		}
	}
}

func TestRunEvents(t *testing.T) {
	url := packTestServer(t)
	p, err := LoadBuiltin("budget-limit")
	if err != nil {
		t.Fatal(err)
	}
	var events []Event
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err = Run(ctx, p, url, Options{OnEvent: func(e Event) { events = append(events, e) }})
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, e := range events {
		counts[e.Type]++
	}
	if counts[EventScenarioStarted] != 1 || counts[EventScenarioFinished] != 1 {
		t.Errorf("scenario events = %v, want one started and one finished", counts)
	}
	if counts[EventStepFinished] != 2 {
		t.Errorf("step_finished events = %d, want 2", counts[EventStepFinished])
	}
}

func TestAssertionFailureDetail(t *testing.T) {
	url := packTestServer(t)
	packYAML := `
name: failing
description: assertions must fail loudly with detail
scenarios:
  - name: bad expectations
    steps:
      - name: wrong product
        tool: get_products
        arguments: {}
        assertions:
          - status: ok
          - response_contains: {products: [{product_id: no-such-product}]}
          - latency_ms_lt: 0
      - name: unknown tool
        tool: no_such_tool
        arguments: {}
        assertions:
          - status: error
          - error_code: -32601
`
	p, err := ParsePack([]byte(packYAML))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rep, err := Run(ctx, p, url, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.AllPassed() {
		t.Fatal("expected failures, got a pass")
	}
	sr := rep.Scenarios[0]
	if sr.Steps[0].Passed {
		t.Error("step 0 should fail (bad response_contains + latency)")
	}
	var sawContains, sawLatency bool
	for _, a := range sr.Steps[0].Assertions {
		if !a.Passed && a.Detail == "" {
			t.Errorf("failing assertion %q has no detail", a.Kind)
		}
		switch a.Kind {
		case AssertResponseContain:
			sawContains = !a.Passed
		case AssertLatencyMsLt:
			sawLatency = !a.Passed
		}
	}
	if !sawContains || !sawLatency {
		t.Errorf("want failing response_contains and latency_ms_lt, got %+v", sr.Steps[0].Assertions)
	}
	if !sr.Steps[1].Passed {
		t.Errorf("step 1 should pass (unknown tool -> -32601):\n%s", reportFailures(rep))
	}
}

func TestResolvePack(t *testing.T) {
	if _, err := ResolvePack("happy-path-media-buy"); err != nil {
		t.Errorf("builtin id: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "custom.yaml")
	custom := "name: custom\ndescription: x\nscenarios:\n  - name: s\n    steps:\n      - {tool: get_products, assertions: [{status: ok}]}\n"
	if err := os.WriteFile(path, []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := ResolvePack(path)
	if err != nil {
		t.Errorf("file path: %v", err)
	} else if p.Name != "custom" {
		t.Errorf("pack name = %q, want custom", p.Name)
	}
	if _, err := ResolvePack("no-such-pack"); err == nil {
		t.Error("unknown pack should error")
	}
	if _, err := ResolvePack(""); err == nil {
		t.Error("empty ref should error")
	}
}

func TestPackValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		yaml string
		want string
	}{
		{"no name", "description: x\nscenarios:\n  - {name: s, steps: [{tool: t}]}", "pack name"},
		{"no scenarios", "name: x\ndescription: y\n", "no scenarios"},
		{"no steps", "name: x\nscenarios:\n  - {name: s}", "no steps"},
		{"bad assertion", "name: x\nscenarios:\n  - {name: s, steps: [{tool: t, assertions: [{bogus: 1}]}]}", "unknown kind"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParsePack([]byte(tc.yaml))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}
