package specdiff

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sujanchalla0510/adcp-test/internal/mockcfg"
	"github.com/sujanchalla0510/adcp-test/internal/mockserver"
)

func TestGetKnownVersions(t *testing.T) {
	for _, v := range Versions() {
		s, err := Get(v)
		if err != nil {
			t.Fatalf("Get(%q): %v", v, err)
		}
		if s.Version != v || len(s.Tools) == 0 {
			t.Errorf("Get(%q): bad surface %+v", v, s.Version)
		}
	}
	if _, err := Get("9.9"); err == nil {
		t.Error("Get(9.9) should fail")
	}
}

func TestSurface31IsPublishedBaseline(t *testing.T) {
	s, _ := Get("3.1")
	if s.Status != StatusPublished {
		t.Errorf("3.1 status = %q, want published", s.Status)
	}
}

func TestSurface40IsDraft(t *testing.T) {
	s, _ := Get("4.0-draft-expectations")
	if s.Status != StatusDraftExpectation {
		t.Errorf("4.0 status = %q, want draft-expectation", s.Status)
	}
	// sync_accounts must flip to required in the 4.0 draft.
	var found bool
	for _, tl := range s.Tools {
		if tl.Name == "sync_accounts" {
			found = true
			if !tl.Required {
				t.Error("4.0 draft: sync_accounts should be required")
			}
		}
	}
	if !found {
		t.Error("4.0 draft: sync_accounts missing from tools")
	}
	// Mandatory RFC 9421 for spend ops must be present.
	var mandatorySpend bool
	for _, a := range s.Auth {
		if a.Scope == "spend" && a.Mechanism == "rfc9421-signature" && a.Mandatory {
			mandatorySpend = true
		}
	}
	if !mandatorySpend {
		t.Error("4.0 draft: missing mandatory rfc9421-signature for spend scope")
	}
}

func TestDiffSurfaces31To40(t *testing.T) {
	from, _ := Get("3.1")
	to, _ := Get("4.0-draft-expectations")
	d := DiffSurfaces(from, to)
	if d.From != "3.1" || d.To != "4.0-draft-expectations" {
		t.Fatalf("bad diff endpoints: %+v", d)
	}
	if d.ToStatus != StatusDraftExpectation {
		t.Errorf("diff ToStatus = %q, want draft-expectation", d.ToStatus)
	}
	// sync_accounts: optional -> required.
	var sawRequired bool
	for _, c := range d.ToolChanges {
		if c.Tool == "sync_accounts" && c.Kind == ChangeRequired {
			sawRequired = true
		}
	}
	if !sawRequired {
		t.Errorf("diff missing sync_accounts became-required; changes: %+v", d.ToolChanges)
	}
	// mutating rfc9421: optional -> mandatory.
	var sawAuth bool
	for _, c := range d.AuthChanges {
		if c.Scope == "mutating" && c.Mechanism == "rfc9421-signature" && c.Kind == ChangeAuth {
			sawAuth = true
		}
	}
	if !sawAuth {
		t.Errorf("diff missing mutating rfc9421 auth change; changes: %+v", d.AuthChanges)
	}
}

func TestDiffSurfacesIdentity(t *testing.T) {
	s, _ := Get("3.1")
	d := DiffSurfaces(s, s)
	if len(d.ToolChanges) != 0 || len(d.AuthChanges) != 0 {
		t.Errorf("identity diff should be empty, got %+v", d)
	}
}

// startExampleSeller loads the worked-example mock seller on an ephemeral
// port.
func startExampleSeller(t *testing.T) string {
	t.Helper()
	cfg, err := mockcfg.LoadFile("../../examples/mock-seller.yaml")
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	svcs := cfg.Services()
	if len(svcs) == 0 {
		t.Fatal("example config has no services")
	}
	svc := svcs[0]
	svc.Listen = ":0"
	srv, err := mockserver.New(svc, mockserver.Options{BaseDir: cfg.BaseDir()})
	if err != nil {
		t.Fatalf("mockserver.New: %v", err)
	}
	url, err := srv.Start()
	if err != nil {
		t.Fatalf("srv.Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return url
}

func TestDiffAgainstTarget(t *testing.T) {
	target := startExampleSeller(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	rep, err := DiffAgainstTarget(ctx, target, "3.1", "4.0-draft-expectations", "")
	if err != nil {
		t.Fatalf("DiffAgainstTarget: %v", err)
	}
	if rep.ToStatus != StatusDraftExpectation {
		t.Errorf("ToStatus = %q, want draft-expectation", rep.ToStatus)
	}
	// The example seller does not advertise sync_accounts -> would-fail.
	var sawSyncAccounts bool
	for _, f := range rep.Findings {
		if strings.Contains(f.Detail, "sync_accounts") && f.Severity == "would-fail" {
			sawSyncAccounts = true
		}
	}
	if !sawSyncAccounts {
		t.Errorf("expected a sync_accounts would-fail finding; findings: %+v", rep.Findings)
	}
	// The example seller rejects unsigned mutating calls -> attention.
	var sawAttention bool
	for _, f := range rep.Findings {
		if f.Check == "auth:unsigned-mutating-call-rejected" && f.Severity == "attention" {
			sawAttention = true
		}
	}
	if !sawAttention {
		t.Errorf("expected an attention finding for unsigned-mutating-call-rejected; findings: %+v", rep.Findings)
	}
}

func TestDiffAgainstTargetBadVersion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := DiffAgainstTarget(ctx, "http://127.0.0.1:1", "3.1", "9.9", ""); err == nil {
		t.Error("unknown to-version should fail")
	}
}

func TestDraftVersionNameIsNotBare40(t *testing.T) {
	if _, err := Get("4.0"); err == nil {
		t.Fatal(`Get("4.0"): expected error; the draft expectations must not be addressable as "4.0"`)
	}
}

func TestDraftChangesCarrySource(t *testing.T) {
	from, _ := Get("3.1")
	to, _ := Get("4.0-draft-expectations")
	d := DiffSurfaces(from, to)
	if len(d.ToolChanges) == 0 && len(d.AuthChanges) == 0 {
		t.Fatal("expected some changes between 3.1 and the draft expectations")
	}
	for _, c := range d.ToolChanges {
		if c.Source == "" {
			t.Errorf("tool change %s has no source citation", c.Tool)
		} else if !containsNotPublished(c.Source) {
			t.Errorf("tool change %s source does not disclaim spec status: %q", c.Tool, c.Source)
		}
	}
	for _, c := range d.AuthChanges {
		if c.Source == "" {
			t.Errorf("auth change %s|%s has no source citation", c.Scope, c.Mechanism)
		} else if !containsNotPublished(c.Source) {
			t.Errorf("auth change %s|%s source does not disclaim spec status: %q", c.Scope, c.Mechanism, c.Source)
		}
	}
	for _, a := range to.Auth {
		if a.Source == "" {
			t.Errorf("draft auth requirement %s|%s has no source citation", a.Scope, a.Mechanism)
		}
	}
}

func containsNotPublished(s string) bool {
	return strings.Contains(s, "NOT a published spec")
}
