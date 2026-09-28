package web

import (
	"io/fs"
	"strings"
	"testing"
)

// TestMockScreenWired guards the M4 UI wiring: the Mock builder nav entry
// must be an enabled screen button, the section and its editor controls
// must exist, and the client script must reference the mock API surface.
func TestMockScreenWired(t *testing.T) {
	html, err := fs.ReadFile(FS, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	js, err := fs.ReadFile(FS, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<button data-screen="mock">Mock builder</button>`,
		`id="screen-mock"`,
		`id="mock-service-form"`,
		`id="mock-routes"`,
		`id="mock-add-route"`,
		`id="mock-yaml"`,
		`id="mock-errors"`,
		`id="mock-validate-btn"`,
		`id="mock-start-btn"`,
		`id="mock-stop-btn"`,
		`id="mock-status"`,
		`id="mock-record-form"`,
		`id="mock-record-url"`,
		`id="mock-record-status"`,
	} {
		if !strings.Contains(string(html), want) {
			t.Fatalf("index.html missing %q", want)
		}
	}
	for _, want := range []string{
		"/api/mock/config",
		"/api/mock/validate",
		"/api/mock/start",
		"/api/mock/stop",
		"/api/mock/record",
		"/api/mock/record/finish",
		"media-buy-lifecycle",
	} {
		if !strings.Contains(string(js), want) {
			t.Fatalf("app.js missing %q", want)
		}
	}
}
