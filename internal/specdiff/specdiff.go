// Package specdiff diffs AdCP spec-version expectation surfaces.
//
// The 3.1 surface is the baseline adcp-test checks against (derived from
// internal/conformance's expected tool surface). The 4.0-draft-expectations
// surface is a DRAFT expectation set encoding what is known publicly about
// the next revision — it is not a published spec, and every draft artifact
// is marked accordingly.
//
// Known 4.0-direction facts (AdCP Slack auth discussion, 2026-09-28 —
// public implementer discussion; NOT a published spec):
//   - RFC 9421 signatures become mandatory for spend/mutating operations.
//   - Multi-account agents: the buyer declares each brand + operator via
//     sync_accounts, and the seller checks per call that the account
//     belongs to the agent that signed the request.
//   - Authorization stays per account, not per user: the signature says
//     who is calling; what they may do is decided per account.
//   - Alternative model: require_operator_auth — the agent carries the
//     operator's OAuth credential and only sees that operator's accounts.
//
// specdiff answers "what would break if the spec moved from X to Y": as a
// pure surface diff (--surface-only, the default), or grounded against a
// live seller (--target runs conformance and flags 3.1-passing behavior
// that would fail under the newer expectations).
package specdiff

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sujanchalla0510/adcp-test/internal/conformance"
)

// Status marks whether a surface is a published spec or a draft
// expectation.
type Status string

const (
	// StatusPublished is a surface taken from a published spec revision.
	StatusPublished Status = "published"
	// StatusDraftExpectation is a surface encoding public discussion of
	// an unreleased revision. Not authoritative.
	StatusDraftExpectation Status = "draft-expectation"
)

// AuthRequirement is one authentication/authorization expectation.
type AuthRequirement struct {
	// Scope is the operation class: "mutating", "spend", "read".
	Scope string `json:"scope"`
	// Mechanism is how the caller authenticates, e.g. "rfc9421-signature".
	Mechanism string `json:"mechanism"`
	// Mandatory means the seller must enforce it; otherwise it is probed
	// opportunistically.
	Mandatory bool `json:"mandatory"`
	// Detail is human context.
	Detail string `json:"detail"`
	// Source cites where the expectation comes from. Expectations taken
	// from a published spec cite the spec; draft expectations cite the
	// discussion they were derived from and are not authoritative.
	Source string `json:"source,omitempty"`
}

// Surface is the expected tool + auth surface for one spec version.
type Surface struct {
	Version string                     `json:"version"`
	Status  Status                     `json:"status"`
	Note    string                     `json:"note"`
	Tools   []conformance.ExpectedTool `json:"tools"`
	Auth    []AuthRequirement          `json:"auth"`
}

// ChangeKind classifies one diff entry.
type ChangeKind string

const (
	ChangeAdded    ChangeKind = "added"
	ChangeRemoved  ChangeKind = "removed"
	ChangeRequired ChangeKind = "became-required"
	ChangeOptional ChangeKind = "became-optional"
	ChangeAuth     ChangeKind = "auth-changed"
)

// ToolChange is one tool-surface difference between two versions.
type ToolChange struct {
	Kind   ChangeKind `json:"kind"`
	Tool   string     `json:"tool"`
	Detail string     `json:"detail"`
	// Source cites where the expectation comes from (see
	// AuthRequirement.Source).
	Source string `json:"source,omitempty"`
}

// AuthChange is one auth-requirement difference between two versions.
type AuthChange struct {
	Kind      ChangeKind `json:"kind"`
	Scope     string     `json:"scope"`
	Mechanism string     `json:"mechanism"`
	Detail    string     `json:"detail"`
	// Source cites where the expectation comes from (see
	// AuthRequirement.Source).
	Source string `json:"source,omitempty"`
}

// Diff is the computed difference between two surfaces.
type Diff struct {
	From        string       `json:"from"`
	To          string       `json:"to"`
	ToStatus    Status       `json:"to_status"`
	ToolChanges []ToolChange `json:"tool_changes"`
	AuthChanges []AuthChange `json:"auth_changes"`
}

