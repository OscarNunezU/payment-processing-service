-- The code the acquirer gives when it approves; needed later to reconcile or refund.
ALTER TABLE transactions ADD COLUMN authorization_code text;
