package signdebug

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"hash"
	"strconv"
	"strings"
	"time"
)

// Supported signature algorithms.
const (
	AlgEd25519    = "ed25519"
	AlgECDSAP256  = "ecdsa-p256-sha256"
	AlgECDSAP384  = "ecdsa-p384-sha384"
	AlgECDSAP521  = "ecdsa-p521-sha512"
	AlgRSAPSS256  = "rsa-pss-sha256"
	AlgRSAPSS512  = "rsa-pss-sha512"
	AlgRSAPKCS256 = "rsa-v1_5-sha256"
	AlgRSAPKCS512 = "rsa-v1_5-sha512"
	AlgHMACSHA256 = "hmac-sha256"
)

// base64Std encodes s with standard base64.
func base64Std(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

// parseKey parses PEM key material in memory and returns the public key
// plus a human description. Private keys are accepted (the public half
// is derived). Nothing is written anywhere; callers must not log the
// input bytes.
func parseKey(keyPEM []byte, alg string) (crypto.PublicKey, string, error) {
	if alg == AlgHMACSHA256 {
		secret := []byte(strings.TrimSpace(string(keyPEM)))
		if len(secret) == 0 {
			return nil, "", fmt.Errorf("empty HMAC secret")
		}
		return secret, fmt.Sprintf("HMAC secret (%d bytes)", len(secret)), nil
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, "", fmt.Errorf("not PEM-encoded (need a PUBLIC KEY / PRIVATE KEY block, or raw bytes for hmac-sha256)")
	}
	switch block.Type {
	case "PUBLIC KEY":
		pub, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, "", fmt.Errorf("parse public key: %w", err)
		}
		return pub, describePublicKey(pub), nil
	case "PRIVATE KEY":
		priv, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, "", fmt.Errorf("parse PKCS#8 private key: %w", err)
		}
		pub, err := publicOf(priv)
		if err != nil {
			return nil, "", err
		}
		return pub, describePublicKey(pub) + " (derived from private key)", nil
	case "RSA PRIVATE KEY":
		priv, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, "", fmt.Errorf("parse PKCS#1 private key: %w", err)
		}
		return &priv.PublicKey, describePublicKey(&priv.PublicKey) + " (derived from private key)", nil
	case "EC PRIVATE KEY":
		priv, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, "", fmt.Errorf("parse EC private key: %w", err)
		}
		return &priv.PublicKey, describePublicKey(&priv.PublicKey) + " (derived from private key)", nil
	default:
		return nil, "", fmt.Errorf("unsupported PEM block type %q", block.Type)
	}
}

func publicOf(priv crypto.PrivateKey) (crypto.PublicKey, error) {
	switch k := priv.(type) {
	case *rsa.PrivateKey:
		return &k.PublicKey, nil
	case *ecdsa.PrivateKey:
		return &k.PublicKey, nil
	case ed25519.PrivateKey:
		return k.Public().(ed25519.PublicKey), nil
	default:
		return nil, fmt.Errorf("unsupported private key type %T", priv)
	}
}

func describePublicKey(pub crypto.PublicKey) string {
	switch k := pub.(type) {
	case ed25519.PublicKey:
		return "Ed25519 public key"
	case *ecdsa.PublicKey:
		return fmt.Sprintf("ECDSA %s public key", k.Curve.Params().Name)
	case *rsa.PublicKey:
		return fmt.Sprintf("RSA-%d public key", k.N.BitLen())
	case []byte:
		return "HMAC secret"
	default:
		return fmt.Sprintf("public key (%T)", pub)
	}
}

// inferAlgorithm picks the algorithm from the key type when the
// Signature-Input has no alg parameter. RSA is ambiguous (PSS vs
// PKCS#1 v1.5), so it returns "" there.
func inferAlgorithm(pub crypto.PublicKey) string {
	switch k := pub.(type) {
	case ed25519.PublicKey:
		return AlgEd25519
	case *ecdsa.PublicKey:
		switch k.Curve.Params().Name {
		case "P-256":
			return AlgECDSAP256
		case "P-384":
			return AlgECDSAP384
		case "P-521":
			return AlgECDSAP521
		}
	case []byte:
		return AlgHMACSHA256
	}
	return ""
}

