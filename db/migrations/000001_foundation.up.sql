-- M1 foundation: identity, portfolios, brokers, currencies and accounts.
-- Implements FR-1, FR-2. Decisions: D-1, D-2, D-14. See specs/data-model.md.

CREATE SCHEMA IF NOT EXISTS core;
CREATE SCHEMA IF NOT EXISTS imports;
CREATE SCHEMA IF NOT EXISTS market;

-- The API connects as the database owner to run migrations, then runs every
-- request transaction under this role with SET LOCAL ROLE, so RLS always applies.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'pt_app') THEN
        CREATE ROLE pt_app NOLOGIN NOSUPERUSER NOBYPASSRLS;
    END IF;
END
$$;

GRANT USAGE ON SCHEMA core TO pt_app;

-- Fails closed: an unset or empty app.user_id yields NULL, which matches no row.
CREATE FUNCTION core.current_user_id() RETURNS uuid
    LANGUAGE sql STABLE PARALLEL SAFE
    AS $$ SELECT NULLIF(current_setting('app.user_id', true), '')::uuid $$;

GRANT EXECUTE ON FUNCTION core.current_user_id() TO pt_app;

CREATE TABLE core.currencies (
    code        char(3) PRIMARY KEY CHECK (code = upper(code)),
    name        text NOT NULL,
    minor_units smallint NOT NULL CHECK (minor_units >= 0)
);

INSERT INTO core.currencies (code, name, minor_units) VALUES
    ('CAD', 'Canadian dollar', 2),
    ('USD', 'US dollar', 2),
    ('EUR', 'Euro', 2);

CREATE TABLE core.brokers (
    id   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    code text NOT NULL UNIQUE,
    name text NOT NULL
);

INSERT INTO core.brokers (code, name) VALUES
    ('questrade', 'Questrade'),
    ('wealthsimple', 'Wealthsimple');

CREATE TABLE core.users (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email              text NOT NULL,
    password_hash      text NOT NULL,
    display_name       text NOT NULL,
    reporting_currency char(3) NOT NULL DEFAULT 'CAD' REFERENCES core.currencies (code),
    created_at         timestamptz NOT NULL DEFAULT now(),
    disabled_at        timestamptz
);

CREATE UNIQUE INDEX users_email_lower_key ON core.users (lower(email));

CREATE TABLE core.sessions (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      uuid NOT NULL REFERENCES core.users (id) ON DELETE CASCADE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    CHECK (expires_at > created_at)
);

CREATE INDEX sessions_user_id_idx ON core.sessions (user_id);
CREATE INDEX sessions_expires_at_idx ON core.sessions (expires_at);

CREATE TABLE core.portfolios (
    id      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES core.users (id) ON DELETE RESTRICT,
    name    text NOT NULL CHECK (btrim(name) <> ''),
    UNIQUE (user_id, name),
    -- Target of the composite foreign key that keeps accounts in their owner's portfolio.
    UNIQUE (id, user_id)
);

CREATE TABLE core.accounts (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         uuid NOT NULL REFERENCES core.users (id) ON DELETE RESTRICT,
    portfolio_id    uuid NOT NULL,
    broker_id       uuid NOT NULL REFERENCES core.brokers (id),
    external_number text,
    name            text NOT NULL CHECK (btrim(name) <> ''),
    type            text NOT NULL
        CHECK (type IN ('TFSA', 'FHSA', 'RRSP', 'RESP', 'NON_REGISTERED', 'OTHER')),
    is_registered   boolean NOT NULL,
    base_currency   char(3) NOT NULL REFERENCES core.currencies (code),
    status          text NOT NULL DEFAULT 'open'
        CHECK (status IN ('open', 'closed', 'archived')),
    management      text NOT NULL DEFAULT 'self_directed'
        CHECK (management IN ('self_directed', 'robo')),
    analysis_level  text NOT NULL DEFAULT 'security'
        CHECK (analysis_level IN ('security', 'account')),
    opened_on       date,
    closed_on       date,
    notes           text,
    migration_meta  jsonb,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),

    FOREIGN KEY (portfolio_id, user_id) REFERENCES core.portfolios (id, user_id),
    UNIQUE (id, user_id),

    -- is_registered is derived from type, except OTHER where the user chooses.
    CONSTRAINT accounts_registered_matches_type CHECK (
        type = 'OTHER'
        OR (type = 'NON_REGISTERED' AND NOT is_registered)
        OR (type IN ('TFSA', 'FHSA', 'RRSP', 'RESP') AND is_registered)
    ),
    -- D-14: robo accounts are analysed at account level only.
    CONSTRAINT accounts_robo_account_level CHECK (
        management <> 'robo' OR analysis_level = 'account'
    ),
    CONSTRAINT accounts_closed_after_opened CHECK (
        closed_on IS NULL OR opened_on IS NULL OR closed_on >= opened_on
    )
);

CREATE INDEX accounts_user_id_idx ON core.accounts (user_id);
CREATE INDEX accounts_portfolio_id_idx ON core.accounts (portfolio_id);

-- An external number identifies one account per broker for each user (D-22, D-27).
CREATE UNIQUE INDEX accounts_user_broker_number_key
    ON core.accounts (user_id, broker_id, external_number)
    WHERE external_number IS NOT NULL;

-- Row-level security (D-1). FORCE also applies the policies to the table owner.
ALTER TABLE core.portfolios ENABLE ROW LEVEL SECURITY;
ALTER TABLE core.portfolios FORCE ROW LEVEL SECURITY;
CREATE POLICY portfolios_owner ON core.portfolios
    USING (user_id = core.current_user_id())
    WITH CHECK (user_id = core.current_user_id());

ALTER TABLE core.accounts ENABLE ROW LEVEL SECURITY;
ALTER TABLE core.accounts FORCE ROW LEVEL SECURITY;
CREATE POLICY accounts_owner ON core.accounts
    USING (user_id = core.current_user_id())
    WITH CHECK (user_id = core.current_user_id());

GRANT SELECT ON core.currencies, core.brokers TO pt_app;
GRANT SELECT, INSERT, UPDATE ON core.portfolios, core.accounts TO pt_app;
