# Load testing

The load engine hammers a seller endpoint with N concurrent virtual
users, each repeating one tool call or a whole scenario pack, with a
linear ramp-up and a duration or total-iteration stop condition. Live
progress (requests, rps, p50/p99, errors, timeouts) streams to the Load
screen's canvas charts; the final report carries p50/p95/p99 latency,
throughput, error/timeout rates, and CI threshold verdicts.

```yaml
# load.yaml
target_url: http://127.0.0.1:8080
tool: get_products
arguments: {}
concurrency: 20
ramp_up: 30s
duration: 60s
# iterations: 1000        # alternative stop condition
request_timeout: 10s
thresholds:
  p99_ms_lt: 2000
  error_rate_lt: 0.01
  timeout_rate_lt: 0.01
```

```sh
./adcp-test load --config load.yaml
# JSON report on stdout; exit 0 when every threshold passes, 1 otherwise
# --target / --concurrency / --duration / --iterations override the config
```

**Safety:** non-localhost targets are refused unless you pass
`--allow-remote` (or `allow_remote: true` in the config / API body), so a
typo can't turn a load test into an accidental production hammering.

API: `GET /api/load/presets` (smoke / ramp / soak starters);
`POST /api/load/run` with either `{"config_yaml": "..."}` or structured
fields streams `progress` events then a final `result` event
`{"id": ..., "result": ...}`; `GET /api/load/results/{id}` retrieves a
finished run's report.