// checkAlgorithm verifies the alg parameter agrees with the key type.
func checkAlgorithm(alg string, pub crypto.PublicKey) (bool, string) {
	switch alg {
	case AlgEd25519:
		if _, ok := pub.(ed25519.PublicKey); ok {
			return true, "alg ed25519 matches an Ed25519 key"
		}
	case AlgECDSAP256, AlgECDSAP384, AlgECDSAP521:
		if k, ok := pub.(*ecdsa.PublicKey); ok {
			want := map[string]string{AlgECDSAP256: "P-256", AlgECDSAP384: "P-384", AlgECDSAP521: "P-521"}[alg]
			if k.Curve.Params().Name == want {
				return true, fmt.Sprintf("alg %s matches an ECDSA %s key", alg, want)
			}
			return false, fmt.Sprintf("alg %s wants curve %s but the key is %s",
				alg, want, k.Curve.Params().Name)
		}
	case AlgRSAPSS256, AlgRSAPSS512, AlgRSAPKCS256, AlgRSAPKCS512:
		if _, ok := pub.(*rsa.PublicKey); ok {
			return true, fmt.Sprintf("alg %s matches an RSA key", alg)
		}
	case AlgHMACSHA256:
		if _, ok := pub.([]byte); ok {
			return true, "alg hmac-sha256 matches the provided secret"
		}
	case "":
		return false, "no alg parameter and the algorithm cannot be inferred from the key type (ambiguous for RSA: rsa-pss-sha512 vs rsa-v1_5-sha512)"
	default:
		return false, fmt.Sprintf("unsupported algorithm %q", alg)
	}
	return false, fmt.Sprintf("alg %s does not match the provided key (%s)", alg, describePublicKey(pub))
}

// verifySignature verifies sig over base with the named algorithm.
func verifySignature(alg string, pub crypto.PublicKey, keyPEM, base, sig []byte) error {
	switch alg {
	case AlgEd25519:
		k, ok := pub.(ed25519.PublicKey)
		if !ok {
			return fmt.Errorf("not an Ed25519 key")
		}
		if !ed25519.Verify(k, base, sig) {
			return fmt.Errorf("Ed25519 verification failed")
		}
		return nil
	case AlgECDSAP256, AlgECDSAP384, AlgECDSAP521:
		k, ok := pub.(*ecdsa.PublicKey)
		if !ok {
			return fmt.Errorf("not an ECDSA key")
		}
		h := ecdsaHash(alg)
		sum := hashSum(h, base)
		if !ecdsa.VerifyASN1(k, sum, sig) {
			return fmt.Errorf("ECDSA verification failed")
		}
		return nil
	case AlgRSAPKCS256, AlgRSAPKCS512:
		k, ok := pub.(*rsa.PublicKey)
		if !ok {
			return fmt.Errorf("not an RSA key")
		}
		h := rsaHash(alg)
		sum := hashSum(h, base)
		return rsa.VerifyPKCS1v15(k, h, sum, sig)
	case AlgRSAPSS256, AlgRSAPSS512:
		k, ok := pub.(*rsa.PublicKey)
		if !ok {
			return fmt.Errorf("not an RSA key")
		}
		h := rsaHash(alg)
		sum := hashSum(h, base)
		return rsa.VerifyPSS(k, h, sum, sig, &rsa.PSSOptions{
			SaltLength: rsa.PSSSaltLengthEqualsHash,
			Hash:       h,
		})
	case AlgHMACSHA256:
		secret, ok := pub.([]byte)
		if !ok {
			return fmt.Errorf("not an HMAC secret")
		}
		mac := hmac.New(sha256.New, secret)
		mac.Write(base)
		if subtle.ConstantTimeCompare(mac.Sum(nil), sig) != 1 {
			return fmt.Errorf("HMAC verification failed")
		}
		return nil
	default:
		return fmt.Errorf("unsupported algorithm %q", alg)
	}
}

func ecdsaHash(alg string) crypto.Hash {
	switch alg {
	case AlgECDSAP384:
		return crypto.SHA384
	case AlgECDSAP521:
		return crypto.SHA512
	default:
		return crypto.SHA256
	}
}

func rsaHash(alg string) crypto.Hash {
	switch alg {
	case AlgRSAPSS512, AlgRSAPKCS512:
		return crypto.SHA512
	default:
		return crypto.SHA256
	}
}

func hashSum(h crypto.Hash, base []byte) []byte {
	var hf hash.Hash
	switch h {
	case crypto.SHA384:
		hf = sha512.New384()
	case crypto.SHA512:
		hf = sha512.New()
	default:
		hf = sha256.New()
	}
	hf.Write(base)
	return hf.Sum(nil)
}