// Versions lists the known surface versions in order.
//
// "4.0-draft-expectations" is deliberately not called "4.0": there is no
// published AdCP 4.0 spec, and naming the version "4.0" would let the
// table be cited as the spec. It is a draft expectation set derived from
// public implementer discussion.
func Versions() []string { return []string{"3.1", "4.0-draft-expectations"} }

// draftVersion is the draft-expectations version name.
const draftVersion = "4.0-draft-expectations"

// draftSource cites the actual source of every draft expectation. The
// table is not a published spec and must never be cited as one.
const draftSource = "AdCP Slack auth discussion, 2026-09-28 (public implementer discussion; NOT a published spec)"

// Get returns the expectation surface for a known version.
func Get(version string) (*Surface, error) {
	switch version {
	case "3.1":
		return surface31(), nil
	case draftVersion:
		return surface40(), nil
	default:
		return nil, fmt.Errorf("specdiff: unknown version %q (known: %s)", version, strings.Join(Versions(), ", "))
	}
}

// surface31 is the AdCP 3.1 baseline: the v3 MCP task-based protocol with
// the RFC 9421 request-signing auth model.
func surface31() *Surface {
	return &Surface{
		Version: "3.1",
		Status:  StatusPublished,
		Note:    "AdCP 3.1: v3 MCP task-based protocol; RFC 9421 signing available for mutating calls, bearer tokens per account.",
		Tools:   conformance.CoreTools,
		Auth: []AuthRequirement{
			{Scope: "mutating", Mechanism: "rfc9421-signature", Mandatory: false,
				Detail: "Sellers should reject unsigned/malformed mutating calls with a structured error; probed, not mandatory."},
			{Scope: "read", Mechanism: "rfc9421-signature", Mandatory: false,
				Detail: "Read path (e.g. get_products) reachable without signing; behavior documented, not asserted."},
			{Scope: "mutating", Mechanism: "bearer-token", Mandatory: false,
				Detail: "Bearer token per buyer account is a common 3.1 pattern (one token = one account)."},
		},
	}
}

// surface40 is the draft 4.0 expectation set. DRAFT — not a published
// spec. It encodes the public implementer discussion cited in
// draftSource: mandatory signing for spend operations, multi-account
// agents via sync_accounts, per-call account-belongs-to-signer checks,
// and the require_operator_auth OAuth alternative.
func surface40() *Surface {
	tools := make([]conformance.ExpectedTool, 0, len(conformance.CoreTools))
	for _, t := range conformance.CoreTools {
		if t.Name == "sync_accounts" {
			// Multi-account agents declare each brand + operator here;
			// required once signing is mandatory for spend ops.
			t.Required = true
			t.Description = "account onboarding: buyer declares each brand + operator (required for multi-account agents)"
		}
		tools = append(tools, t)
	}
	return &Surface{
		Version: draftVersion,
		Status:  StatusDraftExpectation,
		Note:    "DRAFT expectation, not a published spec. " + draftSource + "; verify against the released spec before treating any entry as authoritative.",
		Tools:   tools,
		Auth: []AuthRequirement{
			{Scope: "spend", Mechanism: "rfc9421-signature", Mandatory: true, Source: draftSource,
				Detail: "RFC 9421 signatures become mandatory for spend operations (mutating calls that commit money)."},
			{Scope: "mutating", Mechanism: "rfc9421-signature", Mandatory: true, Source: draftSource,
				Detail: "Unsigned or malformed mutating calls must be rejected; optional in 3.1, mandatory in the draft 4.0 expectations."},
			{Scope: "spend", Mechanism: "account-ownership-check", Mandatory: true, Source: draftSource,
				Detail: "Per call, the seller checks that the account belongs to the agent that signed the request (signature says who is calling; authorization is per account)."},
			{Scope: "spend", Mechanism: "oauth-operator-auth", Mandatory: false, Source: draftSource,
				Detail: "Alternative model: require_operator_auth — the agent carries the operator's OAuth credential and only sees that operator's accounts."},
		},
	}
}

