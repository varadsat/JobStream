CREATE TABLE outbox (
    id            BIGSERIAL    PRIMARY KEY,
    aggregate_id  UUID         NOT NULL,
    topic         TEXT         NOT NULL,
    payload       BYTEA        NOT NULL,
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    published_at  TIMESTAMPTZ
);

-- Partial index for the relay's hot path: it only ever scans pending rows.
-- Excluding already-published rows keeps the index small even after years of traffic.
CREATE INDEX idx_outbox_unpublished ON outbox(id) WHERE published_at IS NULL;
