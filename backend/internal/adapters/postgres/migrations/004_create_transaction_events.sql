-- The history of each payment: one row per change, written in the same
-- statement as the change itself. Payments created before this migration have
-- no history.
CREATE TABLE transaction_events (
    id             bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    transaction_id uuid        NOT NULL REFERENCES transactions (id),
    status         text        NOT NULL CHECK (status IN ('PENDING', 'PROCESSING', 'APPROVED', 'DECLINED', 'FAILED')),
    attempt        int         NOT NULL,
    reason         text,
    created_at     timestamptz NOT NULL DEFAULT now()
);

-- GET /payments/{id}/events, oldest first.
CREATE INDEX transaction_events_transaction_idx
    ON transaction_events (transaction_id, id);
