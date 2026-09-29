package signdebug

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"strings"
	"testing"
	"time"
)

// signedFixture builds a request with a real RFC 9421 Ed25519 signature
// over (@method, @target-uri, content-digest), created with stdlib
// crypto only. It returns the request, the PEM public key, and the exact
// signature base that was signed.
func signedFixture(t *testing.T, body string, created int64) (*Request, []byte, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})

	digest := "sha-256=:" + base64.StdEncoding.EncodeToString(sha256Of(body)) + ":"
	inner := fmt.Sprintf(`("@method" "@target-uri" "content-digest");created=%d;keyid="test-key";alg="ed25519"`, created)
	base := fmt.Sprintf("\"@method\": POST\n\"@target-uri\": https://seller.example/mcp?x=1\n\"content-digest\": %s\n\"@signature-params\": %s",
		digest, inner)
	sig := ed25519.Sign(priv, []byte(base))

	req := &Request{
		Method: "POST",
		URL:    "https://seller.example/mcp?x=1",
		Headers: map[string]string{
			"Content-Digest":  digest,
			"Signature-Input": "sig1=" + inner,
			"Signature":       "sig1=:" + base64.StdEncoding.EncodeToString(sig) + ":",
		},
		BodyText: body,
	}
	return req, keyPEM, base
}

func sha256Of(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}

func findCheck(rep *Report, name string) *Check {
	for i := range rep.Checks {
		if rep.Checks[i].Name == name {
			return &rep.Checks[i]
		}
	}
	return nil
}

func TestVerifyValid(t *testing.T) {
	req, keyPEM, _ := signedFixture(t, `{"hello":"world"}`, time.Now().Unix())
	rep := Verify(req, keyPEM, Options{})
	if rep.Verdict != "valid" {
		t.Fatalf("expected valid, got %q (%s); checks: %+v", rep.Verdict, rep.Summary, rep.Checks)
	}
	for _, c := range rep.Checks {
		if !c.OK {
			t.Errorf("check %q failed: %s", c.Name, c.Detail)
		}
	}
	if !strings.Contains(rep.Base, `"@signature-params": ("@method" "@target-uri" "content-digest")`) {
		t.Errorf("base missing @signature-params line:\n%s", rep.Base)
	}
}

func TestVerifyTamperedBodyPinpointed(t *testing.T) {
	req, keyPEM, _ := signedFixture(t, `{"hello":"world"}`, time.Now().Unix())
	// Tamper exactly one byte of the body after signing.
	req.BodyText = `{"hello":"worle"}`
	rep := Verify(req, keyPEM, Options{})
	if rep.Verdict != "invalid" {
		t.Fatalf("expected invalid after tamper, got %q", rep.Verdict)
	}
	dc := findCheck(rep, "content-digest")
	if dc == nil || dc.OK {
		t.Fatalf("expected a failing content-digest check, got %+v", dc)
	}
	if dc.Expected == "" || dc.Actual == "" || dc.Expected == dc.Actual {
		t.Fatalf("diagnostic must show expected vs actual digests, got %+v", dc)
	}
	// The signature itself still verifies over the reconstructed base
	// (the base embeds the header's digest verbatim); the pinpoint is
	// the content-digest cross-check, which is what gates the verdict.
	sv := findCheck(rep, "signature-verify")
	if sv == nil || !sv.OK {
		t.Fatalf("signature should still verify over the reconstructed base, got %+v", sv)
	}
	if !strings.Contains(rep.Summary, "content-digest") {
		t.Fatalf("summary should name content-digest as the failure: %s", rep.Summary)
	}
}

func TestVerifyExpired(t *testing.T) {
	req, keyPEM, _ := signedFixture(t, `{"hello":"world"}`, time.Now().Add(-2*time.Hour).Unix())
	// Add expires in the past by rewriting the Signature-Input.
	req.Headers["Signature-Input"] = strings.Replace(
		req.Headers["Signature-Input"], ";alg=", fmt.Sprintf(";expires=%d;alg=", time.Now().Add(-time.Hour).Unix()), 1)
	// The signature no longer matches (params changed), but the expiry
	// check must still fire independently.
	rep := Verify(req, keyPEM, Options{})
	ec := findCheck(rep, "expiry")
	if ec == nil || ec.OK {
		t.Fatalf("expected failing expiry check, got %+v", ec)
	}
	if !strings.Contains(ec.Detail, "expired") {
		t.Fatalf("unexpected expiry detail: %s", ec.Detail)
	}
}

func TestVerifyWrongKey(t *testing.T) {
	req, _, _ := signedFixture(t, `{"hello":"world"}`, time.Now().Unix())
	otherPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(otherPub)
	otherPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	rep := Verify(req, otherPEM, Options{})
	if rep.Verdict != "invalid" {
		t.Fatalf("expected invalid with the wrong key, got %q", rep.Verdict)
	}
}

func TestVerifyMalformedInput(t *testing.T) {
	req, keyPEM, _ := signedFixture(t, `{"hello":"world"}`, time.Now().Unix())
	req.Headers["Signature-Input"] = `sig1=("@method" "@target-uri";created=oops`
	rep := Verify(req, keyPEM, Options{})
	if rep.Verdict != "invalid" {
		t.Fatalf("expected invalid, got %q", rep.Verdict)
	}
	pc := findCheck(rep, "signature-input-parse")
	if pc == nil || pc.OK {
		t.Fatalf("expected failing parse check, got %+v", pc)
	}
}

func TestVerifyMissingHeaders(t *testing.T) {
	req := &Request{Method: "GET", URL: "https://seller.example/mcp", Headers: map[string]string{}}
	rep := Verify(req, []byte("whatever"), Options{})
	if rep.Verdict != "invalid" {
		t.Fatalf("expected invalid, got %q", rep.Verdict)
	}
}

func TestExpectedBaseDiffPinpoints(t *testing.T) {
	req, keyPEM, base := signedFixture(t, `{"hello":"world"}`, time.Now().Unix())
	// The "signer" saw a different @target-uri; diff must name it.
	tampered := strings.Replace(base, "https://seller.example/mcp?x=1", "https://seller.example/other", 1)
	rep := Verify(req, keyPEM, Options{ExpectedBase: tampered})
	dc := findCheck(rep, "expected-base-diff")
	if dc == nil || dc.OK {
		t.Fatalf("expected failing diff check, got %+v", dc)
	}
	if !strings.Contains(dc.Expected, "@target-uri") {
		t.Fatalf("diff should pinpoint @target-uri, got %q", dc.Expected)
	}
	if rep.Verdict != "valid" {
		t.Fatalf("signature itself is still valid; verdict should stay valid, got %q", rep.Verdict)
	}
}

func TestVerifyKeyMaterialNeverEchoed(t *testing.T) {
	req, keyPEM, _ := signedFixture(t, `{"hello":"world"}`, time.Now().Unix())
	rep := Verify(req, keyPEM, Options{})
	blob := fmt.Sprintf("%+v", rep)
	for _, frag := range []string{"PRIVATE", "-----BEGIN", base64.StdEncoding.EncodeToString(keyPEM)} {
		if frag != "" && strings.Contains(blob, frag) {
			t.Fatalf("report echoes key material %q", frag)
		}
	}
}