// checkDigest cross-checks a covered content-digest component against a
// freshly computed digest of the request body, pinpointing body tamper.
func checkDigest(req *Request, headers map[string]string, body []byte, covered []innerItem, check func(Check)) {
	coveredDigest := false
	for _, it := range covered {
		if strings.ToLower(it.Value) == "content-digest" {
			coveredDigest = true
		}
	}
	if !coveredDigest {
		check(Check{Name: "content-digest", OK: true, Detail: "not covered; nothing to cross-check"})
		return
	}
	raw, ok := headers["content-digest"]
	if !ok {
		check(Check{Name: "content-digest", OK: false, Detail: "covered but the request has no content-digest header"})
		return
	}
	var recomputed []string
	matched := false
	for _, part := range splitTopLevel(raw, ',') {
		part = strings.TrimSpace(part)
		name, val, found := splitKeyValue(part)
		if !found {
			continue
		}
		want, err := parseByteSequence(strings.TrimSpace(val))
		if err != nil {
			continue
		}
		algName := strings.ToLower(strings.TrimSpace(name))
		var sum []byte
		switch algName {
		case "sha-256":
			s := sha256.Sum256(body)
			sum = s[:]
		case "sha-512":
			s := sha512.Sum512(body)
			sum = s[:]
		default:
			continue // unknown digest algorithm; cannot cross-check
		}
		recomputed = append(recomputed, algName+"=:"+base64.StdEncoding.EncodeToString(sum)+":")
		if subtle.ConstantTimeCompare(want, sum) == 1 {
			matched = true
		}
	}
	if matched {
		check(Check{Name: "content-digest", OK: true,
			Detail: "recomputed digest matches the header: the body is intact as far as this check can tell"})
		return
	}
	actual := strings.Join(recomputed, ", ")
	if actual == "" {
		actual = "(no supported digest algorithm in the header)"
	}
	check(Check{Name: "content-digest", OK: false,
		Detail:   "body does not match the content-digest header: tampered, truncated, or re-encoded after signing",
		Expected: strings.TrimSpace(raw),
		Actual:   actual})
}

// checkExpiry validates created/expires signature parameters.
func checkExpiry(params map[string]string, now time.Time, check func(Check)) {
	createdS, hasCreated := params["created"]
	expiresS, hasExpires := params["expires"]
	if !hasCreated && !hasExpires {
		check(Check{Name: "expiry", OK: true, Detail: "no created/expires parameters to check"})
		return
	}
	const skew = 300 // seconds of tolerated clock skew
	if hasCreated {
		created, err := strconv.ParseInt(strings.TrimSpace(createdS), 10, 64)
		if err != nil {
			check(Check{Name: "expiry", OK: false, Detail: fmt.Sprintf("created=%s is not an integer", createdS)})
			return
		}
		if created > now.Unix()+skew {
			check(Check{Name: "expiry", OK: false,
				Detail: fmt.Sprintf("created=%d is in the future (now=%d): clock skew between signer and verifier, or a replayed/future-dated signature", created, now.Unix())})
			return
		}
	}
	if hasExpires {
		expires, err := strconv.ParseInt(strings.TrimSpace(expiresS), 10, 64)
		if err != nil {
			check(Check{Name: "expiry", OK: false, Detail: fmt.Sprintf("expires=%s is not an integer", expiresS)})
			return
		}
		if now.Unix() > expires {
			check(Check{Name: "expiry", OK: false,
				Detail: fmt.Sprintf("signature expired at %d (now=%d)", expires, now.Unix())})
			return
		}
	}
	check(Check{Name: "expiry", OK: true, Detail: "created/expires are consistent with the current time"})
}

// diffCheck diffs the reconstructed base against an expected base the
// user pasted (e.g. what their signer computed), pinpointing exactly
// which covered component differs.
func diffCheck(base, expected string, check func(Check)) {
	baseLines := strings.Split(strings.TrimRight(base, "\n"), "\n")
	expLines := strings.Split(strings.TrimRight(expected, "\n"), "\n")
	var mismatched []string
	n := len(baseLines)
	if len(expLines) > n {
		n = len(expLines)
	}
	for i := 0; i < n; i++ {
		var b, e string
		if i < len(baseLines) {
			b = baseLines[i]
		}
		if i < len(expLines) {
			e = expLines[i]
		}
		if b != e {
			mismatched = append(mismatched, componentNameOf(b, e))
		}
	}
	if len(mismatched) == 0 {
		check(Check{Name: "expected-base-diff", OK: true,
			Detail: "reconstructed base matches the expected base line-for-line"})
		return
	}
	check(Check{Name: "expected-base-diff", OK: false,
		Detail:   fmt.Sprintf("%d line(s) differ between the reconstructed base and the expected base", len(mismatched)),
		Expected: strings.Join(mismatched, ", "),
		Actual:   "values reconstructed from the request as received"})
}

// componentNameOf names the component for a differing base line.
func componentNameOf(baseLine, expLine string) string {
	for _, line := range []string{expLine, baseLine} {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "\"") {
			if end := quotedEnd(line); end > 0 {
				return line[:end+1]
			}
		}
	}
	return "(unparseable line)"
}
