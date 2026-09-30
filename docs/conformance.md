# Conformance

Functional pass/fail of a seller endpoint: tool surface, input schemas,
auth behavior, error codes. 24 checks; headless `--ci` for CI pipelines
(also wrapped by the [GitHub Action](ci-action.md)).

## Run it

UI: **Conformance** → enter the seller's MCP endpoint URL → run, then
drill into failures.

CLI:

```sh
./adcp-test --ci --target https://seller.example/mcp [--bearer-token T] [--profile media-buy]
```

JSON report on stdout; exit 0 when every check passes, 1 otherwise.

API: `POST /api/conformance/run` with `{"target_url": ..., "profile": "media-buy"}`.

## Report

```json
{
  "target_url": "https://seller.example/mcp",
  "started_at": "2026-09-28T...",
  "finished_at": "2026-09-28T...",
  "summary": { "total": 24, "passed": 24, "failed": 0, "skipped": 0 },
  "checks": [
    { "name": "tool-surface", "status": "pass", "detail": "...", "duration_ms": 12.3 },
    { "name": "schema:get_products", "status": "pass", "detail": "...", "duration_ms": 0.1 },
    { "name": "auth:unsigned-mutating-call-rejected", "status": "pass", "detail": "...", "duration_ms": 8.7 }
  ]
}
```

## Profiles

Sellers implement different protocol subsets, so conformance is
profile-scoped. `--profile` (CLI), `profile` (MCP `run_conformance`, API
`/api/conformance/run`) selects which tool surface and required tasks
are checked. Unknown profile names are rejected with an error.

| Profile | What it checks |
|---------|----------------|
| `full` (default) | The whole expected surface: media-buy, creative, and signals tools |
| `media-buy` | Only the media-buy task list (`get_products`, `create_media_buy`, …) |
| `creative` | Only the creative task list (`list_creative_formats`, `sync_creatives`, …) |
| `signals` | Only the signals task list (`get_signals`, `activate_signal`, …) |

## Check families

- `tool-surface` — expected AdCP tool surface from `tools/list`.
- `schema:<tool>` — declared inputSchema well-formedness + expected
  required fields.
- `auth:*` — unsigned / malformed-signature mutating probes must be
  rejected; read-only behavior documented.
- `errors:*` — unknown tool and invalid args must return structured
  JSON-RPC errors, not HTML/plaintext.

The expected surface is the AdCP 3.1 v3-era task list
(`get_adcp_capabilities`, `get_products`, `list_creative_formats`,
`create_media_buy`, `update_media_buy`, `get_media_buys`,
`sync_creatives`, `sync_catalogs`, `list_creatives`,
`get_media_buy_delivery`, `provide_performance_feedback`, plus optional
sibling-protocol tools).

**Auth probes are safe and synthetic, never exploits.** The
`auth:unsigned-mutating-call-rejected` and
`auth:malformed-signature-rejected` probes call `create_media_buy` with
valid, well-typed arguments that are **clearly synthetic and never
signed**: account `adcp-test-probe-account`, brand `adcp-test-probe-brand`,
product id `adcp-test-probe-product`, budget `1` micros, dates in 2030.
They only observe whether the seller rejects unsigned or malformed
mutating calls — they never touch customer data and cannot be mistaken
for real mutations by a conforming seller.

**Probe semantics are strict by design.** An auth probe PASSES only when
the seller rejects the probe with a signature/auth-class rejection (a
rejection mentioning signature, auth, unsigned, or signature age). A
validation-class rejection (e.g. "invalid args") does NOT pass: the
seller may be rejecting the args without enforcing signatures, and we
cannot confirm signing is actually enforced — that is a FAIL with the
detail saying so. If the seller accepts the probe outright, that is also
a FAIL: the seller does not enforce RFC 9421 on mutating calls.
