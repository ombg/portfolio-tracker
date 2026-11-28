-- M4 #27: market data (FR-6, D-3, D-39). Owner: market-data, which connects as pt_market. Prices and rates are
-- global facts about listings, not user data: no user_id and no RLS. Provider data is never edited by users; their
-- overrides live in core (D-39, #29).

-- NOLOGIN here; portfolio-api enables login with MARKET_DB_PASSWORD after migrating (as for pt_importer, D-35).
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'pt_market') THEN
        CREATE ROLE pt_market NOLOGIN NOSUPERUSER NOBYPASSRLS;
    END IF;
END
$$;

-- The schema itself exists since the foundation migration (000001).

-- Latest quotes (kind intraday, kept 7 days) and daily closes (kept forever). Prices are per share as held on
-- trade_date: split adjustment from the provider is undone before they are stored (market-data.md).
CREATE TABLE market.prices (
    security_id uuid NOT NULL REFERENCES core.securities (id),
    kind        text NOT NULL CHECK (kind IN ('intraday', 'close')),
    trade_date  date NOT NULL,
    observed_at timestamptz NOT NULL,
    price       numeric(24, 8) NOT NULL CHECK (price > 0),
    currency    char(3) NOT NULL,
    source      text NOT NULL,
    fetched_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (security_id, kind, observed_at, source)
);
CREATE UNIQUE INDEX prices_one_close_per_day ON market.prices (security_id, trade_date, source) WHERE kind = 'close';
CREATE INDEX prices_latest ON market.prices (security_id, observed_at DESC);

-- FX to the quote currency: Bank of Canada daily rates and Yahoo intraday quotes (D-3, D-6).
CREATE TABLE market.fx_rates (
    base        char(3) NOT NULL,
    quote       char(3) NOT NULL,
    kind        text NOT NULL CHECK (kind IN ('intraday', 'daily')),
    rate_date   date NOT NULL,
    observed_at timestamptz NOT NULL,
    rate        numeric(24, 10) NOT NULL CHECK (rate > 0),
    source      text NOT NULL,
    fetched_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (base, quote, kind, observed_at, source)
);
CREATE UNIQUE INDEX fx_one_daily_rate ON market.fx_rates (base, quote, rate_date, source) WHERE kind = 'daily';

CREATE TABLE market.dividend_events (
    security_id uuid NOT NULL REFERENCES core.securities (id),
    ex_date     date NOT NULL,
    amount      numeric(24, 10) NOT NULL,
    currency    char(3) NOT NULL,
    source      text NOT NULL,
    fetched_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (security_id, ex_date, source)
);

CREATE TABLE market.split_events (
    security_id uuid NOT NULL REFERENCES core.securities (id),
    day         date NOT NULL,
    ratio       numeric(24, 12) NOT NULL CHECK (ratio > 0),  -- new shares per old share
    source      text NOT NULL,
    fetched_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (security_id, day, source)
);

-- One row per run (FR-6 status). errors maps a provider symbol to what went wrong; symbols are public tickers.
CREATE TABLE market.refresh_runs (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind           text NOT NULL CHECK (kind IN ('intraday', 'close', 'backfill')),
    started_at     timestamptz NOT NULL,
    finished_at    timestamptz,
    status         text NOT NULL CHECK (status IN ('running', 'success', 'partial', 'failed')),
    symbols_ok     integer NOT NULL DEFAULT 0,
    symbols_failed integer NOT NULL DEFAULT 0,
    errors         jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(errors) = 'object'),
    next_run_at    timestamptz
);
CREATE INDEX refresh_runs_recent ON market.refresh_runs (started_at DESC);

-- How far each security's daily history has been fetched (backfill from its first trade date, D-39).
CREATE TABLE market.backfills (
    security_id  uuid PRIMARY KEY REFERENCES core.securities (id),
    from_date    date NOT NULL,
    through_date date NOT NULL,
    fetched_at   timestamptz NOT NULL,
    error        text
);

-- The securities to price, without revealing who holds them (D-39): no user ids, no quantities. held is a cheap
-- signal (the sum of quantity changes over every ledger is non-zero, or a ratio split makes the sum meaningless); a
-- false "held" only costs a request. GICs are valued at principal and never requested (D-26). Runs as the owner,
-- which may read every user's rows; pt_market itself has no access to core.
CREATE FUNCTION market.universe()
RETURNS TABLE (security_id uuid, symbol text, exchange text, trading_currency char(3), first_trade_date date,
               held boolean)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, core
AS $$
    WITH moves AS (
        SELECT t.security_id, min(t.trade_date) AS first_trade_date,
               sum(CASE
                       WHEN t.type IN ('BUY', 'DRIP', 'TRANSFER_IN') THEN abs(t.quantity)
                       WHEN t.type IN ('SELL', 'TRANSFER_OUT') THEN -abs(t.quantity)
                       WHEN t.type IN ('SPLIT', 'SYMBOL_CHANGE', 'JOURNAL') THEN t.quantity
                       ELSE 0 END) AS net,
               bool_or(t.split_ratio IS NOT NULL) AS ratio_split
        FROM core.transactions t
        WHERE t.security_id IS NOT NULL
        GROUP BY t.security_id
    )
    SELECT s.id, cur.symbol, cur.exchange, s.trading_currency, m.first_trade_date,
           coalesce(m.net, 0) <> 0 OR m.ratio_split
    FROM moves m
    JOIN core.securities s ON s.id = m.security_id
    LEFT JOIN LATERAL (
        SELECT ss.symbol, ss.exchange FROM core.security_symbols ss
        WHERE ss.security_id = s.id
        ORDER BY ss.valid_to IS NULL DESC, ss.valid_to DESC NULLS FIRST, ss.valid_from DESC NULLS LAST
        LIMIT 1
    ) cur ON true
    WHERE s.security_type IS DISTINCT FROM 'gic'
$$;
REVOKE ALL ON FUNCTION market.universe() FROM PUBLIC;

-- The foreign currencies in any ledger and their first date, for Bank of Canada history (D-6): no users, no amounts.
CREATE FUNCTION market.fx_start()
RETURNS TABLE (currency char(3), first_date date)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, core
AS $$
    SELECT t.currency, min(t.trade_date) FROM core.transactions t WHERE t.currency <> 'CAD' GROUP BY t.currency
$$;
REVOKE ALL ON FUNCTION market.fx_start() FROM PUBLIC;

GRANT USAGE ON SCHEMA market TO pt_market, pt_app;
GRANT EXECUTE ON FUNCTION market.universe(), market.fx_start() TO pt_market;
GRANT SELECT, INSERT, UPDATE ON market.prices, market.fx_rates, market.dividend_events, market.split_events,
    market.refresh_runs, market.backfills TO pt_market;
GRANT DELETE ON market.prices, market.fx_rates TO pt_market;  -- intraday quotes older than 7 days
-- portfolio-api reads prices and rates for valuations and the status card (#28).
GRANT SELECT ON market.prices, market.fx_rates, market.dividend_events, market.split_events, market.refresh_runs,
    market.backfills TO pt_app;
