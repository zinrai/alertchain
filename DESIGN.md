# alertchain — Design

This document describes why `alertchain` is built the way it is. It is
intended for people who want to understand or modify the project. The
README focuses on how to use alertchain; this file focuses on the
reasoning behind that surface.

## Motivation

Alertmanager's repeating-notification model did not fit our use
case. To compensate, we built tools whose only purpose was to
suppress those repetitions — auto-silence scripts, ChatOps bots,
schedulers, watchers — and ended up maintaining four or more such
components. They existed because Alertmanager keeps re-firing the
same alert as long as the condition holds, not because they
addressed an operational need on their own.

Alertmanager's other features — inheritance in the routing tree,
inhibit rules, group/repeat/throttle timers — were also unnecessary
for our use case, and the configuration cost they imposed was high.
Predicting what would happen to a given alert required holding the
whole routing tree plus several internal timers in mind.

`alertchain` is a deliberate reimplementation of the notification
routing subsystem with different tradeoffs: a flat ordered rule
list, no timers, and — critically — one firing notification and one
resolved notification per occurrence. The state machine
(`firing-sent` / `firing-failed` / `resolved-sent` /
`resolved-failed`) is described under Notification semantics. This
single design choice removes the operational pressure that produced
the satellite tooling. The set of components needed to operate
alertchain is alertchain itself, a PostgreSQL database, and the
webhook receivers.

## Invariants

The invariants below are deliberately listed together rather than
scattered through the codebase, so that any proposed change can be
checked against them.

1. **Rule evaluation is local.** Predicting what happens to an alert
   requires reading only one rule. No inheritance, no defaults block,
   no implicit parent fallback, no hardcoded timer constants. A
   rule's behavior is fully determined by its own fields. Match
   conditions are a label-name to expected-value map with
   equality-only semantics, so each rule is readable at a glance.

2. **State lives in one place.** All persisted state (mutes,
   notification history) is in a single PostgreSQL database. HA is
   delegated to the database layer rather than implemented in the
   application. The database is the source of state, not a thing
   alertchain owns; schema management is delegated to standard
   tooling (see README).

3. **Core logic is a Go package.** `Chain.Match`, `Chain.Process`,
   and `Mute.MatchesAlert` are exercised directly from tests, the
   HTTP server, and the CLI subcommands without intermediate
   abstraction.

4. **One outbound protocol.** Only webhooks are emitted by
   alertchain. Translation to other protocols (Slack, Email,
   PagerDuty) is the responsibility of external adapters.

5. **Observability is structural.** Two CLI subcommands serve as
   first-class operational surfaces: `alertchain trace` shows how
   a single alert would be evaluated, for interactive debugging;
   `alertchain verify` runs a YAML-defined table of routing
   expectations against the configuration, for pre-deployment
   verification in CI. To inspect the rule set, read the
   configuration file directly; the flat, inheritance-free format
   means the YAML itself is the evaluation order. Operational
   counters are exposed at `/metrics` in the Prometheus text
   format.

6. **Sender-facing wire compatibility.** alertchain accepts the
   Alertmanager v2 `POST /api/v2/alerts` payload exactly as
   Prometheus, vmalert, and promxy emit it. Request types are
   imported from `github.com/prometheus/alertmanager/api/v2/models`,
   not hand-written, so upstream schema changes are picked up
   through a dependency update. Fingerprints are computed via
   `model.LabelSet.Fingerprint()` from `prometheus/common/model`,
   matching Alertmanager's algorithm byte-for-byte; a webhook
   receiver that deduplicates Alertmanager-sourced alerts by
   fingerprint will deduplicate alertchain-sourced alerts
   identically. Other Alertmanager API endpoints — silences, alert
   groups, status, receivers — are deliberately not implemented.
   The mute API is alertchain's own (`/api/v1/mutes`) and is not a
   silence subset.

## Non-goals

