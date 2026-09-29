# Scenarios

Scenario packs are YAML files describing ordered, assertion-checked
tool-call flows against a seller (real, mock, or replay URL). Each
scenario has steps with per-step assertions plus optional setup/teardown
hooks.

```yaml
name: happy-path-media-buy
description: Full media buy lifecycle.
scenarios:
  - name: display media buy
    steps:
      - name: list products
        tool: get_products
        arguments: {}
        assertions:
          - status: ok
          - latency_ms_lt: 2000
          - response_contains:
              products:
                - product_id: prod-web-banner
      - name: create media buy
        tool: create_media_buy
        arguments: {product_id: prod-web-banner, budget_micros: 5000000}
        save: {media_buy_id: media_buy_id}   # extract into {{vars.media_buy_id}}
        assertions:
          - status: ok
          - response_contains: {status: draft}
```

Assertion kinds: `status: ok|error`, `response_contains: {...}`
(recursive subset match on the result JSON), `latency_ms_lt: N`
(milliseconds), `error_code: N` (JSON-RPC error code). Steps run
sequentially; every call is recorded into a session (visible on the
Inspect screen). A step's `save` map extracts values from its result
into `{{vars.*}}` for later steps; setup/teardown hooks run without
assertions (teardown is best-effort and skips hooks whose variables were
never set).

## Built-in packs

Four packs ship as runnable cards on the Scenarios screen (synthetic data
only): `happy-path-media-buy` (products → create → sync creatives →
delivery), `creative-rejection` (banned creative rejected, clean creative
approved), `cancel-pause` (active → paused → cancelled, plus an illegal
transition asserting `error_code: -32001`), `budget-limit` (over-limit
budget rejected, valid budget creates a draft).

## Run it

```sh
./adcp-test scenario --list
./adcp-test scenario --pack happy-path-media-buy --target https://seller.example/mcp
# --pack also accepts a path to your own pack YAML file
# JSON report on stdout; exit 0 when every scenario passes, 1 otherwise
```

API: `GET /api/scenarios` lists the built-in packs;
`POST /api/scenarios/run {"pack": ..., "target_url": ...}` streams
server-sent events (`scenario_started`, `step_started`, `step_finished`,
`scenario_finished`, then a final `report` event).

## Chaos mode

Every scenario run can inject seeded, reproducible chaos: dropped tool
calls and latency spikes. Steps that were injected are badged ⚡ in the
report, and the report records the seed plus configured rates, so the
exact fault pattern replays.

```sh
./adcp-test scenario --pack happy-path-media-buy \
    --target http://127.0.0.1:8089 --chaos --chaos-seed 42
```

Same localhost guard as load/fuzz: remote targets are refused unless
explicitly allowed. API: `POST /api/scenarios/run` with
`{"chaos": true, "chaos_seed": 42}`.
