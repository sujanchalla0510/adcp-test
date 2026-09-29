# Mock builder

Config-driven mock seller services: describe fake sellers as YAML, run
them headless or from the visual editor, and replicate a real
integration without touching it.

**This is the buyer-testing harness.** Conformance, scenarios, load, and
lifecycle probe *seller* implementations; the mock server exists so you
can point a *buyer* agent at something you control and see how it
behaves. See [Testing buyer agents](testing-buyer-agents.md) for the
full loop.

## What mocks do

- **Request matching** — routes match tool calls by name and argument
  patterns (glob wildcards, nested maps, dotted keys).
- **Responses** — inline JSON, fixture files, nth-call sequences, or
  `{{args.*}}` templates that echo request fields back so mocks stay
  coherent across a session.
- **Lifecycle state machines** — media buys move draft → active → paused
  → active → cancelled; illegal transitions are rejected like the real
  thing.
- **Latency profiles** — fixed, p50/p99 distributions, periodic spikes.
- **Fault injection** — per-route error/timeout/malformed rates, the
  buyer-side equivalent of chaos mode.
- **Compose** — one config file can define several mocks, each on its
  own address.
- **Record mode** — proxy a real seller, capture the traffic, and
  generate a reusable mock config from it: the fastest path to
  "replicate this integration".

## Use it

UI: **Mock builder** → edit visually, or import/export YAML to keep
configs in source control.

```sh
./adcp-test mock --config integration.yaml
# Ctrl-C stops; a record-mode mock writes its generated config on exit
```

See `examples/mock-seller.yaml` for an annotated config: the full
required tool surface, structured rejections for invalid probes, and a
lifecycle state machine.
