-- +goose Up
-- The webhooks service (RFC 0001, section 9): where each merchant receives
-- its events, and one delivery per event.
CREATE TABLE endpoints (
    merchant_id uuid        PRIMARY KEY,
    url         text        NOT NULL CHECK (url ~ '^https?://'),
    -- The service signs with the secret, so it keeps it readable. In
    -- production it would be encrypted with a key from a KMS.
    secret      text        NOT NULL CHECK (length(secret) >= 24),
    created_at  timestamptz NOT NULL
);

CREATE TABLE deliveries (
    id              uuid        PRIMARY KEY,
    -- One delivery per event: the consumer inserts with ON CONFLICT, so an
    -- event that arrives twice is delivered once.
    event_id        uuid        NOT NULL UNIQUE,
    event_type      text        NOT NULL,
    merchant_id     uuid        NOT NULL REFERENCES endpoints (merchant_id),
    payload         bytea       NOT NULL,
    traceparent     text,
    status          text        NOT NULL CHECK (status IN ('pending', 'delivered', 'failed')),
    attempts        integer     NOT NULL CHECK (attempts >= 0),
    next_attempt_at timestamptz,
    last_status     integer,
    last_error      text,
    created_at      timestamptz NOT NULL,
    updated_at      timestamptz NOT NULL,
    delivered_at    timestamptz,
    -- A pending delivery is always due at some point, and only a pending one.
    CONSTRAINT deliveries_due_when_pending CHECK ((status = 'pending') = (next_attempt_at IS NOT NULL)),
    CONSTRAINT deliveries_delivered_at CHECK ((status = 'delivered') = (delivered_at IS NOT NULL))
);

-- The dispatcher's scan: pending deliveries, by when they are due.
CREATE INDEX deliveries_due ON deliveries (next_attempt_at) WHERE status = 'pending';

-- +goose Down
DROP TABLE deliveries;
DROP TABLE endpoints;
