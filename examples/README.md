# alertchain examples

Working artifacts you can drop into a local PostgreSQL plus a webhook
receiver to exercise alertchain end-to-end. Used by the project for
operational verification; doubles as a starter set for new users.

## Files

- `alertchain.yaml`: chain with five receivers (three success, one
  intentionally failing, one low-priority log sink) and six rules
  covering: critical fan-out via `continue: true`, severity routing,
  routing low-value sources to the log sink, and a final catch-all to
  the log sink.
- `verify-cases.yaml`: routing expectations matching `alertchain.yaml`.
  Drives `alertchain verify`.
- `alerts/*.json`: one alert per file in the alertchain `Alert`
  shape. Use directly with `alertchain trace`, or wrap in an array
  (`jq '[.]'`) to POST to `/api/v2/alerts`.

## Scenarios

| File | Labels | Expected route |
| --- | --- | --- |
| `alerts/critical-infra.json` | `severity=critical, team=infra` | `critical-to-infra` + `critical-mirror` (continue) |
| `alerts/critical-platform.json` | `severity=critical, team=platform` | `critical-mirror` only |
| `alerts/infra-warning.json` | `severity=warning, team=infra` | `infra-warnings` |
| `alerts/noisy-suppress.json` | `source=noisy-system` | `noisy-suppress` -> `log-sink` |
| `alerts/catchall.json` | nothing matched above | `catch-all` -> `log-sink` |
| `alerts/chaos-fail.json` | `chaos=fail` | `failing-webhook` (closed port; exercises the failure path) |

## End-to-end walkthrough

Prerequisites:

- PostgreSQL reachable, with the `alertchain` schema applied. See
  `migrations/` in the repository root.
- A webhook receiver listening at `http://127.0.0.1:8000` that returns
  2xx. Any request-dumping HTTP server works (`python3 -m http.server`
  for a smoke test, a small Go/Node listener if you want to inspect
  the body).

Validate the config and the routing expectations without running
anything:

```bash
alertchain check  --config examples/alertchain.yaml
alertchain verify --config examples/alertchain.yaml \
                  --verify-cases examples/verify-cases.yaml
```

Start the server:

```bash
DATABASE_URL='postgres://USER:PASS@HOST:5432/alertchain?sslmode=disable' \
  alertchain serve --config examples/alertchain.yaml --listen 127.0.0.1:9093
```

POST an alert (the JSON files are single Alert objects; the API takes
an array, so wrap with `jq`):

```bash
curl -X POST http://127.0.0.1:9093/api/v2/alerts \
  -H 'Content-Type: application/json' \
  -d "$(jq '[.]' examples/alerts/critical-infra.json)"
```

Trace evaluation against the config (no server, no DB):

```bash
alertchain trace --config examples/alertchain.yaml \
                 --alert-file examples/alerts/critical-infra.json
```

Inspect state:

```bash
# delivery history
psql -h HOST -U USER -d alertchain \
  -c 'SELECT rule_name, fingerprint, status, sent_at FROM notifications ORDER BY rule_name;'

# operational counters
curl -s http://127.0.0.1:9093/metrics | grep ^alertchain_
```

Send the same alert again to observe dedup (the row in `notifications`
does not change). Then add `"endsAt": "<past timestamp>"` to the same
file (or pipe through `jq`) to observe the `firing-sent -> resolved-sent`
transition.

Send `alerts/chaos-fail.json` to exercise the failure path. The
sender still receives HTTP 200, but `notifications` records
`firing-failed` and `alertchain_notify_failure_total` increments.
