package snapshots

import (
	"encoding/json"
	"strings"
	"testing"
)

func confReport(checks ...[3]string) json.RawMessage {
	// each check: {name, status, detail}
	type check struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		Detail string `json:"detail"`
	}
	list := make([]check, len(checks))
	for i, c := range checks {
		list[i] = check{Name: c[0], Status: c[1], Detail: c[2]}
	}
	raw, _ := json.Marshal(map[string]any{
		"target_url": "http://127.0.0.1:1/",
		"checks":     list,
	})
	return raw
}

func TestSaveListGet(t *testing.T) {
	s := New(t.TempDir())
	e, err := s.Save("conformance", "baseline run", confReport(
		[3]string{"a", "pass", "ok"},
		[3]string{"b", "pass", "ok"},
	))
	if err != nil {
		t.Fatal(err)
	}
	if e.Name != "baseline-run" {
		t.Errorf("name not sanitized: %q", e.Name)
	}
	if !strings.Contains(e.Summary, "2/2") {
		t.Errorf("summary %q", e.Summary)
	}
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "baseline-run" {
		t.Fatalf("list: %+v", list)
	}
	raw, got, err := s.Get("baseline run")
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != "conformance" {
		t.Errorf("kind %q", got.Kind)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
}

func TestSaveRejectsBadJSON(t *testing.T) {
	s := New(t.TempDir())
	if _, err := s.Save("conformance", "x", json.RawMessage(`{oops`)); err == nil {
		t.Error("expected an error for invalid JSON")
	}
	if _, err := s.Save("conformance", "!!!", json.RawMessage(`{}`)); err == nil {
		t.Error("expected an error for an empty sanitized name")
	}
}

func TestDiffPassFail(t *testing.T) {
	s := New(t.TempDir())
	if _, err := s.Save("conformance", "before", confReport(
		[3]string{"a", "pass", "ok"},
		[3]string{"b", "pass", "ok"},
		[3]string{"c", "fail", "broken"},
	)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save("conformance", "after", confReport(
		[3]string{"a", "fail", "now broken"},
		[3]string{"b", "pass", "ok"},
		[3]string{"c", "pass", "fixed"},
		[3]string{"d", "pass", "new check"},
	)); err != nil {
		t.Fatal(err)
	}
	d, err := s.DiffSnapshots("before", "after")
	if err != nil {
		t.Fatal(err)
	}
	if !d.HasChanges() {
		t.Error("expected changes")
	}
	if len(d.PassFail) != 1 || d.PassFail[0] != "a" {
		t.Errorf("pass->fail: %v", d.PassFail)
	}
	if len(d.FailPass) != 1 || d.FailPass[0] != "c" {
		t.Errorf("fail->pass: %v", d.FailPass)
	}
	if len(d.New) != 1 || d.New[0] != "d" {
		t.Errorf("new: %v", d.New)
	}
	if len(d.Removed) != 0 {
		t.Errorf("removed: %v", d.Removed)
	}
	// b is unchanged.
	for _, c := range d.Checks {
		if c.Name == "b" && c.Changed {
			t.Error("check b should be unchanged")
		}
	}
}

func TestDiffNoChanges(t *testing.T) {
	s := New(t.TempDir())
	r := confReport([3]string{"a", "pass", "ok"})
	if _, err := s.Save("conformance", "one", r); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save("conformance", "two", r); err != nil {
		t.Fatal(err)
	}
	d, err := s.DiffSnapshots("one", "two")
	if err != nil {
		t.Fatal(err)
	}
	if d.HasChanges() {
		t.Errorf("expected no changes, got %+v", d)
	}
}

func TestDiffScenarioSteps(t *testing.T) {
	s := New(t.TempDir())
	mk := func(stepPass bool) json.RawMessage {
		raw, _ := json.Marshal(map[string]any{
			"pack": "x",
			"scenarios": []any{
				map[string]any{"name": "s1", "passed": stepPass, "steps": []any{
					map[string]any{"name": "step one", "passed": stepPass},
				}},
			},
		})
		return raw
	}
	if _, err := s.Save("scenario", "before", mk(true)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save("scenario", "after", mk(false)); err != nil {
		t.Fatal(err)
	}
	d, err := s.DiffSnapshots("before", "after")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.PassFail) != 1 || d.PassFail[0] != "s1 / step one" {
		t.Errorf("pass->fail: %v", d.PassFail)
	}
}

func TestDelete(t *testing.T) {
	s := New(t.TempDir())
	if _, err := s.Save("conformance", "tmp", confReport([3]string{"a", "pass", "ok"})); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("tmp"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Get("tmp"); err == nil {
		t.Error("expected Get to fail after delete")
	}
	if err := s.Delete("tmp"); err == nil {
		t.Error("expected Delete to fail for a missing snapshot")
	}
}
