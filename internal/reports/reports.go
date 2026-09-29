// Package reports builds self-contained evidence packs from run
// reports: an inline-CSS HTML bundle and a minimal pure-Go PDF.
//
// The HTML pack is the primary artifact (tables, status coloring,
// snapshot diffs). The PDF is a text rendering of the same content —
// generated without third-party dependencies, so it stays a single
// static binary.
package reports

import (
	"encoding/json"
	"fmt"
	"html"
	"sort"
	"strings"
	"time"

	"github.com/sujanchalla0510/adcp-test/internal/snapshots"
)

// Inputs selects which reports go into the pack. Every field is
// optional; nil/empty sections are skipped.
type Inputs struct {
	Title       string
	Conformance json.RawMessage
	Scenarios   json.RawMessage
	Load        json.RawMessage
	Lifecycle   json.RawMessage
	Fuzz        json.RawMessage
	Diff        *snapshots.Diff
	GeneratedAt time.Time
}

// Evidence is the normalized intermediate both renderers consume.
type Evidence struct {
	Title       string
	GeneratedAt time.Time
	Sections    []Section
}

// Section is one report rendered as a summary plus an optional table.
type Section struct {
	Title   string
	Summary string
	Headers []string
	Rows    [][]string
	Lines   []string // used when Headers is empty
}

