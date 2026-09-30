# Signing debugger

Paste a signed HTTP request and the verification key: the debugger
reconstructs the RFC 9421 signature base from the covered components,
verifies the signature (Ed25519, ECDSA, RSA, HMAC), and pinpoints exactly
what mismatches — wrong key, tampered body (content-digest), expired
`created`/`expires`, or a base that differs from what your signer computed
(paste it as "expected base" for a line-by-line diff).

```sh
./adcp-test signdebug --request req.json --key key.pem [--expected-base base.txt]
```

`req.json`: `{"method": "POST", "url": "...", "headers": {...},
"header_instances": [["signature-input","sig1=(...)"], ["signature-input","sig2=(...)"]],
"body": "..."}`. API: `POST /api/signdebug/verify` (accepts `jwks_url`
to resolve `keyId` from a remote JWKS document over HTTPS).

**Key safety:** the key is used in memory for that one verification only.
It is never stored, logged, recorded into sessions, or included in
reports — the UI clears the key field the moment the check completes.

## Derived-component rules (RFC 9421)

- `@query` with no query component covers `"?"` (RFC 9421 §2.2.7).
- `@query-param` percent-encodes per the spec: space becomes `%20`, never
  `+`; a parameter appearing more than once is an error (§2.2.8).
- `;bs` serializes field values as a strict List of byte sequences
  (`:b64:,:b64:`), one item per field value (§2.1).
- Field values are unfolded per RFC 9110 §5.2 (strip OWS, unfold obs-fold)
  — interior whitespace is preserved exactly, not collapsed.
- `@method` is matched case-sensitively as sent (§2.2.1); the debugger
  does not uppercase it.
- ECDSA signatures verify as raw fixed-size `r‖s` per §3.3.2 (not DER).
  The algorithm is checked against the RFC 9421 signature-algorithm
  registry (§6.2): unregistered names like `rsa-pss-sha256` are refused.

## Extra checks

- `keyid` / `keyid-resolution` — records the signature's `keyId` and, when
  a `jwks_url` is supplied, resolves it against the JWKS (HTTPS only,
  matched by `kid`), so a seller can debug key rotation without pasting
  keys.
- `replay-protection` — warns when a signature has no freshness bindings
  (no `created`/`expires` component, no `nonce`/`tag` parameter), and
  surfaces the `nonce`/`tag` values when present.
- `adcp-profile` — warns (non-gating) when the signature does not conform
  to the AdCP profile (e.g. an algorithm the AdCP spec does not bless).
  Crypto validity and AdCP profile conformance are reported separately:
  a signature can verify correctly and still be outside the profile.
- When duplicate instances of a field exist (e.g. two `Signature-Input`
  headers), pass them as `header_instances` — every instance is included
  in the covered-components lookup.
