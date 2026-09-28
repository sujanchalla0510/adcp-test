package mockserver

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRenderTemplate(t *testing.T) {
	ctx := templateContext{
		Tool:     "create_media_buy",
		ID:       json.RawMessage(`42`),
		Args:     map[string]any{"buyer_ref": "acme", "n": 3, "deep": map[string]any{"x": true}},
		Call:     5,
		Now:      time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC),
		State:    "active",
		EntityID: "mb-1",
	}
	out, err := renderTemplate(
		`{"buyer":"{{args.buyer_ref}}","n":{{args.n}},"deep":{{args.deep}},"tool":"{{tool}}","id":{{id}},"call":{{call}},"at":"{{now}}","state":"{{state}}","eid":"{{entity_id}}"}`,
		ctx,
	)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("rendered invalid JSON %q: %v", out, err)
	}
	if v["buyer"] != "acme" || v["tool"] != "create_media_buy" {
		t.Fatalf("echo: %v", v)
	}
	if v["n"] != float64(3) {
		t.Fatalf("n = %v (%T), want number 3", v["n"], v["n"])
	}
	if v["deep"].(map[string]any)["x"] != true {
		t.Fatalf("deep = %v", v["deep"])
	}
	if v["id"] != float64(42) || v["call"] != float64(5) {
		t.Fatalf("id/call = %v/%v", v["id"], v["call"])
	}
	if v["at"] != "2026-09-28T12:00:00Z" {
		t.Fatalf("at = %v", v["at"])
	}
	if v["state"] != "active" || v["eid"] != "mb-1" {
		t.Fatalf("state/eid = %v/%v", v["state"], v["eid"])
	}
}

func TestRenderTemplateEscaping(t *testing.T) {
	ctx := templateContext{Args: map[string]any{"name": `a"b\c` + "\n"}}
	out, err := renderTemplate(`{"name":"{{args.name}}"}`, ctx)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("bad escape %q: %v", out, err)
	}
	if v["name"] != "a\"b\\c\n" {
		t.Fatalf("name = %q", v["name"])
	}
}

func TestRenderTemplateErrors(t *testing.T) {
	ctx := templateContext{Args: map[string]any{}}
	for _, tmpl := range []string{
		`{{unclosed`,
		`x }} stray`,
		`{{nope}}`,
		`{{args.missing}}`,
	} {
		if _, err := renderTemplate(tmpl, ctx); err == nil {
			t.Errorf("template %q: expected error", tmpl)
		} else if !strings.Contains(err.Error(), "unclosed") && !strings.Contains(err.Error(), "stray") &&
			!strings.Contains(err.Error(), "unknown placeholder") && !strings.Contains(err.Error(), "unknown placeholder path") {
			t.Errorf("template %q: unexpected error %v", tmpl, err)
		}
	}
}
