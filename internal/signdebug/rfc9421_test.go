package signdebug

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// edFixture builds a request signed with Ed25519 over the given inner
// list and base lines. It returns the request, the PEM public key, the
// private key (so tests can re-sign), and the exact base that was signed.
func edFixture(t *testing.T, inner string, baseLines ...string) (*Request, []byte, ed25519.PrivateKey, string) {
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
	base := strings.Join(baseLines, "\n") + "\n" + `"@signature-params": ` + inner
	sig := ed25519.Sign(priv, []byte(base))
	req := &Request{
		Method: "POST",
		URL:    "https://seller.example/mcp",
		Headers: map[string]string{
			"Signature-Input": "sig1=" + inner,
			"Signature":       "sig1=:" + base64.StdEncoding.EncodeToString(sig) + ":",
		},
	}
	return req, keyPEM, priv, base
}

// ecdsaFixture builds a request signed with ECDSA (raw r‖s, RFC 9421
// §3.3.2) over ("@method" "@target-uri"). der=true encodes the signature
// as ASN.1 DER instead, which must be rejected.
func ecdsaFixture(t *testing.T, der bool, created int64) (*Request, []byte) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})
	inner := fmt.Sprintf(`("@method" "@target-uri");created=%d;keyid="ec-key";alg="ecdsa-p256-sha256"`, created)
	base := fmt.Sprintf("\"@method\": POST\n\"@target-uri\": https://seller.example/mcp\n\"@signature-params\": %s", inner)
	sum := sha256.Sum256([]byte(base))
	r, s, err := ecdsa.Sign(rand.Reader, priv, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	var sig []byte
	if der {
		sig = derEncode(r, s)
	} else {
		sig = make([]byte, 64)
		rb, sb := r.Bytes(), s.Bytes()
		copy(sig[32-len(rb):32], rb)
		copy(sig[64-len(sb):], sb)
	}
	req := &Request{
		Method: "POST",
		URL:    "https://seller.example/mcp",
		Headers: map[string]string{
			"Signature-Input": "sig1=" + inner,
			"Signature":       "sig1=:" + base64.StdEncoding.EncodeToString(sig) + ":",
		},
	}
	return req, keyPEM
}

// derEncode is a minimal DER SEQUENCE of two INTEGERs, for the negative
// test only.
func derEncode(r, s *big.Int) []byte {
	encInt := func(v *big.Int) []byte {
		b := v.Bytes()
		if len(b) > 0 && b[0]&0x80 != 0 {
			b = append([]byte{0}, b...)
		}
		return append([]byte{0x02, byte(len(b))}, b...)
	}
	body := append(encInt(r), encInt(s)...)
	return append([]byte{0x30, byte(len(body))}, body...)
}

func TestVerifyECDSARawRS(t *testing.T) {
	req, keyPEM := ecdsaFixture(t, false, time.Now().Unix())
	rep := Verify(req, keyPEM, Options{Now: time.Now()})
	if rep.Verdict != "valid" {
		t.Fatalf("expected valid, got %s: %s", rep.Verdict, rep.Summary)
	}
	if c := findCheck(rep, "adcp-profile"); c == nil || !strings.Contains(c.Detail, "within the AdCP 3.1") {
		t.Fatalf("expected adcp-profile check to pass for ecdsa-p256-sha256, got %+v", c)
	}
}

func TestVerifyECDSADERRejected(t *testing.T) {
	req, keyPEM := ecdsaFixture(t, true, time.Now().Unix())
	rep := Verify(req, keyPEM, Options{Now: time.Now()})
	if rep.Verdict != "invalid" {
		t.Fatalf("expected invalid for DER signature, got %s", rep.Verdict)
	}
	c := findCheck(rep, "signature-verify")
	if c == nil || c.OK {
		t.Fatalf("expected failing signature-verify check, got %+v", c)
	}
	if !strings.Contains(rep.Summary, "raw r‖s") {
		t.Fatalf("expected raw r‖s guidance in summary, got: %s", rep.Summary)
	}
}

