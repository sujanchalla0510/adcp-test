# Spec-upgrade diffs

AdCP moves fast. `adcp-test specdiff` answers "what would break if the
spec moved from version X to Y" — as a pure surface diff, or grounded
against a live seller.

## Surface diff

```sh
./adcp-test specdiff --from 3.1 --to 4.0
```

Prints the added/removed/changed tools and the auth-requirement changes
between the two expectation surfaces as JSON. Exit code 0.

## Against a live seller

```sh
./adcp-test specdiff --from 3.1 --to 4.0 --target https://seller.example/mcp
```

Runs the conformance suite (the 3.1 checks) and flags behavior that would
break under the newer expectations. Findings are `would-fail` (breaks
under the new spec) or `attention` (already aligned — worth knowing).
Exit code 1 when any `would-fail` finding exists, so it gates CI.

```json
{
  "target": "https://seller.example/mcp",
  "from": "3.1",
  "to": "4.0",
  "to_status": "draft-expectation",
  "diff": { "...": "..." },
  "findings": [
    {
      "severity": "would-fail",
      "check": "tool-surface",
      "detail": "sync_accounts is not advertised; it becomes required in 4.0 for multi-account agents (brand + operator declaration)"
    },
    {
      "severity": "attention",
      "check": "auth:unsigned-mutating-call-rejected",
      "detail": "seller already rejects unsigned mutating calls — aligned with 4.0 mandatory signing"
    }
  ]
}
```

## Known versions

| Version | Status              | Content                                                        |
|---------|---------------------|----------------------------------------------------------------|
| `3.1`   | published           | The baseline adcp-test checks against: v3 MCP task-based protocol + RFC 9421 signing auth model |
| `4.0`   | **draft-expectation** | Encodes public implementer discussion (Sep 2026): RFC 9421 signatures mandatory for spend/mutating ops, multi-account agents declaring brand + operator via `sync_accounts`, per-call account-belongs-to-signer checks, and the `require_operator_auth` OAuth alternative |

The 4.0 set is explicitly **not a published spec** — every 4.0 artifact
carries the `draft-expectation` status, and the CLI exit code reflects
expectations, not compliance. Verify against the released spec before
treating any entry as authoritative.

## How the 4.0 expectations are derived

The 3.1 surface is generated from the same expected tool surface the
conformance suite checks (`internal/conformance`). The 4.0 draft layers
the publicly discussed changes on top:

- `sync_accounts` flips optional → required (multi-account agents must
  declare each brand + operator).
- RFC 9421 signatures flip probed → mandatory for spend and mutating
  operations.
- New auth expectations: per-call account-ownership checks (the signature
  says who is calling; authorization stays per account) and the
  `require_operator_auth` OAuth model as an alternative.

When the real 4.0 spec publishes, the draft set gets replaced with the
real surface and `specdiff` becomes a true upgrade guide.
