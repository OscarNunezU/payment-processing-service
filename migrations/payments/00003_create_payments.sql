-- +goose Up
-- Payments and their history (RFC 0001, section 7). The unique indexes carry
-- INV01 and INV02; the checks keep a bug in the code from storing a payment
-- that cannot exist.
CREATE TABLE payments (
    id                 uuid        PRIMARY KEY,
    merchant_id        uuid        NOT NULL REFERENCES merchants (id),
    order_id           text        NOT NULL CHECK (length(order_id) BETWEEN 1 AND 64),
    idempotency_key    text        NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 255),
    request_hash       bytea       NOT NULL CHECK (length(request_hash) = 32),
    amount             bigint      NOT NULL CHECK (amount > 0),
    currency           text        NOT NULL CHECK (currency IN ('CLP', 'USD')),
    card_token         text        NOT NULL,
    card_brand         text,
    card_last4         text        CHECK (card_last4 ~ '^[0-9]{4}$'),
    status             text        NOT NULL CHECK (status IN ('PENDING', 'PROCESSING', 'APPROVED', 'DECLINED', 'FAILED')),
    reason             text        CHECK (reason IN ('acquirer_rejected', 'acquirer_unavailable', 'expired')),
    authorization_code text,
    decline_code       text,
    created_at         timestamptz NOT NULL,
    updated_at         timestamptz NOT NULL,
    -- A failed payment always says why, and only a failed one has a reason.
    CONSTRAINT payments_reason_when_failed CHECK ((status = 'FAILED') = (reason IS NOT NULL))
);

-- INV01: one key, one payment.
CREATE UNIQUE INDEX payments_one_per_key ON payments (merchant_id, idempotency_key);

-- INV02: an order has at most one live or approved payment.
CREATE UNIQUE INDEX payments_one_live_per_order ON payments (merchant_id, order_id)
    WHERE status IN ('PENDING', 'PROCESSING', 'APPROVED');

-- The list, newest first. The ids are UUIDv7, so they sort by time.
CREATE INDEX payments_list ON payments (merchant_id, id DESC);
CREATE INDEX payments_list_by_status ON payments (merchant_id, status, id DESC);
CREATE INDEX payments_list_by_order ON payments (merchant_id, order_id, id DESC);

-- One row per status change and per call to the acquirer (FR15), written in
-- the same transaction as the change.
CREATE TABLE payment_history (
    id            bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    payment_id    uuid        NOT NULL REFERENCES payments (id),
    kind          text        NOT NULL CHECK (kind IN ('status', 'acquirer_call')),
    status        text,
    attempt       integer,
    outcome       text,
    latency_ms    integer,
    response_code text,
    reason        text,
    created_at    timestamptz NOT NULL
);

CREATE INDEX payment_history_by_payment ON payment_history (payment_id, id);

-- +goose Down
DROP TABLE payment_history;
DROP TABLE payments;