// DiffSurfaces computes the surface diff from one version to another.
func DiffSurfaces(from, to *Surface) *Diff {
	d := &Diff{From: from.Version, To: to.Version, ToStatus: to.Status}

	// changeSource cites the discussion behind any change that involves
	// a draft-expectations surface; changes between two published
	// surfaces need no source annotation.
	changeSource := ""
	if from.Status == StatusDraftExpectation || to.Status == StatusDraftExpectation {
		changeSource = draftSource
	}

	fromTools := map[string]conformance.ExpectedTool{}
	for _, t := range from.Tools {
		fromTools[t.Name] = t
	}
	toTools := map[string]conformance.ExpectedTool{}
	for _, t := range to.Tools {
		toTools[t.Name] = t
	}
	for name, ft := range fromTools {
		tt, ok := toTools[name]
		switch {
		case !ok:
			d.ToolChanges = append(d.ToolChanges, ToolChange{Kind: ChangeRemoved, Tool: name, Detail: "tool removed from the expected surface", Source: changeSource})
		case !ft.Required && tt.Required:
			d.ToolChanges = append(d.ToolChanges, ToolChange{Kind: ChangeRequired, Tool: name, Source: changeSource,
				Detail: fmt.Sprintf("optional in %s, required in %s: %s", from.Version, to.Version, tt.Description)})
		case ft.Required && !tt.Required:
			d.ToolChanges = append(d.ToolChanges, ToolChange{Kind: ChangeOptional, Tool: name, Detail: "no longer required", Source: changeSource})
		}
	}
	for name := range toTools {
		if _, ok := fromTools[name]; !ok {
			d.ToolChanges = append(d.ToolChanges, ToolChange{Kind: ChangeAdded, Tool: name, Detail: "new tool in the expected surface", Source: changeSource})
		}
	}
	sort.Slice(d.ToolChanges, func(i, j int) bool { return d.ToolChanges[i].Tool < d.ToolChanges[j].Tool })

	authKey := func(a AuthRequirement) string { return a.Scope + "|" + a.Mechanism }
	fromAuth := map[string]AuthRequirement{}
	for _, a := range from.Auth {
		fromAuth[authKey(a)] = a
	}
	for _, a := range to.Auth {
		k := authKey(a)
		fa, ok := fromAuth[k]
		switch {
		case !ok:
			d.AuthChanges = append(d.AuthChanges, AuthChange{Kind: ChangeAdded, Scope: a.Scope, Mechanism: a.Mechanism, Detail: a.Detail, Source: a.Source})
		case fa.Mandatory != a.Mandatory:
			d.AuthChanges = append(d.AuthChanges, AuthChange{Kind: ChangeAuth, Scope: a.Scope, Mechanism: a.Mechanism, Source: a.Source,
				Detail: fmt.Sprintf("optional in %s, mandatory in %s: %s", from.Version, to.Version, a.Detail)})
		}
		delete(fromAuth, k)
	}
	for _, a := range fromAuth {
		d.AuthChanges = append(d.AuthChanges, AuthChange{Kind: ChangeRemoved, Scope: a.Scope, Mechanism: a.Mechanism, Detail: "requirement dropped", Source: changeSource})
	}
	sort.Slice(d.AuthChanges, func(i, j int) bool {
		if d.AuthChanges[i].Scope != d.AuthChanges[j].Scope {
			return d.AuthChanges[i].Scope < d.AuthChanges[j].Scope
		}
		return d.AuthChanges[i].Mechanism < d.AuthChanges[j].Mechanism
	})
	return d
}

// TargetFinding is one "would break under the newer spec" finding from a
// live conformance run.
type TargetFinding struct {
	Severity string `json:"severity"` // "would-fail" or "attention"
	Check    string `json:"check"`
	Detail   string `json:"detail"`
}

// TargetReport grounds a surface diff against a live seller.
type TargetReport struct {
	Target   string          `json:"target"`
	From     string          `json:"from"`
	To       string          `json:"to"`
	ToStatus Status          `json:"to_status"`
	Diff     *Diff           `json:"diff"`
	Findings []TargetFinding `json:"findings"`
}

