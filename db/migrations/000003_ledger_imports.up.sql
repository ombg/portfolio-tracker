-- M2: security master, immutable transaction ledger with corrections, and import staging.
-- Implements FR-3, NFR-2, NFR-3. Decisions: D-1, D-7, D-8, D-12, D-15, D-26, D-27, D-35. See specs/data-model.md.

-- Needed for the no-overlap rule on symbol validity periods (D-15).
CREATE EXTENSION IF NOT EXISTS btree_gist;

-- The importer parses untrusted files, so it gets its own role instead of the owner's credentials (D-35).
-- NOLOGIN here; portfolio-api enables login with IMPORTER_DB_PASSWORD once the importer service exists.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'pt_importer') THEN
        CREATE ROLE pt_importer NOLOGIN NOSUPERUSER NOBYPASSRLS;
    END IF;
END
$$;

GRANT USAGE ON SCHEMA core TO pt_importer;
GRANT USAGE ON SCHEMA imports TO pt_app, pt_importer;
GRANT EXECUTE ON FUNCTION core.current_user_id() TO pt_importer;

-- ---------------------------------------------------------------------------------------------------------------
-- Security master (global, no user_id): facts about listed securities, shared by all users (D-15, D-35).

CREATE TABLE core.securities (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name             text NOT NULL CHECK (btrim(name) <> ''),
    security_type    text NOT NULL
        CHECK (security_type IN ('stock', 'etf', 'fund', 'reit', 'cash_equivalent', 'gic')),
    trading_currency char(3) NOT NULL REFERENCES core.currencies (code),
    asset_class      text,
    issuer_country   char(2),
    fund_domicile    char(2),
    market_cap       numeric(24, 2),
    market_cap_as_of date,
    identifiers      jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(identifiers) = 'object'),
    -- D-26: one security per GIC; terms parsed from the broker's description.
    gic_issuer       text,
    gic_rate         numeric(9, 6),
    gic_maturity     date,
    created_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT securities_gic_terms CHECK (
        security_type = 'gic' OR (gic_issuer IS NULL AND gic_rate IS NULL AND gic_maturity IS NULL)
    )
);

CREATE TABLE core.security_symbols (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    security_id uuid NOT NULL REFERENCES core.securities (id),
    symbol      text NOT NULL CHECK (symbol = upper(btrim(symbol)) AND symbol <> ''),
    exchange    text NOT NULL CHECK (exchange IN ('TSX', 'TSXV', 'NEO', 'CSE', 'US')),
    valid_from  date,  -- NULL: since before the first known date
    valid_to    date,  -- NULL: still valid; exclusive end
    CHECK (valid_to IS NULL OR valid_from IS NULL OR valid_to > valid_from),
    -- A symbol on an exchange names one security at a time (symbol changes, D-15).
    CONSTRAINT security_symbols_no_overlap EXCLUDE USING gist (
        symbol WITH =, exchange WITH =, daterange(valid_from, valid_to) WITH &&
    )
);

CREATE INDEX security_symbols_security_id_idx ON core.security_symbols (security_id);

-- Broker-specific codes (e.g. Questrade internal codes). user_id NULL = global alias, maintained by the owner;
-- users add their own aliases from import corrections.
CREATE TABLE core.security_aliases (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     uuid REFERENCES core.users (id) ON DELETE RESTRICT,
    broker_id   uuid NOT NULL REFERENCES core.brokers (id),
    alias       text NOT NULL CHECK (alias = upper(btrim(alias)) AND alias <> ''),
    security_id uuid NOT NULL REFERENCES core.securities (id),
    created_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT security_aliases_key UNIQUE NULLS NOT DISTINCT (user_id, broker_id, alias)
);

-- ---------------------------------------------------------------------------------------------------------------
-- Import staging (owner: importer). Originals are stored unchanged on the files volume (D-12).

CREATE TABLE imports.source_files (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id       uuid NOT NULL REFERENCES core.users (id) ON DELETE RESTRICT,
    sha256        text NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    original_name text NOT NULL,
    size          bigint NOT NULL CHECK (size >= 0),
    media_type    text NOT NULL,
    stored_path   text NOT NULL,
    uploaded_at   timestamptz NOT NULL DEFAULT now(),
    deleted_at    timestamptz,
    UNIQUE (id, user_id)
);

-- D-8: the same bytes are imported once per user.
CREATE UNIQUE INDEX source_files_user_sha256_key ON imports.source_files (user_id, sha256) WHERE deleted_at IS NULL;

