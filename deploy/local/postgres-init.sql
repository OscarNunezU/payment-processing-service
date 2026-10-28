-- Local development only: one database and one role per service, and no role
-- can connect to another service's database (RFC 0001, section 13). Real
-- credentials come from a secret manager, never from this file.
CREATE ROLE payments LOGIN PASSWORD 'payments';
CREATE DATABASE payments OWNER payments;

CREATE ROLE vault LOGIN PASSWORD 'vault';
CREATE DATABASE vault OWNER vault;

CREATE ROLE money LOGIN PASSWORD 'money';
CREATE DATABASE money OWNER money;

CREATE ROLE webhooks LOGIN PASSWORD 'webhooks';
CREATE DATABASE webhooks OWNER webhooks;

REVOKE CONNECT ON DATABASE payments, vault, money, webhooks FROM PUBLIC;
