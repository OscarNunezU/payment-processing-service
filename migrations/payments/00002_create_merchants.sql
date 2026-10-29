-- +goose Up
-- Merchants and their API keys (RFC 0001, section 7). A key is stored as the
-- SHA-256 of its secret, never as the secret itself.
CREATE TABLE merchants (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    legal_name text        NOT NULL CHECK (legal_name <> ''),
    -- Canonical form, as the domain writes it: "76543210-3".
    rut        text        NOT NULL UNIQUE CHECK (rut ~ '^[1-9][0-9]{0,7}-[0-9K]$'),
    status     text        NOT NULL CHECK (status IN ('ACTIVE', 'SUSPENDED')),
    created_at timestamptz NOT NULL DEFAULT now()
);

-- The largest payment a merchant may take in each currency. A currency
-- without a row is not accepted for that merchant.
CREATE TABLE merchant_limits (
    merchant_id uuid   NOT NULL REFERENCES merchants (id),
    currency    text   NOT NULL CHECK (currency IN ('CLP', 'USD')),
    max_amount  bigint NOT NULL CHECK (max_amount > 0),
    PRIMARY KEY (merchant_id, currency)
);

CREATE TABLE api_keys (
    hash        bytea       PRIMARY KEY CHECK (length(hash) = 32),
    -- The first characters of the key, to recognize it without its secret.
    hint        text        NOT NULL,
    merchant_id uuid        NOT NULL REFERENCES merchants (id),
    created_at  timestamptz NOT NULL DEFAULT now(),
    -- A key keeps working until this moment, which allows a rotation where
    -- the old and the new key overlap.
    revoked_at  timestamptz
);

CREATE INDEX api_keys_merchant ON api_keys (merchant_id);

-- +goose Down
DROP TABLE api_keys;
DROP TABLE merchant_limits;
DROP TABLE merchants;
