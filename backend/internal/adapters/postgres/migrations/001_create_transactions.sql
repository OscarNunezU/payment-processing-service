CREATE TABLE transactions (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id  text        NOT NULL,
    amount       bigint      NOT NULL CHECK (amount > 0),
    currency     char(3)     NOT NULL,
    status       text        NOT NULL CHECK (status IN ('PENDING', 'PROCESSING', 'APPROVED', 'DECLINED', 'FAILED')),
    card_last4   char(4)     NOT NULL,
    card_data    bytea,
    reason       text,
    attempts     int         NOT NULL DEFAULT 0,
    available_at timestamptz NOT NULL DEFAULT now(),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

-- GET /payments?merchant_id=&status=, newest first.
CREATE INDEX transactions_merchant_status_created_idx
    ON transactions (merchant_id, status, created_at DESC);

-- The worker only looks at payments that are still in flight.
CREATE INDEX transactions_queue_idx
    ON transactions (available_at)
    WHERE status IN ('PENDING', 'PROCESSING');