CREATE TABLE imports.import_batches (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id        uuid NOT NULL REFERENCES core.users (id) ON DELETE RESTRICT,
    source_file_id uuid NOT NULL,
    broker_id      uuid NOT NULL REFERENCES core.brokers (id),
    account_id     uuid,  -- target chosen at upload, if any
    parser         text NOT NULL,
    parser_version text NOT NULL,
    status         text NOT NULL
        CHECK (status IN ('parsed', 'needs_review', 'committed', 'discarded', 'failed')),
    counts         jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(counts) = 'object'),
    created_at     timestamptz NOT NULL DEFAULT now(),
    committed_at   timestamptz,
    FOREIGN KEY (source_file_id, user_id) REFERENCES imports.source_files (id, user_id),
    FOREIGN KEY (account_id, user_id) REFERENCES core.accounts (id, user_id),
    UNIQUE (id, user_id),
    CHECK ((status = 'committed') = (committed_at IS NOT NULL))
);

CREATE INDEX import_batches_user_created_idx ON imports.import_batches (user_id, created_at DESC);

CREATE TABLE imports.import_rows (
    batch_id   uuid NOT NULL,
    row_no     integer NOT NULL CHECK (row_no > 0),
    user_id    uuid NOT NULL REFERENCES core.users (id) ON DELETE RESTRICT,
    raw        jsonb NOT NULL,
    normalized jsonb,
    status     text NOT NULL CHECK (status IN ('ok', 'warning', 'error', 'duplicate', 'skipped', 'excluded')),
    messages   jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(messages) = 'array'),
    user_edits jsonb,
    PRIMARY KEY (batch_id, row_no),
    FOREIGN KEY (batch_id, user_id) REFERENCES imports.import_batches (id, user_id)
);

-- ---------------------------------------------------------------------------------------------------------------
-- Ledger (owner: portfolio-api). Rows are never updated or deleted; corrections are separate rows (D-7).

CREATE TABLE core.transactions (
    id                      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id                 uuid NOT NULL REFERENCES core.users (id) ON DELETE RESTRICT,
    account_id              uuid NOT NULL,
    type                    text NOT NULL CHECK (type IN (
        'BUY', 'SELL', 'DIVIDEND', 'DISTRIBUTION', 'DRIP', 'DEPOSIT', 'WITHDRAWAL', 'TRANSFER_IN', 'TRANSFER_OUT',
        'FEE', 'INTEREST', 'FX_CONVERSION', 'SPLIT', 'SYMBOL_CHANGE', 'JOURNAL', 'MERGER', 'SPIN_OFF',
        'RETURN_OF_CAPITAL', 'WITHHOLDING_TAX', 'GRANT', 'CORRECTION_ADJUSTMENT')),
    trade_date              date NOT NULL,
    settle_date             date,
    record_date             date,
    security_id             uuid REFERENCES core.securities (id),
    quantity                numeric(24, 8),
    split_ratio             numeric(24, 12) CHECK (split_ratio > 0),
    price                   numeric(24, 8),
    commission              numeric(24, 8) NOT NULL DEFAULT 0,
    gross_amount            numeric(24, 8),
    net_amount              numeric(24, 8) NOT NULL,
    currency                char(3) NOT NULL REFERENCES core.currencies (code),
    fx_rate_to_cad          numeric(24, 10) CHECK (fx_rate_to_cad > 0),
    fx_source               text CHECK (fx_source IN ('broker', 'implied', 'boc', 'override')),
    withholding_tax         numeric(24, 8),
    related_id              uuid,
    link_basis              text CHECK (link_basis IN ('single', 'ratio', 'allocated', 'manual')),
    group_id                uuid,
    transfer_status         text CHECK (transfer_status IN ('linked', 'awaiting_counterpart')),
    counterparty_account_id uuid,
    origin                  text NOT NULL CHECK (origin IN ('import', 'manual', 'opening_balance')),
    source_file_id          uuid,
    source_row_no           integer CHECK (source_row_no > 0),
    import_batch_id         uuid,
    natural_key             text NOT NULL CHECK (natural_key ~ '^[0-9a-f]{64}$'),
    raw                     jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(raw) = 'object'),
    imported_at             timestamptz NOT NULL DEFAULT now(),

    FOREIGN KEY (account_id, user_id) REFERENCES core.accounts (id, user_id),
    FOREIGN KEY (counterparty_account_id, user_id) REFERENCES core.accounts (id, user_id),
    FOREIGN KEY (related_id, user_id) REFERENCES core.transactions (id, user_id),
    FOREIGN KEY (source_file_id, user_id) REFERENCES imports.source_files (id, user_id),
    FOREIGN KEY (import_batch_id, user_id) REFERENCES imports.import_batches (id, user_id),
    UNIQUE (id, user_id),

    -- NFR-2: every imported row points at its file, row and batch.
    CONSTRAINT transactions_import_source CHECK (
        origin <> 'import'
        OR (source_file_id IS NOT NULL AND source_row_no IS NOT NULL AND import_batch_id IS NOT NULL)
    ),
    -- Splits carry a ratio (Questrade) or a quantity delta (Wealthsimple), exactly one (data-model.md).
    CONSTRAINT transactions_split_shape CHECK (
        (type = 'SPLIT' AND num_nonnulls(split_ratio, quantity) = 1)
        OR (type <> 'SPLIT' AND split_ratio IS NULL)
    ),
    CONSTRAINT transactions_security_required CHECK (
        security_id IS NOT NULL OR type NOT IN (
            'BUY', 'SELL', 'DIVIDEND', 'DISTRIBUTION', 'DRIP', 'SPLIT', 'SYMBOL_CHANGE', 'JOURNAL', 'MERGER',
            'SPIN_OFF', 'RETURN_OF_CAPITAL')
    ),
    CONSTRAINT transactions_counterparty_not_self CHECK (counterparty_account_id <> account_id)
);

