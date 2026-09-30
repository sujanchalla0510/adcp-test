package signdebug

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"
)

// jwksFetchTimeout bounds the JWKS fetch behind --jwks-url.
const jwksFetchTimeout = 10 * time.Second

// jwk is the subset of a JSON Web Key we need for signature verification.
type jwk struct {
	Kty string `json:"kty"` // "OKP", "EC", "RSA"
	Kid string `json:"kid"`
	Crv string `json:"crv"` // OKP/EC curve name
	X   string `json:"x"`   // base64url: OKP public key / EC x
	Y   string `json:"y"`   // base64url: EC y
	N   string `json:"n"`   // base64url: RSA modulus
	E   string `json:"e"`   // base64url: RSA exponent
}

// jwksDocument is a JWKS key set.
type jwksDocument struct {
	Keys []jwk `json:"keys"`
}

// jwksHTTPClient is the client used for JWKS fetches; overridden in
// tests to trust the test TLS server.
var jwksHTTPClient = http.DefaultClient

// resolveKeyFromJWKS fetches the JWKS at jwksURL, selects the key whose
// kid matches keyid, and returns its public key. Only https URLs are
// accepted. Supported key types: OKP/Ed25519, EC (P-256/P-384/P-521),
// RSA. Nothing is cached or persisted; callers use the key in memory.
//
// Production sellers must resolve keyid -> key material this way (or via
// an equivalent trusted directory). A debugger that accepts pasted key
// material cannot do it for them — this flag only lets the operator
// reproduce the seller-side lookup.
func resolveKeyFromJWKS(jwksURL, keyid string) (crypto.PublicKey, string, error) {
	if !strings.HasPrefix(jwksURL, "https://") {
		return nil, "", fmt.Errorf("jwks-url must be https, got %q", jwksURL)
	}
	if strings.TrimSpace(keyid) == "" {
		return nil, "", fmt.Errorf("no keyid in the signature parameters; cannot select a JWKS key")
	}
	ctx, cancel := context.WithTimeout(context.Background(), jwksFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jwksURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("jwks request: %w", err)
	}
	resp, err := jwksHTTPClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("fetch jwks: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("jwks fetch: unexpected status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, "", fmt.Errorf("read jwks: %w", err)
	}
	var doc jwksDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, "", fmt.Errorf("jwks is not valid JSON: %w", err)
	}
	for _, k := range doc.Keys {
		if k.Kid != keyid {
			continue
		}
		pub, desc, err := jwkPublicKey(k)
		if err != nil {
			return nil, "", fmt.Errorf("jwk %q: %w", keyid, err)
		}
		return pub, fmt.Sprintf("%s (kid %q from JWKS)", desc, keyid), nil
	}
	return nil, "", fmt.Errorf("no JWKS key with kid %q", keyid)
}

// jwkPublicKey builds a public key from one JWK.
func jwkPublicKey(k jwk) (crypto.PublicKey, string, error) {
	dec := func(s, what string) ([]byte, error) {
		b, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("bad base64url %s: %w", what, err)
		}
		return b, nil
	}
	switch k.Kty {
	case "OKP":
		if k.Crv != "Ed25519" {
			return nil, "", fmt.Errorf("unsupported OKP curve %q (only Ed25519)", k.Crv)
		}
		x, err := dec(k.X, "x")
		if err != nil {
			return nil, "", err
		}
		if len(x) != ed25519.PublicKeySize {
			return nil, "", fmt.Errorf("Ed25519 x must be %d bytes, got %d", ed25519.PublicKeySize, len(x))
		}
		return ed25519.PublicKey(x), "Ed25519 public key", nil
	case "EC":
		x, err := dec(k.X, "x")
		if err != nil {
			return nil, "", err
		}
		y, err := dec(k.Y, "y")
		if err != nil {
			return nil, "", err
		}
		curve, name := ecCurve(k.Crv)
		if curve == nil {
			return nil, "", fmt.Errorf("unsupported EC curve %q", k.Crv)
		}
		return &ecdsa.PublicKey{Curve: curve, X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)},
			fmt.Sprintf("ECDSA %s public key", name), nil
	case "RSA":
		n, err := dec(k.N, "n")
		if err != nil {
			return nil, "", err
		}
		e, err := dec(k.E, "e")
		if err != nil {
			return nil, "", err
		}
		return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())},
			fmt.Sprintf("RSA-%d public key", len(n)*8), nil
	default:
		return nil, "", fmt.Errorf("unsupported kty %q", k.Kty)
	}
}

// ecCurve maps a JWK crv name to the stdlib curve.
func ecCurve(crv string) (elliptic.Curve, string) {
	switch crv {
	case "P-256":
		return elliptic.P256(), "P-256"
	case "P-384":
		return elliptic.P384(), "P-384"
	case "P-521":
		return elliptic.P521(), "P-521"
	default:
		return nil, ""
	}
}