// maxTargetRun bounds the conformance run behind a target-mode diff.
const maxTargetRun = 3 * time.Minute

// DiffAgainstTarget runs conformance (the from-version checks) against
// target and flags behavior that would break under the to-version
// expectations.
func DiffAgainstTarget(ctx context.Context, target string, fromVersion, toVersion string, bearerToken string) (*TargetReport, error) {
	from, err := Get(fromVersion)
	if err != nil {
		return nil, err
	}
	to, err := Get(toVersion)
	if err != nil {
		return nil, err
	}
	d := DiffSurfaces(from, to)

	tctx, cancel := context.WithTimeout(ctx, maxTargetRun)
	defer cancel()
	rep, err := conformance.Run(tctx, target, conformance.Options{BearerToken: bearerToken})
	if err != nil {
		return nil, err
	}

	out := &TargetReport{Target: target, From: from.Version, To: to.Version, ToStatus: to.Status, Diff: d}

	// sync_accounts becomes required in 4.0: flag its absence — but only
	// when the surface check actually passed, so we know what we saw.
	surfaceOK, advertised := surfaceTools(rep)
	if surfaceOK && !advertised["sync_accounts"] {
		out.Findings = append(out.Findings, TargetFinding{
			Severity: "would-fail",
			Check:    "tool-surface",
			Detail:   "sync_accounts is not advertised; it becomes required in 4.0 for multi-account agents (brand + operator declaration)",
		})
	} else if !surfaceOK {
		out.Findings = append(out.Findings, TargetFinding{
			Severity: "attention",
			Check:    "tool-surface",
			Detail:   "tool surface could not be read; draft-4.0 readiness (e.g. sync_accounts) cannot be assessed",
		})
	}
	// Mandatory signing for spend ops: the 3.1 auth probes document what
	// 4.0 would enforce. A seller that accepts unsigned mutating calls
	// passes the 3.1 probe only if it rejects them — surface the probe
	// outcomes as 4.0 readiness signals.
	for _, c := range rep.Checks {
		switch c.Name {
		case "auth:unsigned-mutating-call-rejected":
			if c.Status == conformance.StatusFail {
				out.Findings = append(out.Findings, TargetFinding{
					Severity: "would-fail",
					Check:    c.Name,
					Detail:   "seller accepted (or mishandled) an unsigned mutating call; 4.0 makes RFC 9421 signatures mandatory for spend operations",
				})
			} else if c.Status == conformance.StatusPass {
				out.Findings = append(out.Findings, TargetFinding{
					Severity: "attention",
					Check:    c.Name,
					Detail:   "seller already rejects unsigned mutating calls — aligned with 4.0 mandatory signing",
				})
			}
		case "auth:malformed-signature-rejected":
			if c.Status == conformance.StatusFail {
				out.Findings = append(out.Findings, TargetFinding{
					Severity: "would-fail",
					Check:    c.Name,
					Detail:   "seller did not reject a malformed RFC 9421 signature; 4.0 mandates signature verification on mutating calls",
				})
			}
		}
	}
	if out.Findings == nil {
		out.Findings = []TargetFinding{}
	}
	return out, nil
}

// surfaceTools reports whether the tool-surface check passed and, when it
// did, which expected tools the seller advertised. Presence is derived
// from the check detail: on pass it reads "advertised N tools; all M
// required AdCP tools present" plus an optional
// "; optional tools absent (informational): a, b, c" trailer, so any
// expected tool NOT named in the trailer was advertised.
func surfaceTools(rep *conformance.Report) (bool, map[string]bool) {
	advertised := map[string]bool{}
	for _, c := range rep.Checks {
		if c.Name != "tool-surface" || c.Status != conformance.StatusPass {
			continue
		}
		for _, exp := range conformance.CoreTools {
			if !strings.Contains(c.Detail, exp.Name) {
				advertised[exp.Name] = true
			}
		}
		return true, advertised
	}
	return false, advertised
}