CREATE INDEX transactions_account_date_idx ON core.transactions (account_id, trade_date);
CREATE INDEX transactions_account_natural_key_idx ON core.transactions (account_id, natural_key); -- D-8
CREATE INDEX transactions_security_idx ON core.transactions (security_id) WHERE security_id IS NOT NULL;
CREATE INDEX transactions_group_idx ON core.transactions (group_id) WHERE group_id IS NOT NULL;
CREATE INDEX transactions_batch_idx ON core.transactions (import_batch_id) WHERE import_batch_id IS NOT NULL;

-- D-7: a correction replaces one field's value; field 'voided' removes the row from v_ledger. Values are JSON so a
-- correction can also clear a field (JSON null). Automatic links of late transfer legs are corrections too (D-35).
CREATE TABLE core.transaction_corrections (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id        uuid NOT NULL REFERENCES core.users (id) ON DELETE RESTRICT,
    transaction_id uuid NOT NULL,
    field          text NOT NULL CHECK (field IN (
        'type', 'trade_date', 'settle_date', 'record_date', 'security_id', 'quantity', 'split_ratio', 'price',
        'commission', 'gross_amount', 'net_amount', 'currency', 'fx_rate_to_cad', 'fx_source', 'withholding_tax',
        'related_id', 'link_basis', 'group_id', 'transfer_status', 'counterparty_account_id', 'voided')),
    old_value      jsonb,
    new_value      jsonb NOT NULL,
    reason         text NOT NULL CHECK (btrim(reason) <> ''),
    created_at     timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (transaction_id, user_id) REFERENCES core.transactions (id, user_id),
    CHECK (field <> 'voided' OR jsonb_typeof(new_value) = 'boolean')
);

CREATE INDEX transaction_corrections_tx_idx ON core.transaction_corrections (transaction_id, field, created_at);

-- The effective ledger: latest correction per field applied, voided rows removed (D-7).
-- security_invoker makes the caller's RLS policies apply to the underlying tables.
CREATE VIEW core.v_ledger WITH (security_invoker = true) AS
SELECT
    t.id,
    t.user_id,
    t.account_id,
    CASE WHEN c.v ? 'type' THEN c.v ->> 'type' ELSE t.type END AS type,
    CASE WHEN c.v ? 'trade_date' THEN (c.v ->> 'trade_date')::date ELSE t.trade_date END AS trade_date,
    CASE WHEN c.v ? 'settle_date' THEN (c.v ->> 'settle_date')::date ELSE t.settle_date END AS settle_date,
    CASE WHEN c.v ? 'record_date' THEN (c.v ->> 'record_date')::date ELSE t.record_date END AS record_date,
    CASE WHEN c.v ? 'security_id' THEN (c.v ->> 'security_id')::uuid ELSE t.security_id END AS security_id,
    CASE WHEN c.v ? 'quantity' THEN (c.v ->> 'quantity')::numeric(24, 8) ELSE t.quantity END AS quantity,
    CASE WHEN c.v ? 'split_ratio' THEN (c.v ->> 'split_ratio')::numeric(24, 12) ELSE t.split_ratio END AS split_ratio,
    CASE WHEN c.v ? 'price' THEN (c.v ->> 'price')::numeric(24, 8) ELSE t.price END AS price,
    CASE WHEN c.v ? 'commission' THEN (c.v ->> 'commission')::numeric(24, 8) ELSE t.commission END AS commission,
    CASE WHEN c.v ? 'gross_amount' THEN (c.v ->> 'gross_amount')::numeric(24, 8) ELSE t.gross_amount END AS gross_amount,
    CASE WHEN c.v ? 'net_amount' THEN (c.v ->> 'net_amount')::numeric(24, 8) ELSE t.net_amount END AS net_amount,
    CASE WHEN c.v ? 'currency' THEN (c.v ->> 'currency')::char(3) ELSE t.currency END AS currency,
    CASE WHEN c.v ? 'fx_rate_to_cad' THEN (c.v ->> 'fx_rate_to_cad')::numeric(24, 10) ELSE t.fx_rate_to_cad END AS fx_rate_to_cad,
    CASE WHEN c.v ? 'fx_source' THEN c.v ->> 'fx_source' ELSE t.fx_source END AS fx_source,
    CASE WHEN c.v ? 'withholding_tax' THEN (c.v ->> 'withholding_tax')::numeric(24, 8) ELSE t.withholding_tax END AS withholding_tax,
    CASE WHEN c.v ? 'related_id' THEN (c.v ->> 'related_id')::uuid ELSE t.related_id END AS related_id,
    CASE WHEN c.v ? 'link_basis' THEN c.v ->> 'link_basis' ELSE t.link_basis END AS link_basis,
    CASE WHEN c.v ? 'group_id' THEN (c.v ->> 'group_id')::uuid ELSE t.group_id END AS group_id,
    CASE WHEN c.v ? 'transfer_status' THEN c.v ->> 'transfer_status' ELSE t.transfer_status END AS transfer_status,
    CASE WHEN c.v ? 'counterparty_account_id' THEN (c.v ->> 'counterparty_account_id')::uuid
         ELSE t.counterparty_account_id END AS counterparty_account_id,
    t.origin,
    t.source_file_id,
    t.source_row_no,
    t.import_batch_id,
    t.natural_key,
    t.raw,
    t.imported_at,
    c.v IS NOT NULL AS corrected
