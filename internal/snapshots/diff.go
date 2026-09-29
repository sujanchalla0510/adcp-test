package snapshots

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// point is one named pass/fail (or informational) value in a report.
type point struct {
	name   string
	status string // "pass" | "fail" | raw value for summaries
	detail string
}

// extractPoints normalizes the report kinds this tool knows about into
// named points: conformance/lifecycle checks, scenario steps
// (namespaced by scenario), and load summary scalars.
func extractPoints(doc map[string]any) []point {
	var out []point
	if checks, ok := doc["checks"].([]any); ok {
		for _, c := range checks {
			m, _ := c.(map[string]any)
			if m == nil {
				continue
			}
			name, _ := m["name"].(string)
			status, _ := m["status"].(string)
			detail, _ := m["detail"].(string)
			if name == "" {
				continue
			}
			out = append(out, point{name: name, status: normStatus(status), detail: detail})
		}
		return out
	}
	if scs, ok := doc["scenarios"].([]any); ok {
		for _, sc := range scs {
			m, _ := sc.(map[string]any)
			if m == nil {
				continue
			}
			scName, _ := m["name"].(string)
			steps, _ := m["steps"].([]any)
			for _, st := range steps {
				sm, _ := st.(map[string]any)
				if sm == nil {
					continue
				}
				stName, _ := sm["name"].(string)
				passed, _ := sm["passed"].(bool)
				status := "fail"
				if passed {
					status = "pass"
				}
				errMsg, _ := sm["error"].(string)
				out = append(out, point{
					name:   scName + " / " + stName,
					status: status,
					detail: errMsg,
				})
			}
		}
		return out
	}
	if sum, ok := doc["summary"].(map[string]any); ok {
		keys := make([]string, 0, len(sum))
		for k := range sum {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			out = append(out, point{name: "summary." + k, status: fmt.Sprintf("%v", sum[k])})
		}
		return out
	}
	return out
}

func normStatus(s string) string {
	switch strings.ToLower(s) {
	case "pass", "passed", "ok", "success":
		return "pass"
	case "fail", "failed", "error":
		return "fail"
	default:
		return strings.ToLower(s)
	}
}

// CheckDiff is one named point compared across two snapshots.
type CheckDiff struct {
	Name         string `json:"name"`
	Before       string `json:"before"`
	After        string `json:"after"`
	Changed      bool   `json:"changed"`
	BeforeDetail string `json:"before_detail,omitempty"`
	AfterDetail  string `json:"after_detail,omitempty"`
}

// Diff is the comparison of two snapshots.
type Diff struct {
	Before   string      `json:"before"`
	After    string      `json:"after"`
	Changed  int         `json:"changed"`
	New      []string    `json:"new"`
	Removed  []string    `json:"removed"`
	PassFail []string    `json:"pass_to_fail"`
	FailPass []string    `json:"fail_to_pass"`
	Checks   []CheckDiff `json:"checks"`
}

// HasChanges reports whether anything differs.
func (d *Diff) HasChanges() bool {
	return d.Changed > 0 || len(d.New) > 0 || len(d.Removed) > 0
}

// DiffSnapshots compares two saved snapshots by name.
func (s *Store) DiffSnapshots(beforeName, afterName string) (*Diff, error) {
	beforeRaw, beforeEntry, err := s.Get(beforeName)
	if err != nil {
		return nil, err
	}
	afterRaw, afterEntry, err := s.Get(afterName)
	if err != nil {
		return nil, err
	}
	var beforeDoc, afterDoc map[string]any
	if err := json.Unmarshal(beforeRaw, &beforeDoc); err != nil {
		return nil, fmt.Errorf("snapshots: %q is not a JSON object: %w", beforeName, err)
	}
	if err := json.Unmarshal(afterRaw, &afterDoc); err != nil {
		return nil, fmt.Errorf("snapshots: %q is not a JSON object: %w", afterName, err)
	}
	return diffDocs(beforeEntry.Name, afterEntry.Name, beforeDoc, afterDoc), nil
}

// diffDocs compares two decoded reports.
func diffDocs(beforeName, afterName string, beforeDoc, afterDoc map[string]any) *Diff {
	d := &Diff{Before: beforeName, After: afterName}
	beforePts := indexPoints(extractPoints(beforeDoc))
	afterPts := indexPoints(extractPoints(afterDoc))
	names := map[string]bool{}
	for n := range beforePts {
		names[n] = true
	}
	for n := range afterPts {
		names[n] = true
	}
	ordered := make([]string, 0, len(names))
	for n := range names {
		ordered = append(ordered, n)
	}
	sort.Strings(ordered)
	for _, n := range ordered {
		b, inB := beforePts[n]
		a, inA := afterPts[n]
		switch {
		case inB && !inA:
			d.Removed = append(d.Removed, n)
			d.Checks = append(d.Checks, CheckDiff{Name: n, Before: b.status, After: "-", Changed: true, BeforeDetail: b.detail})
			d.Changed++
		case !inB && inA:
			d.New = append(d.New, n)
			d.Checks = append(d.Checks, CheckDiff{Name: n, Before: "-", After: a.status, Changed: true, AfterDetail: a.detail})
			d.Changed++
		default:
			cd := CheckDiff{Name: n, Before: b.status, After: a.status, BeforeDetail: b.detail, AfterDetail: a.detail}
			if b.status != a.status || b.detail != a.detail {
				cd.Changed = true
				d.Changed++
			}
			if b.status == "pass" && a.status == "fail" {
				d.PassFail = append(d.PassFail, n)
			}
			if b.status == "fail" && a.status == "pass" {
				d.FailPass = append(d.FailPass, n)
			}
			d.Checks = append(d.Checks, cd)
		}
	}
	return d
}

func indexPoints(pts []point) map[string]point {
	m := make(map[string]point, len(pts))
	for _, p := range pts {
		m[p.name] = p
	}
	return m
}