These are intentional exclusions. Each one is a thing alertchain
could reasonably do, but does not, because the cost of doing it
outweighs the value.

- **Inhibit rules.** Use Prometheus `unless` operator, or route the
  alert to the built-in `discard` receiver.
- **Routing tree with inheritance.** Rules are a flat ordered list.
- **A `defaults` block in the config.** There is no per-rule timer
  to default; every rule is fully self-contained.
- **Built-in clustering or gossip.** Share the database for HA.
- **Reminder notifications / `repeat_interval`.** alertchain
  delivers one firing notification and one resolved notification per
  occurrence. Escalation cadences belong in whichever system
  receives the webhook (PagerDuty, Opsgenie, a ChatOps bot, etc.).
- **Built-in Slack, Email, PagerDuty, or other protocol-specific
  notifiers.** Route to a webhook and translate in an adapter.
- **SQLite or other embedded database backends.** PostgreSQL only.
- **Multi-tenant isolation.** One team, one config file.
- **An Alertmanager configuration migration tool.** The data models
  differ by design; manual review is required.
- **Config hot reload.** A configuration change requires a process
  restart. Because state lives in PostgreSQL (invariant 2), a
  rolling restart is non-disruptive: replacement processes pick up
  notification history and mutes from the database.
- **Authentication on the HTTP API.** Both `/api/v2/alerts` and
  `/api/v1/mutes` are unauthenticated, matching the equivalent
  Alertmanager endpoints. Authentication is the responsibility of a
  reverse proxy in front of alertchain.
- **Matcher expressiveness beyond label equality.** No `!=`, no
  regex, no alternation. See the next section for the reasoning.
- **Embedded schema migration.** Use golang-migrate or any tool
  that reads the same file convention.

## Matchers are equality only, not expressive

alertchain's match conditions are a YAML or JSON map from label name
to expected value: every entry must be equal to the corresponding
label on the alert. There is no operator, no regex, no negation. A
rule that matches `severity=critical AND team=infra` is written as:

```yaml
match:
  severity: critical
  team: infra
```

Conditions that an expressive matcher language would express in one
line — "severity is anything but critical", "alertname starts with
Disk", "team is one of A, B, C" — are expressed by writing multiple
rules in the order that produces the desired routing. This is the
same approach iptables uses: each rule line is an equality condition,
and complex policies emerge from the sequence rather than from
condition syntax.

The tradeoff is deliberate. Complex matcher conditions in a single
rule are a hidden cost:

- **Reading time.** A rule with a regex or a chain of negations
  takes seconds to parse mentally. A rule that says exactly three
  equalities takes a fraction of that. With dozens of rules in a
  file, the difference accumulates.
- **Writing time.** When the matcher language has multiple
  operators, the author repeatedly faces "should this be one rule
  with a complex condition or two rules?" and the answer varies by
  taste, leading to inconsistent style across the file.
- **Debugging time.** A failed match in a complex condition is
  harder to diagnose: was the regex wrong, the negation flipped, the
  operator precedence misread? Equality has none of these.

By restricting matchers to equality, alertchain enforces the
"sequence of equality rules" style. The expressiveness lost — one
rule cannot express "severity is anything but critical" on its own
— is recovered by splitting that intent across rules and relying on
first-match semantics. The gain is that every rule reads as a list
of equalities, which makes invariant 1 ("rule evaluation is local")
strong on the reader's side as well as on the evaluator's.

A practical consequence: the matcher syntax is no longer a subset
of Prometheus or Alertmanager matchers. Users familiar with PromQL
matchers (`{job=~"foo.*"}`) need to know that the routing layer is
plain equality. The README states this explicitly so the
expectation does not carry over from PromQL.

## Notification semantics

alertchain dispatches each notification with a single HTTP POST and
does not retry within a single Process invocation. The sending side
(Prometheus, vmalert, promxy) is expected to keep firing alerts
until they resolve, which provides the practical retry mechanism
without alertchain having to implement queues or backoff itself.

