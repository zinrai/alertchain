# alertchain

An iptables-style notification router for Prometheus alerts.

`alertchain` evaluates alerts against an ordered, flat list of rules.
The first matching rule dispatches the alert to its receiver; the
chain stops there unless the rule sets `continue: true`. There is no
routing tree, no inheritance, no inhibit rules, and no gossip-based
clustering.

Webhooks are the only outbound protocol. Slack, Email, PagerDuty,
and other destinations are reached by pointing a webhook receiver at
a small adapter service. The adapter is outside alertchain's
responsibility.

For the reasoning behind these choices, see [DESIGN.md](DESIGN.md).

## Install

alertchain expects a PostgreSQL database with its schema already
applied. The schema is managed out-of-band; see
[Database setup](#database-setup) before starting `alertchain serve`.

## Subcommands

```
DATABASE_URL=postgres://user:pass@host/db alertchain serve --config alertchain.yaml --listen :9093
alertchain trace  --config alertchain.yaml --alert-file alert.json
alertchain check  --config alertchain.yaml
alertchain verify --config alertchain.yaml --verify-cases routing.yaml
```

`serve` is the daemon. `trace`, `check`, and `verify` are pure
functions over the config file. They do not require a running server
and do not touch the database.

The PostgreSQL DSN is supplied through the `DATABASE_URL`
environment variable. URL form
(`postgres://user:pass@host:5432/db?sslmode=disable`) and key/value
form (`host=... user=... dbname=... sslmode=...`) are both accepted.
Passing the DSN as a flag is intentionally not supported to keep
credentials out of process listings.

Mute operations are available only over the HTTP API. There is no
`alertchain mute` subcommand. Use `curl` or build a thin CLI on top
of the API.

## Database setup

alertchain does not manage its schema. SQL migrations live in
`migrations/` and follow the
[golang-migrate](https://github.com/golang-migrate/migrate) naming
convention (`NNNN_name.up.sql` / `NNNN_name.down.sql`).

## Configuration

```yaml
receivers:
  - name: infra-webhook
    type: webhook
    url_file: /etc/alertchain/infra-webhook.url

  # The built-in "discard" receiver is always available. Do not
  # declare it; reference it directly from rules below.

rules:
  - name: critical-to-infra
    match:
      severity: critical
      team: infra
    receiver: infra-webhook
    continue: true

  - name: critical-mirror
    match:
      severity: critical
    receiver: infra-webhook

  - name: infra-warnings
    match:
      team: infra
      severity: warning
    receiver: infra-webhook

  - name: noisy-suppress
    match:
      source: noisy-system
    receiver: discard

  - name: catch-all-discard
    match: {}
    receiver: discard
```

Configuration changes require a process restart. There is no
hot-reload. Because state is persisted in PostgreSQL, rolling
restarts are non-disruptive.

### Rules

Each rule has these fields:

| Field      | Required | Default | Notes                                 |
|------------|----------|---------|---------------------------------------|
| `name`     | yes      | -       | Unique. Used as key in notification history. |
| `match`    | no       | `{}`    | Label-to-value equality map. Empty or omitted = catch-all. |
| `receiver` | yes      | -       | Must reference a defined receiver or the built-in `discard`. |
| `continue` | no       | `false` | If true, evaluation proceeds.         |

The `name` field is the key under which notification history is
recorded, so renaming a rule resets delivery state for all
fingerprints it covers (the next firing of each fingerprint will be
delivered again).

### Match conditions

`match` is a map from label name to the expected value. **All
entries must equal the corresponding labels on the alert** for the
rule to apply (logical AND). Matching is plain equality — there is
no `!=`, no regex, no alternation.

```yaml
match:
  severity: critical
  team: infra
```

An empty map (`match: {}`) or omitted `match` field is a catch-all
that matches every alert.

Conditions that other systems express with operators or regex are
expressed in alertchain by writing multiple rules and relying on
first-match semantics. For instance:

- "Send critical alerts from any team except infra to general
  on-call" -> write a specific `team: infra` rule first, then a
  `severity: critical` catch-all after it.
- "Mute every alert from sources matching `noisy-*`" -> write one
  rule per noisy source.

This is a deliberate tradeoff: a small loss in matcher expressiveness
for a large gain in readability. Each rule reads in under a second.
See [DESIGN.md -> Matchers are equality only, not
expressive](DESIGN.md#matchers-are-equality-only-not-expressive) for
the full reasoning.

### Receivers

Only one configurable type: `webhook`. Plus the built-in `discard`.

```yaml
- name: my-webhook
  type: webhook
  url: https://example.com/hook        # or url_file: /path/to/url
```

The built-in `discard` receiver is always available. A rule that
targets `receiver: discard` drops matching alerts without notifying
anywhere. Use it for the catch-all final rule and for explicit
suppression of known-noisy sources. Declaring a user receiver with
the name `discard` or the type `discard` is an error.

### Reaching destinations other than a generic webhook

For Slack, Email, PagerDuty, MS Teams, or any other destination,
run a small adapter service and point a webhook receiver at it.
Examples of adapter strategies:

- **Slack:** Slack's incoming webhooks accept a `{"text": "..."}`
  JSON body. Run a ~50-line adapter that receives the alertchain
  payload and POSTs a translated body to the Slack webhook URL.
- **Email:** Run a small SMTP relay service (in any language) that
  receives the alertchain payload and sends mail.
- **PagerDuty:** PagerDuty Events API v2 accepts a JSON body. An
  adapter translates the alertchain payload to the PagerDuty
  schema.

This split keeps alertchain itself small and testable, and lets
each adapter evolve independently. Adapters can be written in any
language and deployed with any tool.

### Webhook payload format

Webhook receivers receive an HTTP POST with `Content-Type:
application/json` and a body of the following shape (Alertmanager
v2 compatible):

```json
{
  "receiver": "infra-webhook",
  "status": "firing",
  "alerts": [
    {
      "status": "firing",
      "labels": {
        "alertname": "DiskFull",
        "severity": "critical",
        "team": "infra"
      },
      "annotations": {
        "summary": "Disk is full"
      },
      "startsAt": "2026-05-19T10:00:00Z",
      "endsAt": "0001-01-01T00:00:00Z",
      "generatorURL": "http://prometheus.example.com/...",
      "fingerprint": "8c0e3a3f57e0a6f1"
    }
  ]
}
```

The top-level `status` is `"firing"` when the alert is active,
`"resolved"` when `endsAt` is at or before the current time. The
per-alert `status` field carries the same value (the payload
contains a single alert). The per-alert `fingerprint` is computed
via `model.LabelSet.Fingerprint()` from
`github.com/prometheus/common/model` — the same algorithm
Alertmanager uses — so identical label sets produce identical
fingerprints whether the source is alertchain or Alertmanager.

## Notification semantics in brief

alertchain sends one firing notification and one resolved
notification per `(rule, fingerprint)` pair. It does not retry on
its own; the sending side (Prometheus, etc.) is expected to keep
firing the alert until it resolves, and alertchain delivers again
whenever the next push represents a new state.

**Webhook receivers must implement idempotency using the alert
fingerprint as the deduplication key.** Duplicate deliveries can
occur due to network failures or concurrent processing; alertchain
does not attempt to prevent them.

For the full state machine, the database/webhook interaction
rules, and the responsibility split, see
[DESIGN.md -> Notification semantics](DESIGN.md#notification-semantics).

## HTTP API

### `/api/v2/alerts`

The alert ingestion endpoint accepts the Alertmanager v2 wire format
exactly as Prometheus, vmalert, and promxy emit it. Request types are
imported from `github.com/prometheus/alertmanager/api/v2/models`, so
these clients work against alertchain without modification.

| Method | Path             | Notes                          |
|--------|------------------|--------------------------------|
| POST   | `/api/v2/alerts` | Accept a JSON array of Alertmanager v2 postableAlert objects. |
| GET    | `/api/v2/alerts` | Returns empty array (alertchain does not cache an active set). |

POST returns 500 when a database read fails (mute lookup or history
lookup). Senders that follow Alertmanager conventions retry on the
next push, which is the intended recovery path.

This is the only path where alertchain mirrors an Alertmanager API.
Other Alertmanager endpoints — `/api/v2/silences`, `/api/v2/receivers`,
`/api/v2/alertgroups`, `/api/v2/status` — are deliberately not
implemented. Mute is alertchain's own concept (see below) and is
not an Alertmanager-silence substitute.

### `/api/v1/mutes`

The mute API is alertchain's own. The matcher payload is a JSON
map from label name to expected value — the same shape used in the
YAML rule `match` field.

| Method | Path                  | Notes                                                   |
|--------|-----------------------|---------------------------------------------------------|
| GET    | `/api/v1/mutes`       | List mutes. Returns array including computed `status`.  |
| POST   | `/api/v1/mutes`       | Create a mute. Returns `{"id": "..."}`.                 |
| GET    | `/api/v1/mutes/{id}`  | Get one mute by id.                                     |
| DELETE | `/api/v1/mutes/{id}`  | Expire a mute immediately.                              |

Example mute payload:

```json
{
  "matchers": {
    "severity": "info",
    "team": "infra"
  },
  "starts_at": "2026-05-22T13:00:00Z",
  "ends_at":   "2026-05-22T14:00:00Z",
  "comment":   "DB maintenance",
  "created_by": "alice"
}
```

The GET responses additionally carry a `status` field computed from
the current time, one of `"pending"`, `"active"`, or `"expired"`.

The version numbers `/api/v1/` and `/api/v2/` are coincidentally
adjacent but unrelated; they reflect the version of each path's own
schema. The mute API is not an Alertmanager-silence subset, and
shaping it like one would import design pressure alertchain does
not want to carry. For the reasoning, see
[DESIGN.md -> Motivation](DESIGN.md#motivation) and
[DESIGN.md -> Compatibility boundary](DESIGN.md#compatibility-boundary).

### `/metrics`

| Method | Path       | Notes                                       |
|--------|------------|---------------------------------------------|
| GET    | `/metrics` | Prometheus text exposition format.          |

Counters exposed:

| Name                                              | Meaning                                    |
|---------------------------------------------------|--------------------------------------------|
| `alertchain_alerts_received_total`                | POST /api/v2/alerts requests accepted.     |
| `alertchain_notify_success_total`                 | Webhook deliveries that returned 2xx.      |
| `alertchain_notify_failure_total`                 | Webhook deliveries that errored or returned non-2xx. |
| `alertchain_mute_lookup_failure_total`            | Database errors while checking mutes.      |
| `alertchain_history_lookup_failure_total`         | Database errors while reading notification history. |
| `alertchain_history_write_failure_total`          | Database errors while writing notification history. |

There are no histograms or label dimensions; this is a deliberate
choice to keep the metrics surface aligned with the rest of
alertchain's minimalism. Operators who need request-duration data
should measure it at the reverse proxy in front of alertchain.

### Health endpoints

| Method | Path        | Notes      |
|--------|-------------|------------|
| GET    | `/-/healthy` | Liveness.  |
| GET    | `/-/ready`   | Readiness. |

### Authentication

All HTTP endpoints are unauthenticated, matching the equivalent
Alertmanager endpoints. Put a reverse proxy (nginx, Caddy, an
authenticating sidecar, etc.) in front of alertchain for access
control. This is the same expectation Alertmanager places on its
operators.

## Database

PostgreSQL is the only supported backend. SQLite and other embedded
databases are intentionally unsupported, and so is in-process
clustering — both are delegated to the database layer. See
[Database setup](#database-setup) for schema management.

## Routing verification

`alertchain verify` runs a YAML table of routing expectations against
the configuration. It is intended for pre-deployment verification in
CI: a PR that changes `alertchain.yaml` is accompanied by a change
to the verify file, and a regression in routing behavior fails the
CI job before the bad configuration is deployed.

```
alertchain verify --config alertchain.yaml --verify-cases routing.yaml
```

Exit code `0` means all cases passed; `1` means one or more failed.
The command is a pure function over the configuration: no database,
no network, no running server.

Run `alertchain check` alongside `verify`: `check` validates
configuration syntax (referenced receivers exist, no duplicate rule
names), and `verify` validates routing behavior. Each catches a
class of problem the other does not.

### Case file format

Each case declares an alert (as a label map) and the routing outcome
that the configuration must produce. **Both the matched rules and
the reached receivers are required**: asserting one without the
other would leave the unchecked dimension free to drift unnoticed.

```yaml
verify:
  - name: critical infra alert
    labels:
      severity: critical
      team: infra
    expect:
      rules: [critical-to-infra, critical-mirror]
      receivers: [infra-webhook, infra-webhook]
    description: |
      critical-to-infra has continue:true, so critical-mirror also
      fires. Both decisions route to the same webhook, hence the
      duplicate entry in receivers.

  - name: unmatched alert falls through to catch-all
    labels:
      alertname: SomethingUnknown
    expect:
      rules: [catch-all-discard]
      receivers: [discard]
```

The comparison is order-independent for both `rules` and
`receivers`, but a duplicate counts as a separate entry: a rule that
fires twice via `continue:true` to the same receiver produces two
entries in `receivers`.

A sample case file matching the configuration in
`examples/alertchain.yaml` is at `examples/verify-cases.yaml`. See
`examples/README.md` for the full end-to-end walkthrough.

### Scope

`alertchain verify` checks the static routing table only. Mutes are
runtime state managed via the HTTP API and are not part of the
deployable configuration, so they are out of scope. Use `alertchain
trace` against a running server for "would this mute suppress this
alert" questions.

## Trace example

```
$ cat > /tmp/alert.json <<'EOF'
{"labels":{"severity":"critical","team":"infra"}}
EOF
$ alertchain trace --config alertchain.yaml --alert-file /tmp/alert.json

Alert:
  fingerprint: 8c0e3a3f57e0a6f1
  labels:      severity="critical", team="infra"

Mute check: skipped (no store provided)

Rule evaluation:
  [1] critical-to-infra               MATCH  -> infra-webhook  (continue)  severity="critical", team="infra"
  [2] critical-mirror                 MATCH  -> infra-webhook  (stop)      severity="critical"

Final decisions:
  -> notify  (rule: critical-to-infra, type: webhook)
  -> notify  (rule: critical-mirror, type: webhook)
```

To pass an inline JSON without creating a temp file, use shell
process substitution:

```
alertchain trace --config alertchain.yaml \
    --alert-file <(echo '{"labels":{"severity":"critical"}}')
```

## License

This project is licensed under the [MIT License](./LICENSE).
