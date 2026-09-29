# Worked example: mock seller → conformance → scenario → evidence report

A complete end-to-end flow against a synthetic mock seller, using only
this repository. All fixtures (product ids, budgets, URLs) are invented for
testing — nothing here touches a real seller or real money.

## 0. Build

```bash
go build -o adcp-test ./cmd/adcp-test
```

## 1. Start the mock seller

In one terminal (keeps running until Ctrl-C):

```bash
./adcp-test mock --config examples/mock-seller.yaml
# mock example-seller replay       http://127.0.0.1:8089
```

The mock speaks JSON-RPC 2.0 over HTTP POST (`tools/list`, `tools/call`),
with a media-buy lifecycle state machine
(draft → active → paused → active → cancelled) and structured-error
rejections for unsigned mutating calls.

## 2. Run conformance (headless)

In another terminal:

```bash
./adcp-test --ci --target http://127.0.0.1:8089 > conformance.json
echo $?
# 0 when every check passes
```

This probes the tool surface, input schemas, auth behavior, and error
taxonomy, and prints a JSON report.

## 3. Run the happy-path scenario

```bash
./adcp-test scenario --pack happy-path-media-buy \
    --target http://127.0.0.1:8089 > scenario.json
```

Every tool call is also recorded into the Inspect screen's session store
when run through the web UI.

## 4. Export the evidence report

```bash
./adcp-test report \
    --conformance conformance.json \
    --scenarios scenario.json \
    --title "Example seller — integration evidence" \
    --out evidence.html --pdf evidence.pdf
```

`evidence.html` is a self-contained evidence pack (inline CSS, no external
requests); `evidence.pdf` is the printable export.

## Same flow in the web UI

```bash
./adcp-test
# adcp-test serving on http://127.0.0.1:8080
```

1. **Scenarios** → set target to `http://127.0.0.1:8089` → run
   `happy-path-media-buy` (tick **chaos mode** to watch fault injection;
   the seed in the report replays the exact run).
2. **Conformance** → run the suite against the same target.
3. **Lifecycle** → run the lifecycle check from the Scenarios screen.
4. **Snapshots** → save each report as a named snapshot.
5. **Reports** → tick "use last run" for each kind, download the HTML
   evidence pack.

## Going further

- `adcp-test fuzz --target http://127.0.0.1:8089 --iterations 200`
  — malformed-payload robustness (localhost only unless `--allow-remote`).
- `adcp-test webhook-listen --port 8090` — capture the seller's webhooks
  on a localhost timeline (starts a listener; point the seller at the
  printed URL).
- Signdebug screen — paste a signed request + verification key to debug
  RFC 9421 signature mismatches. The key is used in memory for that one
  check and never stored.