// Build assembles the evidence from the inputs.
func Build(in Inputs) (*Evidence, error) {
	gen := in.GeneratedAt
	if gen.IsZero() {
		gen = time.Now().UTC()
	}
	e := &Evidence{Title: firstNonEmpty(in.Title, "adcp-test evidence pack"), GeneratedAt: gen}
	add := func(raw json.RawMessage, fn func(map[string]any) Section) {
		if len(raw) == 0 {
			return
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			e.Sections = append(e.Sections, Section{Title: "unparseable report", Lines: []string{err.Error()}})
			return
		}
		e.Sections = append(e.Sections, fn(doc))
	}
	add(in.Conformance, conformanceSection)
	add(in.Scenarios, scenarioSection)
	add(in.Load, loadSection)
	add(in.Lifecycle, lifecycleSection)
	add(in.Fuzz, fuzzSection)
	if in.Diff != nil {
		e.Sections = append(e.Sections, diffSection(in.Diff))
	}
	if len(e.Sections) == 0 {
		return nil, fmt.Errorf("reports: no report inputs provided")
	}
	return e, nil
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func str(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

func num(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	return fmt.Sprintf("%v", v)
}

func conformanceSection(doc map[string]any) Section {
	s := Section{Title: "Conformance", Headers: []string{"check", "status", "duration (ms)", "detail"}}
	pass, total := 0, 0
	if checks, ok := doc["checks"].([]any); ok {
		for _, c := range checks {
			m, _ := c.(map[string]any)
			if m == nil {
				continue
			}
			total++
			status := str(m, "status")
			if strings.EqualFold(status, "pass") {
				pass++
			}
			s.Rows = append(s.Rows, []string{str(m, "name"), status, num(m, "duration_ms"), str(m, "detail")})
		}
	}
	s.Summary = fmt.Sprintf("%d/%d checks passed", pass, total)
	if t, ok := doc["target_url"].(string); ok && t != "" {
		s.Summary += " — target " + t
	}
	return s
}

func lifecycleSection(doc map[string]any) Section {
	s := conformanceSection(doc)
	s.Title = "Lifecycle"
	return s
}

func scenarioSection(doc map[string]any) Section {
	s := Section{Title: "Scenarios", Headers: []string{"scenario / step", "result", "latency (ms)", "detail"}}
	pass, total := 0, 0
	if scs, ok := doc["scenarios"].([]any); ok {
		for _, sc := range scs {
			m, _ := sc.(map[string]any)
			if m == nil {
				continue
			}
			total++
			scName := str(m, "name")
			scPassed, _ := m["passed"].(bool)
			if scPassed {
				pass++
			}
			res := "FAIL"
			if scPassed {
				res = "PASS"
			}
			s.Rows = append(s.Rows, []string{scName, res, "", firstNonEmpty(str(m, "setup_error"), str(m, "teardown_error"))})
			if steps, ok := m["steps"].([]any); ok {
				for _, st := range steps {
					sm, _ := st.(map[string]any)
					if sm == nil {
						continue
					}
					stPassed, _ := sm["passed"].(bool)
					stRes := "FAIL"
					if stPassed {
						stRes = "PASS"
					}
					detail := str(sm, "error")
					if chaos, ok := sm["chaos"].(map[string]any); ok && chaos != nil {
						detail = strings.TrimSpace(detail + " [chaos: " + str(chaos, "injected") + "]")
					}
					s.Rows = append(s.Rows, []string{"  " + scName + " / " + str(sm, "name"), stRes, num(sm, "latency_ms"), detail})
				}
			}
		}
	}
	s.Summary = fmt.Sprintf("%d/%d scenarios passed", pass, total)
	if p, ok := doc["pack"].(string); ok && p != "" {
		s.Summary += " — pack " + p
	}
	return s
}

func loadSection(doc map[string]any) Section {
	s := Section{Title: "Load"}
	if sum, ok := doc["summary"].(map[string]any); ok {
		keys := make([]string, 0, len(sum))
		for k := range sum {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			s.Lines = append(s.Lines, fmt.Sprintf("%s: %v", k, sum[k]))
		}
		s.Summary = fmt.Sprintf("%d summary metrics", len(keys))
	} else {
		s.Lines = []string{"no summary present"}
	}
	return s
}

func fuzzSection(doc map[string]any) Section {
	s := Section{Title: "Fuzz", Headers: []string{"iteration", "kind", "detail"}}
	n := 0
	if findings, ok := doc["findings"].([]any); ok {
		for _, f := range findings {
			m, _ := f.(map[string]any)
			if m == nil {
				continue
			}
			n++
			s.Rows = append(s.Rows, []string{num(m, "iteration"), str(m, "kind"), str(m, "detail")})
		}
	}
	if n == 0 {
		s.Summary = "clean — no findings"
	} else {
		s.Summary = fmt.Sprintf("%d findings", n)
	}
	return s
}

func diffSection(d *snapshots.Diff) Section {
	s := Section{Title: "Snapshot diff: " + d.Before + " → " + d.After}
	join := func(ss []string) string {
		if len(ss) == 0 {
			return "none"
		}
		return strings.Join(ss, ", ")
	}
	s.Lines = []string{
		fmt.Sprintf("changed checks: %d", d.Changed),
		"pass → fail: " + join(d.PassFail),
		"fail → pass: " + join(d.FailPass),
		"new: " + join(d.New),
		"removed: " + join(d.Removed),
	}
	s.Summary = fmt.Sprintf("%d changed", d.Changed)
	return s
}

// BuildHTML renders the evidence as a self-contained HTML document
// with inline CSS.
func BuildHTML(e *Evidence) string {
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">`)
	b.WriteString("<title>" + html.EscapeString(e.Title) + "</title><style>")
	b.WriteString(`body{font-family:system-ui,-apple-system,"Segoe UI",sans-serif;margin:2rem auto;max-width:72rem;color:#1a1a1a;background:#fff;line-height:1.5}` +
		`h1{font-size:1.6rem}h2{font-size:1.2rem;margin-top:2rem;border-bottom:2px solid #eee;padding-bottom:.3rem}` +
		`.meta{color:#666;font-size:.9rem}` +
		`.summary{background:#f6f8fa;border:1px solid #e1e4e8;border-radius:6px;padding:.6rem .9rem;margin:.6rem 0}` +
		`table{border-collapse:collapse;width:100%;font-size:.85rem;margin:.6rem 0}` +
		`th,td{text-align:left;padding:.4rem .6rem;border:1px solid #e1e4e8;vertical-align:top}` +
		`th{background:#f6f8fa}td.st-pass{color:#0a7c2e;font-weight:600}td.st-fail{color:#c0392b;font-weight:600}` +
		`ul{font-size:.9rem}.foot{margin-top:3rem;color:#999;font-size:.8rem}`)
	b.WriteString(`</style></head><body>`)
	b.WriteString("<h1>" + html.EscapeString(e.Title) + "</h1>")
	b.WriteString(`<p class="meta">Generated ` + html.EscapeString(e.GeneratedAt.Format(time.RFC3339)) + ` by adcp-test v0.1</p>`)
	for _, s := range e.Sections {
		b.WriteString("<h2>" + html.EscapeString(s.Title) + "</h2>")
		if s.Summary != "" {
			b.WriteString(`<div class="summary">` + html.EscapeString(s.Summary) + `</div>`)
		}
		if len(s.Headers) > 0 {
			b.WriteString("<table><tr>")
			for _, h := range s.Headers {
				b.WriteString("<th>" + html.EscapeString(h) + "</th>")
			}
			b.WriteString("</tr>")
			for _, row := range s.Rows {
				b.WriteString("<tr>")
				for i, cell := range row {
					cls := ""
					if (i == 1 || strings.EqualFold(s.Headers[i], "status") || strings.EqualFold(s.Headers[i], "result")) &&
						(strings.EqualFold(cell, "pass") || cell == "PASS") {
						cls = ` class="st-pass"`
					}
					if (i == 1 || strings.EqualFold(s.Headers[i], "status") || strings.EqualFold(s.Headers[i], "result")) &&
						(strings.EqualFold(cell, "fail") || cell == "FAIL") {
						cls = ` class="st-fail"`
					}
					b.WriteString("<td" + cls + ">" + html.EscapeString(cell) + "</td>")
				}
				b.WriteString("</tr>")
			}
			b.WriteString("</table>")
		} else if len(s.Lines) > 0 {
			b.WriteString("<ul>")
			for _, l := range s.Lines {
				b.WriteString("<li>" + html.EscapeString(l) + "</li>")
			}
			b.WriteString("</ul>")
		}
	}
	b.WriteString(`<p class="foot">Evidence pack generated locally by adcp-test. No data left this machine.</p>`)
	b.WriteString("</body></html>")
	return b.String()
}