func TestVerifyECDSAWrongLength(t *testing.T) {
	req, keyPEM := ecdsaFixture(t, false, time.Now().Unix())
	// Truncate the signature to 32 bytes: wrong length for P-256.
	req.Headers["Signature"] = "sig1=:" + base64.StdEncoding.EncodeToString(make([]byte, 32)) + ":"
	rep := Verify(req, keyPEM, Options{Now: time.Now()})
	if rep.Verdict != "invalid" {
		t.Fatalf("expected invalid for short signature, got %s", rep.Verdict)
	}
	if !strings.Contains(rep.Summary, "64 octets") {
		t.Fatalf("expected length guidance, got: %s", rep.Summary)
	}
}

func TestQueryAbsentIsQuestionMark(t *testing.T) {
	created := time.Now().Unix()
	inner := fmt.Sprintf(`("@query");created=%d;keyid="test-key";alg="ed25519"`, created)
	// The signer signs "?" per RFC 9421 §2.2.7 (no query in the URL).
	req, keyPEM, _, _ := edFixture(t, inner, `"@query": ?`)
	rep := Verify(req, keyPEM, Options{Now: time.Now()})
	if rep.Verdict != "valid" {
		t.Fatalf("expected valid, got %s: %s", rep.Verdict, rep.Summary)
	}
	if len(rep.Components) != 1 || rep.Components[0].Value != "?" {
		t.Fatalf("expected @query component value \"?\", got %+v", rep.Components)
	}
}

func TestQueryParamPercentEncoding(t *testing.T) {
	created := time.Now().Unix()
	// lang=en us (raw space in the wire query as %20): the covered value
	// must be re-encoded as en%20us, never en+us.
	inner := fmt.Sprintf(`("@query-param";name="lang");created=%d;keyid="test-key";alg="ed25519"`, created)
	req, keyPEM, _, _ := edFixture(t, inner, `"@query-param";name="lang": en%20us`)
	req.URL = "https://seller.example/mcp?lang=en%20us"
	rep := Verify(req, keyPEM, Options{Now: time.Now()})
	if rep.Verdict != "valid" {
		t.Fatalf("expected valid, got %s: %s", rep.Verdict, rep.Summary)
	}
	if rep.Components[0].Value != "en%20us" {
		t.Fatalf("expected en%%20us, got %q", rep.Components[0].Value)
	}
}

func TestQueryParamRepeatedIsError(t *testing.T) {
	created := time.Now().Unix()
	inner := fmt.Sprintf(`("@query-param";name="lang");created=%d;keyid="test-key";alg="ed25519"`, created)
	req, keyPEM, _, _ := edFixture(t, inner, `"@query-param";name="lang": en`)
	req.URL = "https://seller.example/mcp?lang=en&lang=fr"
	rep := Verify(req, keyPEM, Options{Now: time.Now()})
	c := findCheck(rep, "component-reconstruction")
	if c == nil || c.OK {
		t.Fatalf("expected failing component-reconstruction for repeated @query-param, got %+v", c)
	}
	if !strings.Contains(c.Detail, "forbids covering a repeated parameter") {
		t.Fatalf("expected repeated-parameter guidance, got: %s", c.Detail)
	}
}

func TestBSPerFieldValue(t *testing.T) {
	// Two instances of x-multi must become a two-element List, not one
	// combined base64 blob.
	created := time.Now().Unix()
	inner := fmt.Sprintf(`("x-multi";bs);created=%d;keyid="test-key";alg="ed25519"`, created)
	req, keyPEM, _, _ := edFixture(t, inner, `"x-multi";bs: :YQ==:, :Yg==:`)
	req.HeaderInstances = [][2]string{
		{"X-Multi", "a"},
		{"X-Multi", "b"},
	}
	rep := Verify(req, keyPEM, Options{Now: time.Now()})
	if rep.Verdict != "valid" {
		t.Fatalf("expected valid, got %s: %s", rep.Verdict, rep.Summary)
	}
	if rep.Components[0].Value != ":YQ==:, :Yg==:" {
		t.Fatalf("expected two-element ;bs List, got %q", rep.Components[0].Value)
	}
}

