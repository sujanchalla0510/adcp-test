// Package signdebug is an RFC 9421 (HTTP Message Signatures) signing
// debugger: given a captured request, its Signature-Input/Signature
// headers, and key material pasted in by the user, it reconstructs the
// signature base, verifies the signature with stdlib crypto, and produces
// a step-by-step diagnostic pinpointing what mismatched.
//
// Key material is strictly in-memory: it is never written to disk, never
// recorded into sessions, and never echoed in logs or reports (only the
// key type and key id appear in diagnostics).
package signdebug

import (
	"encoding/base64"
	"fmt"
	"strings"
	"time"
)

// Request is the captured HTTP request under debug.
type Request struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	// Body is the raw request body. BodyBase64 carries binary bodies.
	Body       []byte `json:"-"`
	BodyText   string `json:"body,omitempty"`
	BodyBase64 string `json:"body_base64,omitempty"`
}

// BodyBytes resolves the effective request body.
func (r *Request) BodyBytes() ([]byte, error) {
	if r.BodyBase64 != "" {
		return base64.StdEncoding.DecodeString(r.BodyBase64)
	}
	if r.Body != nil {
		return r.Body, nil
	}
	return []byte(r.BodyText), nil
}

// headerLookup returns a case-insensitive view of the request headers.
func (r *Request) headerLookup() map[string]string {
	out := make(map[string]string, len(r.Headers))
	for k, v := range r.Headers {
		out[strings.ToLower(k)] = v
	}
	return out
}

// Component is one covered component with the value used in the base.
type Component struct {
	Name   string `json:"name"`
	Params string `json:"params,omitempty"`
	Value  string `json:"value"`
	Note   string `json:"note,omitempty"`
}

// Check is one diagnostic step outcome.
type Check struct {
	Name     string `json:"name"`
	OK       bool   `json:"ok"`
	Detail   string `json:"detail,omitempty"`
	Expected string `json:"expected,omitempty"`
	Actual   string `json:"actual,omitempty"`
}

// Report is the full debugging diagnostic.
type Report struct {
	Verdict    string      `json:"verdict"` // "valid" or "invalid"
	Summary    string      `json:"summary"`
	Label      string      `json:"label"`
	Algorithm  string      `json:"algorithm"`
	KeyID      string      `json:"key_id,omitempty"`
	Base       string      `json:"signature_base"`
	Components []Component `json:"components"`
	Checks     []Check     `json:"checks"`
	Steps      []string    `json:"steps"`
}

// Options tunes a verification.
type Options struct {
	// ExpectedBase, when non-empty, is diffed line-by-line against the
	// reconstructed base to pinpoint exactly which covered component
	// differs (paste what your signer computed).
	ExpectedBase string
	// Now overrides the clock for created/expires checks.
	Now time.Time
}

