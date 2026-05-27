CREATE TABLE mutes (
    id          TEXT PRIMARY KEY,
    matchers    JSONB NOT NULL,
    starts_at   TIMESTAMPTZ NOT NULL,
    ends_at     TIMESTAMPTZ NOT NULL,
    comment     TEXT,
    created_by  TEXT
);

CREATE INDEX idx_mutes_active ON mutes(starts_at, ends_at);

-- notifications records the most recent attempt per
-- (rule_name, fingerprint). The status column captures both the
-- firing/resolved state that was attempted and whether it succeeded:
--   'firing-sent', 'firing-failed', 'resolved-sent', 'resolved-failed'
CREATE TABLE notifications (
    rule_name   TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    sent_at     TIMESTAMPTZ NOT NULL,
    status      TEXT NOT NULL,
    PRIMARY KEY (rule_name, fingerprint)
);
