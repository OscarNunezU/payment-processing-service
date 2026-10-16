-- One payment per idempotency key and merchant. Rows created before this
-- migration have no key, and NULLs never collide in a unique index.
ALTER TABLE transactions
    ADD COLUMN idempotency_key     text,
    ADD COLUMN request_fingerprint text;

CREATE UNIQUE INDEX transactions_merchant_idempotency_key_idx
    ON transactions (merchant_id, idempotency_key);