### Status state machine

Per `(rule, fingerprint)` pair, the `notifications` table records
the status of the most recent attempt as one of four values:

- `firing-sent` — the firing alert was delivered successfully
- `firing-failed` — the firing alert was attempted but failed
- `resolved-sent` — the resolved alert was delivered successfully
- `resolved-failed` — the resolved alert was attempted but failed

Whether an incoming alert is "firing" or "resolved" is determined
the same way Alertmanager determines it: `endsAt` at or before now
means resolved, otherwise firing. The boundary is the closed
interval (`endsAt <= now` is resolved), matching the closed-interval
semantics of `Mute.Active` so that "equal to now" is handled
consistently across the codebase. Prometheus and similar senders set
`endsAt` explicitly when they want a resolution to be delivered
(they continue re-sending the resolved alert for a few minutes).

The `Process` logic is then: **deliver unless the desired status
(firing-sent or resolved-sent) is already recorded.** Three
operating rules follow from this:

1. **One firing notification per occurrence.** Once `firing-sent` is
   recorded for a `(rule, fingerprint)` pair, alertchain stops
   delivering firing alerts for that pair, no matter how often the
   sending side re-sends them. There is no `repeat_interval` and no
   reminder cadence: alertchain is a router, not a reminder.
   Escalation and reminders are the responsibility of whichever
   system receives the webhook.

2. **One resolved notification per occurrence.** Once
   `resolved-sent` is recorded, alertchain stops delivering resolved
   alerts for the same pair. Prometheus typically re-sends resolved
   alerts for ~15 minutes; only the first reaches the webhook.

3. **A firing alert after `resolved-sent` is a new occurrence.**
   When the same fingerprint fires again after being resolved,
   alertchain delivers it (the transition `resolved-sent -> firing`
   triggers a new delivery). Symmetrically, a `firing-sent` state
   followed by a resolution delivers the resolved notification.

Webhook failures roll into the same state machine via the `*-failed`
statuses. Any non-success status (`firing-failed`,
`resolved-failed`, or a different status than the desired
one) causes the next matching alert from the sending side to be
delivered again. Once the webhook recovers, the next incoming alert
carries the delivery through and updates the status to the `*-sent`
variant.

### Database and webhook interaction

The boundary between the database, the webhook, and the sending side
is governed by an explicit error contract on `Chain.Process`:

> `Process` returns a non-nil error if and only if the alert could
> not be fully evaluated due to a database **read** failure (mute
> lookup or history lookup). Webhook delivery failures and history
> **write** failures are recorded and/or logged but never surface as
> an error from `Process`.

The rationale follows from a single observable distinction: whether
a side-effect has already occurred.

- **Read failure before sending: abort.** If a mute lookup or the
  notification-history lookup fails before the webhook POST,
  alertchain returns an error from `Process`. The HTTP handler
  surfaces this as a 5xx response, prompting the sending side
  (Prometheus, vmalert) to retry on its next push. A missed alert
  is recovered passively by the sender; an alert leaked while mute
  state is unknown would be harder to correct after the fact.

- **Webhook failure: record, do not abort.** If the webhook POST
  fails, the status is set to the `*-failed` variant and `Process`
  continues with any remaining decisions. The next matching alert
  from the sender side will be retried. alertchain does not run a
  background worker or queue: recovery happens passively.

- **History write failure: log, do not abort.** If `RecordAttempt`
  fails after a successful webhook POST, the webhook side-effect
  has already occurred and cannot be undone by aborting. The next
  push from the sender side will produce another delivery, which
  webhook receivers deduplicate by fingerprint. The failure is
  counted in `alertchain_history_write_failure_total` so that
  operators can detect a persistently broken database write path.

### Duplicate delivery and the responsibility split

Duplicate deliveries to the same webhook can still occur in several
situations:

