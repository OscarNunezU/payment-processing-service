-- Local development only: one database and one role per service, and no role
-- can connect to another service's database (RFC 0001, section 14). Real
-- credentials come from a secret manager, never from this file.
CREATE ROLE payments LOGIN PASSWORD 'payments';
CREATE DATABASE payments OWNER payments;

CREATE ROLE webhooks LOGIN PASSWORD 'webhooks';
CREATE DATABASE webhooks OWNER webhooks;

REVOKE CONNECT ON DATABASE payments, webhooks FROM PUBLIC;