// Verify reconstructs the RFC 9421 signature base for req and verifies
// the first signature in the Signature headers against keyPEM.
// keyPEM is a PEM-encoded public or private key (or raw bytes when the
// algorithm is hmac-sha256). It is used in memory only.
func Verify(req *Request, keyPEM []byte, opts Options) *Report {
	rep := &Report{Verdict: "invalid"}
	step := func(s string) { rep.Steps = append(rep.Steps, s) }
	check := func(c Check) { rep.Checks = append(rep.Checks, c) }
	fail := func(summary string) {
		rep.Summary = summary
	}

	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	headers := req.headerLookup()

	sigInputRaw, ok := headers["signature-input"]
	if !ok || strings.TrimSpace(sigInputRaw) == "" {
		check(Check{Name: "signature-input-present", OK: false, Detail: "no Signature-Input header in the request"})
		fail("no Signature-Input header present")
		return rep
	}
	sigRaw, ok := headers["signature"]
	if !ok || strings.TrimSpace(sigRaw) == "" {
		check(Check{Name: "signature-present", OK: false, Detail: "no Signature header in the request"})
		fail("no Signature header present")
		return rep
	}

	// 1. Parse Signature-Input.
	inputs, err := parseDict(sigInputRaw)
	step("parsed Signature-Input dictionary")
	if err != nil {
		check(Check{Name: "signature-input-parse", OK: false, Detail: err.Error()})
		fail("Signature-Input does not parse: " + err.Error())
		return rep
	}
	if len(inputs) == 0 {
		check(Check{Name: "signature-input-parse", OK: false, Detail: "empty Signature-Input dictionary"})
		fail("empty Signature-Input dictionary")
		return rep
	}
	label := inputs[0].Key
	covered := inputs[0].InnerList
	params := inputs[0].Params
	rep.Label = label
	rep.Algorithm = unquote(params["alg"])
	rep.KeyID = unquote(params["keyid"])
	check(Check{Name: "signature-input-parse", OK: true,
		Detail: fmt.Sprintf("label %q covers %d component(s): %s", label, len(covered), componentNames(covered))})
	step(fmt.Sprintf("using signature label %q", label))

	// 2. Parse Signature.
	sigs, err := parseDict(sigRaw)
	if err != nil {
		check(Check{Name: "signature-parse", OK: false, Detail: "Signature header does not parse: " + err.Error()})
		fail("Signature header does not parse: " + err.Error())
		return rep
	}
	var sigBytes []byte
	for _, s := range sigs {
		if s.Key == label {
			b, err := parseByteSequence(s.Value)
			if err != nil {
				check(Check{Name: "signature-parse", OK: false, Detail: fmt.Sprintf("signature %q is not a byte sequence: %v", label, err)})
				fail("signature value is not a byte sequence")
				return rep
			}
			sigBytes = b
		}
	}
	if sigBytes == nil {
		check(Check{Name: "signature-parse", OK: false,
			Detail: fmt.Sprintf("no Signature entry for label %q", label)})
		fail(fmt.Sprintf("no Signature entry for label %q", label))
		return rep
	}
	check(Check{Name: "signature-parse", OK: true,
		Detail: fmt.Sprintf("decoded %d signature bytes for label %q", len(sigBytes), label)})
	step("decoded signature bytes")

	// 3. Parse key material (in memory only; never echoed).
	pub, keyDesc, keyErr := parseKey(keyPEM, rep.Algorithm)
	if keyErr != nil {
		check(Check{Name: "key-parse", OK: false, Detail: keyErr.Error()})
		fail("key material does not parse: " + keyErr.Error())
		return rep
	}
	check(Check{Name: "key-parse", OK: true, Detail: "parsed " + keyDesc + " (key bytes never persisted or logged)"})
	step("parsed key material (" + keyDesc + ")")

	// 4. Algorithm vs key agreement.
	alg := rep.Algorithm
	if alg == "" {
		alg = inferAlgorithm(pub)
		rep.Algorithm = alg
		step("no alg parameter; inferred " + alg + " from key type")
	}
	algOK, algDetail := checkAlgorithm(alg, pub)
	check(Check{Name: "algorithm", OK: algOK, Detail: algDetail})

	// 5. Reconstruct covered components.
	body, err := req.BodyBytes()
	if err != nil {
		check(Check{Name: "request-body", OK: false, Detail: "body_base64 does not decode: " + err.Error()})
		fail("request body does not decode")
		return rep
	}
	comps, compErr := buildComponents(req, headers, covered, body)
	for _, c := range comps {
		rep.Components = append(rep.Components, c)
	}
	if compErr != nil {
		check(Check{Name: "component-reconstruction", OK: false, Detail: compErr.Error()})
		fail("cannot reconstruct covered components: " + compErr.Error())
		return rep
	}
	check(Check{Name: "component-reconstruction", OK: true,
		Detail: fmt.Sprintf("reconstructed %d covered component values from the request as received", len(comps))})
	step("reconstructed covered components")

	// 6. Build the signature base.
	base := buildBase(comps, covered, inputs[0].RawValue)
	rep.Base = base
	step("built canonical signature base")

	// 7. content-digest cross-check (independently verifiable).
	checkDigest(req, headers, body, covered, check)

	// 8. created / expires.
	checkExpiry(params, now, check)

	// 9. Optional expected-base diff: pinpoints the exact component.
	if strings.TrimSpace(opts.ExpectedBase) != "" {
		diffCheck(rep.Base, opts.ExpectedBase, check)
		step("diffed reconstructed base against the expected base")
	}

	// 10. Verify the signature.
	verr := verifySignature(alg, pub, keyPEM, []byte(base), sigBytes)
	if verr != nil {
		detail := "signature does not verify over the reconstructed base"
		causes := likelyCauses(rep)
		if len(causes) > 0 {
			detail += "; likely causes: " + strings.Join(causes, "; ")
		} else {
			detail += ": at least one covered component differs from signing time, or the wrong key was supplied"
		}
		check(Check{Name: "signature-verify", OK: false, Detail: detail})
		fail("INVALID: " + detail)
		return rep
	}
	check(Check{Name: "signature-verify", OK: true, Detail: "signature verifies over the reconstructed base"})

	// Gate the verdict on every check: a signature can verify over the
	// reconstructed base while the request still differs from what was
	// signed (e.g. a body tampered after signing, caught by the
	// content-digest cross-check). The expected-base diff is advisory —
	// it compares against user-pasted text, not the signature — so it
	// does not gate the verdict.
	for _, c := range rep.Checks {
		if c.Name == "expected-base-diff" {
			continue
		}
		if !c.OK {
			fail(fmt.Sprintf("INVALID: signature verifies, but the %s check failed: %s", c.Name, c.Detail))
			return rep
		}
	}
	rep.Verdict = "valid"
	rep.Summary = fmt.Sprintf("VALID: signature %q verifies (%s)", label, algDetailShort(alg, keyDesc))
	return rep
}

func componentNames(items []innerItem) string {
	names := make([]string, 0, len(items))
	for _, it := range items {
		names = append(names, it.Value)
	}
	return strings.Join(names, ", ")
}

// likelyCauses collects failed check names to hint at the root cause of
// a verification failure.
func likelyCauses(rep *Report) []string {
	var out []string
	for _, c := range rep.Checks {
		if !c.OK {
			switch c.Name {
			case "content-digest":
				out = append(out, "request body differs from signing time (content-digest mismatch)")
			case "expiry":
				out = append(out, "signature expired or not yet valid")
			case "algorithm":
				out = append(out, "algorithm/key mismatch")
			case "expected-base-diff":
				out = append(out, "covered component values differ (see diff)")
			}
		}
	}
	return out
}

func algDetailShort(alg, keyDesc string) string {
	if alg == "" {
		return keyDesc
	}
	return alg + " with " + keyDesc
}