- Network conditions where the webhook actually received the request
  but alertchain considered it failed (timeouts, dropped
  connections).
- The webhook succeeds but the subsequent database write fails,
  leaving the next firing alert from the sending side to trigger
  another delivery.
- Concurrent processing of the same alert from the sending side.

alertchain does not attempt to prevent these. The responsibility
split mirrors Alertmanager: each alert in the payload carries a
`fingerprint` field, and **webhook receivers must implement
idempotency using the alert fingerprint as the deduplication key.**
This is the same expectation Alertmanager places on its own webhook
receivers, and it keeps alertchain itself free of complexity that
would not change the guarantees a downstream system would see.

### Fan-out failure semantics

When a rule with `continue: true` precedes another matching rule,
each receiver is attempted independently. A failure on one rule's
receiver does **not** skip subsequent rules' receivers: the loop
records the failure for that `(rule, fingerprint)` pair and
proceeds to the next decision. This is the natural behavior for the
"mirror to a second team" pattern and the "primary + observer" fan-out
pattern; per-rule independence avoids one slow or down receiver
blocking the others.

## Compatibility boundary

alertchain's compatibility with Alertmanager is asymmetric and
intentional. It exists for the sender side, not the operator side.

### Sender side: compatible

For alerts arriving from Prometheus, vmalert, promxy, and other
metric senders, alertchain accepts the Alertmanager v2 wire format
without translation. Two layers:

1. **JSON wire format on `POST /api/v2/alerts`.** Types are
   imported directly from `prometheus/alertmanager/api/v2/models`.
   Schema changes upstream are picked up by a dependency update.

2. **Behavioral semantics for the alert lifecycle** (manually
   mirrored, not imported): firing-vs-resolved determination uses
   `endsAt <= now`, matching Alertmanager's behavior. Prometheus
   sets `endsAt` explicitly — a future timeout while firing, a past
   `ResolvedAt` once resolved — and alertchain consumes that signal
   without computing a resolve_timeout of its own.

3. **Fingerprint algorithm.** `Alert.Fingerprint()` delegates to
   `model.LabelSet.Fingerprint()` from
   `github.com/prometheus/common/model`, the same call Alertmanager
   uses. Identical label sets produce identical fingerprints
   regardless of which system emitted the alert, so webhook
   receivers can deduplicate uniformly.

### Operator side: deliberately not compatible

Operator-facing endpoints — silences, alert groups, status,
receivers — are not implemented. The mute API at `/api/v1/mutes`
is alertchain's own:

- Different URL namespace (`/api/v1/mutes` vs the Alertmanager
  `/api/v2/silences`).
- Different payload shape (matchers are a `{name: value, ...}`
  equality map, not the Alertmanager
  `{name, value, isRegex, isEqual}` matcher objects).
- Different name (mute vs silence) because the action it performs
  on a one-shot router is suppression for a single delivery, not
  silencing of a repeating cadence.

This break is a feature, not a regression. The Alertmanager silence
API is shaped by the auto-silence, ChatOps, and schedule-management
tooling that grows around the repeating-notification model (see
Motivation). Re-implementing that surface in alertchain would
preserve compatibility with tools whose existence alertchain is
trying to make unnecessary. Operators who need a CLI for mute
management can build a client against the alertchain API —
the schema is one struct, two endpoints, and a `map[string]string`
of matchers.

The Alertmanager silence package (`silence/`, `silence/silencepb/`)
is not imported. Its primary responsibility is gossip-mesh state
sharing, which alertchain intentionally does not have; the state
machine boundary semantics that mattered (closed-interval
`[StartsAt, EndsAt]` active window) are mirrored in `Mute.Active`
without taking on the gossip machinery.

The matcher syntax in the configuration file and the mute API is
alertchain's own and is not a subset of any Alertmanager or
Prometheus syntax. It is plain label equality, expressed as a YAML
or JSON map. See "Matchers are equality only, not expressive" above
for the reasoning.
