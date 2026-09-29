package web

import (
	"io/fs"
	"strings"
	"testing"
)

func m6assets(t *testing.T) (string, string) {
	t.Helper()
	html, err := fs.ReadFile(FS, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	js, err := fs.ReadFile(FS, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	return string(html), string(js)
}

// TestM6NavLive guards the M6 launch wiring: every M6 screen must have an
// enabled nav entry and a section; the old disabled placeholders must be gone.
func TestM6NavLive(t *testing.T) {
	html, _ := m6assets(t)
	for _, screen := range []string{"signdebug", "webhooks", "snapshots", "reports"} {
		if !strings.Contains(html, `<button data-screen="`+screen+`">`) {
			t.Errorf("nav entry for %s missing or disabled", screen)
		}
		if !strings.Contains(html, `id="screen-`+screen+`"`) {
			t.Errorf("screen section for %s missing", screen)
		}
	}
	if strings.Contains(html, `title="M6 — coming soon"`) {
		t.Error("stale M6 coming-soon placeholders remain in nav")
	}
	if !strings.Contains(html, "M6") || !strings.Contains(html, "live") {
		t.Error("dashboard roadmap does not mark M6 as live")
	}
}

// TestSigndebugScreenWired checks the signing debugger form, the verify API
// surface, and the verdict rendering helpers.
func TestSigndebugScreenWired(t *testing.T) {
	html, js := m6assets(t)
	for _, want := range []string{
		`id="signdebug-form"`,
		`id="signdebug-results"`,
		`id="sd-method"`,
		`id="sd-url"`,
		`id="sd-headers"`,
		`id="sd-body"`,
		`id="sd-key"`,
		`id="sd-expected"`,
		`never stored`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("signdebug screen missing %q", want)
		}
	}
	for _, want := range []string{
		"/api/signdebug/verify",
		"runSigndebug",
		"renderSigndebug",
		"parseHeaderLines",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js missing %q", want)
		}
	}
}

// TestSigndebugKeyNotPersisted is a safety test: the verification key must
// never be stored, rendered back, or echoed into reports. The key field is
// cleared immediately after the single verification call, and the client
// never writes key material to localStorage.
func TestSigndebugKeyNotPersisted(t *testing.T) {
	_, js := m6assets(t)
	if !strings.Contains(js, `getElementById("sd-key").value = ""`) {
		t.Error("key field is not cleared after verification")
	}
	if strings.Contains(js, "localStorage.setItem") || strings.Contains(js, "sessionStorage.setItem") {
		t.Error("app.js persists values to web storage — key material must never be stored")
	}
	if strings.Contains(js, "key_pem") && strings.Contains(js, "innerHTML") {
		// key_pem is sent in the POST body only; it must never be interpolated
		// into rendered HTML.
		if strings.Contains(js, "${") && strings.Contains(js, "key_pem") {
			for _, line := range strings.Split(js, "\n") {
				if strings.Contains(line, "key_pem") && strings.Contains(line, "innerHTML") {
					t.Errorf("key material interpolated into rendered HTML: %s", strings.TrimSpace(line))
				}
			}
		}
	}
}

// TestChaosToggleWired checks the scenario chaos toggle and its seed field.
func TestChaosToggleWired(t *testing.T) {
	html, js := m6assets(t)
	for _, want := range []string{
		`id="scenario-chaos"`,
		`id="scenario-chaos-seed"`,
		`id="scenario-allow-remote"`,
		"chaos: chaos",
		"chaos_seed: chaosSeed",
		"allow_remote: allowRemote",
		"badge chaos",
	} {
		src := html + js
		if !strings.Contains(src, want) {
			t.Errorf("chaos wiring missing %q", want)
		}
	}
}

// TestLifecycleFuzzWired checks the lifecycle runner card and the fuzz form.
func TestLifecycleFuzzWired(t *testing.T) {
	html, js := m6assets(t)
	for _, want := range []string{
		`id="lifecycle-run-btn"`,
		`id="lifecycle-results"`,
		`id="fuzz-form"`,
		`id="fuzz-results"`,
		`id="fuzz-iterations"`,
		`id="fuzz-seed"`,
		`id="fuzz-allow-remote"`,
		"/api/lifecycle/run",
		"/api/fuzz/run",
		"runLifecycleCheck",
		"runFuzz",
	} {
		if !strings.Contains(html+js, want) {
			t.Errorf("lifecycle/fuzz wiring missing %q", want)
		}
	}
}

// TestWebhooksScreenWired checks the webhook listener UI against the server API.
func TestWebhooksScreenWired(t *testing.T) {
	html, js := m6assets(t)
	for _, want := range []string{
		`id="webhook-form"`,
		`id="webhook-deliveries"`,
		"/api/webhooks/listen",
		"/api/webhooks/deliveries",
		"/api/webhooks/clear",
		"/api/webhooks/stop",
		"startWebhookListener",
		"refreshWebhookDeliveries",
	} {
		if !strings.Contains(html+js, want) {
			t.Errorf("webhooks wiring missing %q", want)
		}
	}
	if !strings.Contains(html, "redacted") {
		t.Error("webhooks screen does not mention header redaction")
	}
}

// TestSnapshotsScreenWired checks the snapshot save/list/diff UI.
func TestSnapshotsScreenWired(t *testing.T) {
	html, js := m6assets(t)
	for _, want := range []string{
		`id="snapshot-save-form"`,
		`id="snapshot-list"`,
		`id="snapshot-diff-form"`,
		`id="snapshot-diff-results"`,
		"/api/snapshots",
		"/api/snapshots/save",
		"/api/snapshots/diff",
		"/api/snapshots/delete",
		"saveSnapshot",
		"diffSnapshots",
	} {
		if !strings.Contains(html+js, want) {
			t.Errorf("snapshots wiring missing %q", want)
		}
	}
}

// TestReportsScreenWired checks the evidence report builder and downloads.
func TestReportsScreenWired(t *testing.T) {
	html, js := m6assets(t)
	for _, want := range []string{
		`id="report-form"`,
		`id="report-html-btn"`,
		`id="report-pdf-btn"`,
		"/api/reports/build",
		"buildReport",
		"adcp-test-evidence.",
	} {
		if !strings.Contains(html+js, want) {
			t.Errorf("reports wiring missing %q", want)
		}
	}
	for _, kind := range []string{"conformance", "scenarios", "load", "lifecycle", "fuzz"} {
		if !strings.Contains(js, `id="rep-use-${k}"`) || !strings.Contains(js, "use last ") {
			t.Errorf("reports screen missing last-run reuse for %s", kind)
		}
	}
}
