-- +goose Up
-- The trace of the request that created each payment, as a W3C traceparent,
-- so the recovery resumes it and a payment is one trace from its request to
-- its webhook, also when the recovery finishes it (NFR07).
ALTER TABLE payments ADD COLUMN traceparent text;

-- +goose Down
ALTER TABLE payments DROP COLUMN traceparent;
