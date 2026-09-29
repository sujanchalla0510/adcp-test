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

`req.json`: `{"method": "POST", "url": "...", "headers": {...}, "body":
"..."}`. API: `POST /api/signdebug/verify`.

**Key safety:** the key is used in memory for that one verification only.
It is never stored, logged, recorded into sessions, or included in
reports — the UI clears the key field the moment the check completes.