func TestHeaderWhitespaceNotCollapsed(t *testing.T) {
	// Interior whitespace is preserved: only leading/trailing ws is
	// stripped and obs-fold unfolded.
	created := time.Now().Unix()
	inner := fmt.Sprintf(`("x-sp");created=%d;keyid="test-key";alg="ed25519"`, created)
	req, keyPEM, _, _ := edFixture(t, inner, `"x-sp": a  b`)
	req.HeaderInstances = [][2]string{
		{"X-Sp", "  a  b  "},
	}
	rep := Verify(req, keyPEM, Options{Now: time.Now()})
	if rep.Verdict != "valid" {
		t.Fatalf("expected valid (interior whitespace preserved), got %s: %s", rep.Verdict, rep.Summary)
	}
}

func TestMethodNotUppercased(t *testing.T) {
	created := time.Now().Unix()
	inner := fmt.Sprintf(`("@method");created=%d;keyid="test-key";alg="ed25519"`, created)
	req, keyPEM, _, _ := edFixture(t, inner, `"@method": post`)
	req.Method = "post"
	rep := Verify(req, keyPEM, Options{Now: time.Now()})
	if rep.Verdict != "valid" {
		t.Fatalf("expected valid for verbatim lowercase method, got %s: %s", rep.Verdict, rep.Summary)
	}
}

func TestAdCPProfileWarnsOnRSA(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})
	created := time.Now().Unix()
	inner := fmt.Sprintf(`("@method");created=%d;keyid="rsa-key";alg="rsa-v1_5-sha256"`, created)
	base := fmt.Sprintf("\"@method\": POST\n\"@signature-params\": %s", inner)
	sum := sha256.Sum256([]byte(base))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	req := &Request{
		Method: "POST",
		URL:    "https://seller.example/mcp",
		Headers: map[string]string{
			"Signature-Input": "sig1=" + inner,
			"Signature":       "sig1=:" + base64.StdEncoding.EncodeToString(sig) + ":",
		},
	}
	rep := Verify(req, keyPEM, Options{Now: time.Now()})
	if rep.Verdict != "valid" {
		t.Fatalf("expected valid RFC 9421 signature, got %s: %s", rep.Verdict, rep.Summary)
	}
	c := findCheck(rep, "adcp-profile")
	if c == nil {
		t.Fatal("expected adcp-profile check to be present")
	}
	if !strings.Contains(c.Detail, "WARNING") || !strings.Contains(c.Detail, "outside the AdCP 3.1") {
		t.Fatalf("expected AdCP profile warning for RSA, got: %s", c.Detail)
	}
}

func TestRSAPSS256Unregistered(t *testing.T) {
	created := time.Now().Unix()
	inner := fmt.Sprintf(`("@method");created=%d;keyid="k";alg="rsa-pss-sha256"`, created)
	req, keyPEM, _, _ := edFixture(t, inner, `"@method": POST`)
	rep := Verify(req, keyPEM, Options{Now: time.Now()})
	c := findCheck(rep, "algorithm")
	if c == nil || c.OK {
		t.Fatalf("expected failing algorithm check for unregistered rsa-pss-sha256, got %+v", c)
	}
	if !strings.Contains(c.Detail, "not in the RFC 9421") {
		t.Fatalf("expected registry message, got: %s", c.Detail)
	}
}

func TestReplayProtectionWarnsWhenBare(t *testing.T) {
	// No created/expires/nonce: the signature binds no freshness.
	inner := `("@method");keyid="test-key";alg="ed25519"`
	req, keyPEM, _, _ := edFixture(t, inner, `"@method": POST`)
	rep := Verify(req, keyPEM, Options{Now: time.Now()})
	c := findCheck(rep, "replay-protection")
	if c == nil {
		t.Fatal("expected replay-protection check")
	}
	if !strings.Contains(c.Detail, "no freshness") {
		t.Fatalf("expected no-freshness warning, got: %s", c.Detail)
	}
	if c2 := findCheck(rep, "keyid"); c2 == nil || !strings.Contains(c2.Detail, "test-key") {
		t.Fatalf("expected keyid check recording test-key, got %+v", c2)
	}
}

