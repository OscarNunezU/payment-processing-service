-- +goose Up
-- The recovery (RFC 0001, section 5.5). The history records every reversal
-- sent to the acquirer, as it records every authorization.
ALTER TABLE payment_history
    DROP CONSTRAINT payment_history_kind_check,
    ADD CONSTRAINT payment_history_kind_check CHECK (kind IN ('status', 'acquirer_call', 'reversal'));

-- Once a reversal started, the payment can only end FAILED: an approval
-- recorded after it would claim an authorization that was released.
ALTER TABLE payments
    ADD CONSTRAINT payments_reversal_ends_failed
        CHECK (reversal_started_at IS NULL OR status IN ('PROCESSING', 'FAILED'));

-- +goose Down
ALTER TABLE payments DROP CONSTRAINT payments_reversal_ends_failed;
DELETE FROM payment_history WHERE kind = 'reversal';
ALTER TABLE payment_history
    DROP CONSTRAINT payment_history_kind_check,
    ADD CONSTRAINT payment_history_kind_check CHECK (kind IN ('status', 'acquirer_call'));
