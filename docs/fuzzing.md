# Fuzzing

Throw malformed, truncated, deeply nested, oversized, batched, mistyped,
and unknown-method JSON-RPC payloads at the endpoint and record crashes,
hangs, non-JSON responses, and transport errors. Generation is seeded:
the report records the seed, so any run replays exactly.

```sh
./adcp-test fuzz --target http://127.0.0.1:8089 --iterations 200 [--seed 42]
```

**Safety:** fuzzing targets localhost only. Non-localhost targets are
refused unless you pass `--allow-remote`. API: `POST /api/fuzz/run`.

Never point the fuzzer at a production seller.
