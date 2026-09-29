package reports

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sujanchalla0510/adcp-test/internal/snapshots"
)

func testInputs() Inputs {
	conf, _ := json.Marshal(map[string]any{
		"target_url": "http://127.0.0.1:1/",
		"checks": []any{
			map[string]any{"name": "mcp/initialize", "status": "pass", "detail": "ok", "duration_ms": 3.1},
			map[string]any{"name": "tools/list", "status": "fail", "detail": "timeout", "duration_ms": 100.0},
		},
	})
	scn, _ := json.Marshal(map[string]any{
		"pack": "happy-path-media-buy",
		"scenarios": []any{
			map[string]any{
				"name": "s1", "passed": true,
				"steps": []any{
					map[string]any{"name": "step one", "passed": true, "latency_ms": 4.2},
					map[string]any{"name": "step two", "passed": true, "latency_ms": 5.1,
						"chaos": map[string]any{"injected": "latency-spike"}},
				},
			},
		},
	})
	fz, _ := json.Marshal(map[string]any{
		"findings": []any{
			map[string]any{"iteration": 7, "kind": "non-json", "detail": "HTTP 500 with non-JSON body"},
		},
	})
	return Inputs{
		Title:       "test evidence",
		Conformance: conf,
		Scenarios:   scn,
		Fuzz:        fz,
		GeneratedAt: time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC),
	}
}

func TestBuildHTML(t *testing.T) {
	e, err := Build(testInputs())
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Sections) != 3 {
		t.Fatalf("expected 3 sections, got %d", len(e.Sections))
	}
	doc := BuildHTML(e)
	for _, want := range []string{
		"test evidence", "1/2 checks passed", "mcp/initialize", "tools/list",
		"happy-path-media-buy", "latency-spike", "non-json",
		"<style>", "</html>",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("HTML missing %q", want)
		}
	}
	// Status coloring hooks.
	if !strings.Contains(doc, "st-pass") || !strings.Contains(doc, "st-fail") {
		t.Error("expected pass/fail CSS classes")
	}
	// Escaping: a hostile check name must not break out.
	evil, _ := json.Marshal(map[string]any{
		"checks": []any{map[string]any{"name": "<script>alert(1)</script>", "status": "pass"}},
	})
	e2, err := Build(Inputs{Conformance: evil})
	if err != nil {
		t.Fatal(err)
	}
	doc2 := BuildHTML(e2)
	if strings.Contains(doc2, "<script>alert(1)</script>") {
		t.Error("check name not escaped")
	}
}

func TestBuildEmpty(t *testing.T) {
	if _, err := Build(Inputs{}); err == nil {
		t.Error("expected an error with no inputs")
	}
}

func TestBuildPDF(t *testing.T) {
	e, err := Build(testInputs())
	if err != nil {
		t.Fatal(err)
	}
	pdf := BuildPDF(e)
	if !bytes.HasPrefix(pdf, []byte("%PDF-1.4")) {
		t.Error("missing PDF header")
	}
	if !bytes.HasSuffix(bytes.TrimSpace(pdf), []byte("%%EOF")) {
		t.Error("missing PDF trailer")
	}
	// Title text must be embedded (escaped).
	if !bytes.Contains(pdf, []byte("test evidence")) {
		t.Error("title text missing from PDF")
	}
	if !bytes.Contains(pdf, []byte("non-json")) {
		t.Error("fuzz finding missing from PDF")
	}
	// xref table must reference every object.
	if !bytes.Contains(pdf, []byte("xref")) || !bytes.Contains(pdf, []byte("startxref")) {
		t.Error("missing xref bookkeeping")
	}
}

func TestBuildPDFManyPages(t *testing.T) {
	// Enough rows to force pagination.
	rows := make([]any, 300)
	for i := range rows {
		rows[i] = map[string]any{"name": "check", "status": "pass", "detail": "ok"}
	}
	conf, _ := json.Marshal(map[string]any{"checks": rows})
	e, err := Build(Inputs{Conformance: conf})
	if err != nil {
		t.Fatal(err)
	}
	pdf := BuildPDF(e)
	if n := bytes.Count(pdf, []byte("/Type /Page ")); n < 3 {
		t.Errorf("expected multiple pages, found %d page objects", n)
	}
}

func TestDiffSection(t *testing.T) {
	s := snapshots.New(t.TempDir())
	mk := func(status string) json.RawMessage {
		raw, _ := json.Marshal(map[string]any{
			"checks": []any{map[string]any{"name": "a", "status": status}},
		})
		return raw
	}
	if _, err := s.Save("conformance", "before", mk("pass")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save("conformance", "after", mk("fail")); err != nil {
		t.Fatal(err)
	}
	d, err := s.DiffSnapshots("before", "after")
	if err != nil {
		t.Fatal(err)
	}
	e, err := Build(Inputs{Diff: d})
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Sections) != 1 || !strings.Contains(e.Sections[0].Title, "before") {
		t.Fatalf("diff section: %+v", e.Sections)
	}
	doc := BuildHTML(e)
	if !strings.Contains(doc, "pass \u2192 fail") {
		t.Error("expected pass→fail line in HTML")
	}
}
