package web

import (
	"io/fs"
	"strings"
	"testing"
)

// TestScenariosLoadScreensWired guards the Scenarios/Load UI wiring: the Scenarios
// and Load nav entries must be enabled screen buttons, the sections and
// their controls must exist, and the client script must reference the
// scenario/load API surface.
func TestScenariosLoadScreensWired(t *testing.T) {
	html, err := fs.ReadFile(FS, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	js, err := fs.ReadFile(FS, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<button data-screen="scenarios">Scenarios</button>`,
		`<button data-screen="load">Load</button>`,
		`id="screen-scenarios"`,
		`id="screen-load"`,
		`id="scenario-target"`,
		`id="scenario-packs"`,
		`id="scenario-results"`,
		`id="load-form"`,
		`id="load-target"`,
		`id="load-tool"`,
		`id="load-pack"`,
		`id="load-concurrency"`,
		`id="load-duration"`,
		`id="load-run-btn"`,
		`id="load-live"`,
		`id="load-chart-lat"`,
		`id="load-chart-rps"`,
		`id="load-results"`,
		`id="load-allow-remote"`,
	} {
		if !strings.Contains(string(html), want) {
			t.Fatalf("index.html missing %q", want)
		}
	}
	if strings.Contains(string(html), "coming soon") {
		t.Fatal("index.html still marks Scenarios/Load as a coming-soon placeholder")
	}
	for _, want := range []string{
		"/api/scenarios",
		"/api/scenarios/run",
		"/api/load/presets",
		"/api/load/run",
		"/api/load/results/",
		"scenario-run-btn",
		"step_finished",
		"drawLineChart",
		"readSSE",
	} {
		if !strings.Contains(string(js), want) {
			t.Fatalf("app.js missing %q", want)
		}
	}
}
