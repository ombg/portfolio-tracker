-- The schema stays: the foundation migration (000001) owns it.
DROP FUNCTION IF EXISTS market.universe(), market.fx_start();
DROP TABLE IF EXISTS market.backfills, market.refresh_runs, market.split_events, market.dividend_events,
    market.fx_rates, market.prices;
REVOKE ALL ON SCHEMA market FROM pt_market, pt_app;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'pt_market') THEN
        DROP ROLE pt_market;
    END IF;
END
$$;
