DROP TABLE IF EXISTS core.accounts;
DROP TABLE IF EXISTS core.portfolios;
DROP TABLE IF EXISTS core.sessions;
DROP TABLE IF EXISTS core.users;
DROP TABLE IF EXISTS core.brokers;
DROP TABLE IF EXISTS core.currencies;
DROP FUNCTION IF EXISTS core.current_user_id();

REVOKE ALL ON SCHEMA core FROM pt_app;

DROP SCHEMA IF EXISTS market;
DROP SCHEMA IF EXISTS imports;
DROP SCHEMA IF EXISTS core;

-- The pt_app role is cluster-wide and is deliberately left in place.