func TestReplayProtectionSurfacesNonce(t *testing.T) {
	created := time.Now().Unix()
	inner := fmt.Sprintf(`("@method");created=%d;nonce="abc123";keyid="test-key";alg="ed25519"`, created)
	req, keyPEM, _, _ := edFixture(t, inner, `"@method": POST`)
	rep := Verify(req, keyPEM, Options{Now: time.Now()})
	c := findCheck(rep, "replay-protection")
	if c == nil || !strings.Contains(c.Detail, "abc123") {
		t.Fatalf("expected nonce surfaced in replay-protection, got %+v", c)
	}
}

// jwksTestServer serves a JWKS with one Ed25519 key (kid "jwks-k1").
func jwksTestServer(t *testing.T, priv ed25519.PrivateKey) *httptest.Server {
	t.Helper()
	pub := priv.Public().(ed25519.PublicKey)
	doc := fmt.Sprintf(`{"keys":[{"kty":"OKP","crv":"Ed25519","kid":"jwks-k1","x":%q}]}`,
		base64.RawURLEncoding.EncodeToString(pub))
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(doc))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestJWKSResolution(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	srv := jwksTestServer(t, priv)
	old := jwksHTTPClient
	jwksHTTPClient = srv.Client()
	defer func() { jwksHTTPClient = old }()

	created := time.Now().Unix()
	inner := fmt.Sprintf(`("@method");created=%d;keyid="jwks-k1";alg="ed25519"`, created)
	base := fmt.Sprintf("\"@method\": POST\n\"@signature-params\": %s", inner)
	sig := ed25519.Sign(priv, []byte(base))
	req := &Request{
		Method: "POST",
		URL:    "https://seller.example/mcp",
		Headers: map[string]string{
			"Signature-Input": "sig1=" + inner,
			"Signature":       "sig1=:" + base64.StdEncoding.EncodeToString(sig) + ":",
		},
	}
	rep := Verify(req, nil, Options{Now: time.Now(), JWKSURL: srv.URL})
	if rep.Verdict != "valid" {
		t.Fatalf("expected valid via JWKS resolution, got %s: %s", rep.Verdict, rep.Summary)
	}
	c := findCheck(rep, "keyid-resolution")
	if c == nil || !c.OK || !strings.Contains(c.Detail, "jwks-k1") {
		t.Fatalf("expected keyid-resolution check, got %+v", c)
	}
}

func TestJWKSUnknownKid(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	srv := jwksTestServer(t, priv)
	old := jwksHTTPClient
	jwksHTTPClient = srv.Client()
	defer func() { jwksHTTPClient = old }()

	created := time.Now().Unix()
	inner := fmt.Sprintf(`("@method");created=%d;keyid="nope";alg="ed25519"`, created)
	base := fmt.Sprintf("\"@method\": POST\n\"@signature-params\": %s", inner)
	sig := ed25519.Sign(priv, []byte(base))
	req := &Request{
		Method: "POST",
		URL:    "https://seller.example/mcp",
		Headers: map[string]string{
			"Signature-Input": "sig1=" + inner,
			"Signature":       "sig1=:" + base64.StdEncoding.EncodeToString(sig) + ":",
		},
	}
	rep := Verify(req, nil, Options{Now: time.Now(), JWKSURL: srv.URL})
	c := findCheck(rep, "keyid-resolution")
	if c == nil || c.OK {
		t.Fatalf("expected failing keyid-resolution for unknown kid, got %+v", c)
	}
	if !strings.Contains(c.Detail, `no JWKS key with kid "nope"`) {
		t.Fatalf("expected clear kid message, got: %s", c.Detail)
	}
}

func TestJWKSRejectsPlainHTTP(t *testing.T) {
	_, _, err := resolveKeyFromJWKS("http://example.com/jwks.json", "k1")
	if err == nil || !strings.Contains(err.Error(), "must be https") {
		t.Fatalf("expected https-only error, got %v", err)
	}
}