FROM core.transactions t
LEFT JOIN LATERAL (
    SELECT jsonb_object_agg(l.field, l.new_value) AS v
    FROM (
        SELECT DISTINCT ON (tc.field) tc.field, tc.new_value
        FROM core.transaction_corrections tc
        WHERE tc.transaction_id = t.id
        ORDER BY tc.field, tc.created_at DESC, tc.id DESC
    ) l
) c ON true
WHERE NOT COALESCE((c.v ->> 'voided')::boolean, false);

-- ---------------------------------------------------------------------------------------------------------------
-- Row-level security (D-1).

ALTER TABLE core.security_aliases ENABLE ROW LEVEL SECURITY;
ALTER TABLE core.security_aliases FORCE ROW LEVEL SECURITY;
CREATE POLICY security_aliases_read ON core.security_aliases FOR SELECT
    USING (user_id IS NULL OR user_id = core.current_user_id());
CREATE POLICY security_aliases_insert ON core.security_aliases FOR INSERT
    WITH CHECK (user_id = core.current_user_id());
CREATE POLICY security_aliases_update ON core.security_aliases FOR UPDATE
    USING (user_id = core.current_user_id())
    WITH CHECK (user_id = core.current_user_id());

DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['core.transactions', 'core.transaction_corrections', 'imports.source_files',
                             'imports.import_batches', 'imports.import_rows']
    LOOP
        EXECUTE format('ALTER TABLE %s ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %s FORCE ROW LEVEL SECURITY', t);
        EXECUTE format('CREATE POLICY owner ON %s USING (user_id = core.current_user_id()) '
                       'WITH CHECK (user_id = core.current_user_id())', t);
    END LOOP;
END
$$;

-- ---------------------------------------------------------------------------------------------------------------
-- Privileges (D-35). Neither role may DELETE anything; nobody but the owner may UPDATE the ledger.

-- portfolio-api requests
GRANT SELECT, INSERT ON core.securities, core.security_symbols TO pt_app;
GRANT UPDATE (valid_to) ON core.security_symbols TO pt_app;   -- closing a symbol on SYMBOL_CHANGE
GRANT SELECT, INSERT, UPDATE ON core.security_aliases TO pt_app;
GRANT SELECT, INSERT ON core.transactions, core.transaction_corrections TO pt_app;
GRANT SELECT ON core.v_ledger TO pt_app;
GRANT SELECT ON imports.source_files, imports.import_batches, imports.import_rows TO pt_app;
GRANT UPDATE (status, committed_at, counts) ON imports.import_batches TO pt_app;  -- commit and discard

-- importer: reads what it needs to resolve rows and pre-check duplicates, writes only its own schema. Duplicates
-- are counted on core.transactions, not v_ledger, so a voided row is not re-imported (D-8).
GRANT SELECT ON core.currencies, core.brokers, core.accounts, core.securities, core.security_symbols,
    core.security_aliases, core.transactions TO pt_importer;
GRANT SELECT, INSERT, UPDATE ON imports.source_files, imports.import_batches, imports.import_rows TO pt_importer;
