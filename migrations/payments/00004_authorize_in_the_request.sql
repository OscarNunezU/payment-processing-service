-- +goose Up
-- Authorization in the request (ADR 0005). A payment starts PROCESSING, with
-- its attempts, an epoch that fences out stale writers, and a lease after
-- which the recovery takes it over. PENDING and expired belonged to the
-- background design and are gone.

-- A PENDING payment was never sent, so nothing reached the acquirer.
UPDATE payments
SET status = 'FAILED', reason = 'acquirer_unavailable', updated_at = now()
WHERE status = 'PENDING';

ALTER TABLE payments
    ADD COLUMN attempts            integer     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    ADD COLUMN epoch               bigint      NOT NULL DEFAULT 0,
    ADD COLUMN lease_until         timestamptz,
    ADD COLUMN reversal_started_at timestamptz,
    DROP CONSTRAINT payments_status_check,
    ADD CONSTRAINT payments_status_check
        CHECK (status IN ('PROCESSING', 'APPROVED', 'DECLINED', 'FAILED')),
    DROP CONSTRAINT payments_reason_check,
    ADD CONSTRAINT payments_reason_check
        CHECK (reason IN ('acquirer_rejected', 'acquirer_unavailable')),
    -- A payment in progress always has a lease, so the recovery can find it.
    ADD CONSTRAINT payments_lease_when_processing
        CHECK ((status = 'PROCESSING') = (lease_until IS NOT NULL));

DROP INDEX payments_one_live_per_order;
CREATE UNIQUE INDEX payments_one_live_per_order ON payments (merchant_id, order_id)
    WHERE status IN ('PROCESSING', 'APPROVED');

-- The recovery's scan: payments in progress whose lease expired.
CREATE INDEX payments_due ON payments (lease_until) WHERE status = 'PROCESSING';

-- Nothing consumes the payment.accepted events of the background design.
DELETE FROM outbox WHERE headers ->> 'event-type' = 'payment.accepted';

-- +goose Down
DROP INDEX payments_due;
DROP INDEX payments_one_live_per_order;
CREATE UNIQUE INDEX payments_one_live_per_order ON payments (merchant_id, order_id)
    WHERE status IN ('PENDING', 'PROCESSING', 'APPROVED');
ALTER TABLE payments
    DROP CONSTRAINT payments_lease_when_processing,
    DROP CONSTRAINT payments_reason_check,
    ADD CONSTRAINT payments_reason_check
        CHECK (reason IN ('acquirer_rejected', 'acquirer_unavailable', 'expired')),
    DROP CONSTRAINT payments_status_check,
    ADD CONSTRAINT payments_status_check
        CHECK (status IN ('PENDING', 'PROCESSING', 'APPROVED', 'DECLINED', 'FAILED')),
    DROP COLUMN reversal_started_at,
    DROP COLUMN lease_until,
    DROP COLUMN epoch,
    DROP COLUMN attempts;
