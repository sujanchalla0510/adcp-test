# Webhooks

Start a localhost-only listener, point the seller's webhook configuration
at the printed URL, and watch deliveries land on an ordered timeline with
pretty-printed bodies. Authorization-style headers are redacted at
capture; bodies can be truncated.

```sh
./adcp-test webhook-listen --port 8090 [--out deliveries.json]
```

API: `POST /api/webhooks/listen`, `GET /api/webhooks/deliveries`,
`POST /api/webhooks/clear`, `POST /api/webhooks/stop`.
