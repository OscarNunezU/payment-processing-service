-- +goose Up
-- Events wait here, written in the same transaction as the change they
-- describe, until the relay publishes them to Kafka (RFC 0001, section 8.3).
CREATE TABLE outbox (
    id         bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    topic      text        NOT NULL,
    key        text        NOT NULL,
    payload    bytea       NOT NULL,
    headers    jsonb       NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE outbox;
