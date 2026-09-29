package web

import (
	"io/fs"
	"strings"
	"testing"
)

// TestInspectScreenWired guards the M3 UI wiring: the Inspect nav entry
// must be an enabled screen button, the section must exist, and the
// record/replay API surface must be referenced by the client script.
func TestInspectScreenWired(t *testing.T) {
	html, err := fs.ReadFile(FS, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	js, err := fs.ReadFile(FS, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<button data-screen="inspect">Inspect</button>`,
		`id="screen-inspect"`,
		`id="record-form"`,
		`id="replay-form"`,
		`id="sessions-list"`,
		`id="session-detail"`,
		`id="cassettes-list"`,
	} {
		if !strings.Contains(string(html), want) {
			t.Fatalf("index.html missing %q", want)
		}
	}
	if strings.Contains(string(html), `title="M3 — coming soon"`) {
		t.Fatal("index.html still marks Inspect as a coming-soon placeholder")
	}
	for _, want := range []string{
		"/api/sessions",
		"/api/record/start",
		"/api/record/stop",
		"/api/cassettes",
		"/api/replay/start",
		"/api/replay/stop",
		"X-Session-ID",
		"arg_problems",
	} {
		if !strings.Contains(string(js), want) {
			t.Fatalf("app.js missing %q", want)
		}
	}
	// Other milestone screens stay placeholders (M5 shipped this milestone).
	for _, want := range []string{
		`title="M6 — coming soon"`,
	} {
		if !strings.Contains(string(html), want) {
			t.Fatalf("index.html lost placeholder %q", want)
		}
	}
	if strings.Contains(string(html), `title="M5 — coming soon"`) {
		t.Fatal("index.html still marks Scenarios/Load as coming-soon placeholders")
	}
	if strings.Contains(string(html), `title="M4 — coming soon"`) {
		t.Fatal("index.html still marks Mock builder as a coming-soon placeholder")
	}
}
