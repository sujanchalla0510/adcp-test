# Testing buyer agents

adcp-test's seller-side tools (conformance, scenarios, load, lifecycle,
fuzzing, spec-upgrade diffs) answer "does this seller implementation
work?" — they probe a seller's endpoint. Testing a **buyer** agent is the
mirror image: you point the buyer at something adcp-test controls, then
make that something misbehave in instructive ways. The mock server is
that harness.

## The loop

**1. Stand up a fake seller.** Write a mock config describing the seller
surface your buyer expects — routes match tool calls by name and
argument patterns, and answer with inline JSON, fixture files,
nth-call response sequences, or `{{args.*}}` templates that echo request
fields back so the mock stays coherent across a multi-step buy. Or skip
writing YAML: **Mock builder** → **record mode** proxies one real
session against a real seller and generates a reusable mock config from
the traffic.

```sh
./adcp-test mock --config buyer-test.yaml
```

**2. Point the buyer at it.** Give your buyer agent the mock's URL as
its seller endpoint and run your buying flow — media-buy creation,
creative sync, delivery polling, whatever your buyer does.

**3. Make the mock adversarial.** This is where buyer bugs surface:

- **Response sequences** — the 1st `create_media_buy` succeeds, the 2nd
  returns a structured error, the 3rd hangs past the buyer's timeout.
  Does the buyer retry safely, or double-spend?
- **Fault injection** — per-route error / timeout / malformed-response
  rates. Does the buyer handle a 500, a truncated body, a non-JSON
  reply without crashing or corrupting state?
- **Latency profiles** — fixed delays, p50/p99 distributions, periodic
  spikes. Do the buyer's timeouts and retries behave, or does a slow
  seller cascade into a stuck buyer?
- **Lifecycle state machine** — media buys move draft → active →
  paused → cancelled, and illegal transitions are rejected. Does the
  buyer track state correctly, or assume the happy path?

**4. Lock it in with cassettes.** Record one good session through the
recording proxy, then replay the cassette as the seller in CI. Your
buyer's regression suite now runs deterministically with no seller
involved at all. See [Inspect](inspect.md).

**5. Debug signatures from the buyer side.** If the seller rejects your
buyer's requests, paste one into the **signing debugger** — it
reconstructs the RFC 9421 signature base, verifies it against your key,
and shows exactly which component mismatches. See
[Signing debugger](signing-debugger.md).

## What adcp-test does not do for buyers

Honestly: adcp-test does not drive your buyer for you. There is no
scripted "buyer conformance suite" — the buyer is your code, and the
harness gives it a controllable seller to talk to. Scenario packs and
chaos mode run against seller endpoints (they assert on seller
behavior); for buyer-side chaos, use the mock's own fault injection,
which shapes what the *buyer* sees.

## Side-by-side

| Feature | Tests |
|---|---|
| Conformance, scenarios, load, lifecycle, fuzzing, spec-upgrade diffs | **Seller** implementations |
| Signing debugger | **Buyer** implementations (your signatures) |
| Mock server, record/replay, cassettes | **Buyer** implementations (your behavior against a controlled seller) |
| Webhook listener | **Seller** implementations (verifies the seller emits webhooks correctly) |
| Snapshots, evidence reports, MCP server, GitHub Action | Both / workflow |
