-- alerts records every alert alertchain has observed, keyed by
-- Alertmanager-compatible fingerprint. Used to power UI views
-- ("currently firing", "expired") and mute-match lookups (which
-- alerts currently match a given mute's matchers).
--
-- Scope is deliberately narrow. Only the fields alertchain itself
-- needs are stored:
--   - labels       : drive mute matching and identify the alert
--   - timing       : sender's view (starts_at/ends_at) for the
--                    firing/expired derivation; alertchain's own
--                    observation (first_seen_at/last_seen_at)
--
-- Annotations (summary, description, runbook, etc.) and generatorURL
-- are deliberately NOT persisted: they are presentation content for
-- the downstream notification destination (Slack, PagerDuty, etc.),
-- not alertchain's responsibility. They pass through the Alert
-- struct unchanged into webhook payloads. Alertmanager itself does
-- not persist alerts to disk either; it holds them in memory and
-- garbage-collects after the resolve_timeout.
--
-- Retention is the operator's responsibility; alertchain does not
-- garbage-collect rows.
CREATE TABLE alerts (
    fingerprint    TEXT PRIMARY KEY,
    labels         JSONB NOT NULL,
    starts_at      TIMESTAMPTZ NOT NULL,
    ends_at        TIMESTAMPTZ,
    first_seen_at  TIMESTAMPTZ NOT NULL,
    last_seen_at   TIMESTAMPTZ NOT NULL
);

-- JSONB containment index, used by:
--   SELECT ... WHERE labels @> $mute_matchers::jsonb
CREATE INDEX idx_alerts_labels_gin ON alerts USING GIN (labels);

-- firing vs expired filter.
CREATE INDEX idx_alerts_ends_at ON alerts(ends_at);

-- recent-first listing.
CREATE INDEX idx_alerts_last_seen_at ON alerts(last_seen_at DESC);
