-- Holdings (FR-7, D-38). Run in db.WithUser: RLS limits ledger and accounts to the signed-in user; the security
-- master is global, so security queries are limited to securities in the user's ledger.

-- name: LedgerForReplay :many
-- Every effective ledger row in replay input order (calculations.md): trade date, then import order.
SELECT l.id, l.account_id, l.type, l.trade_date, l.security_id, l.quantity, l.split_ratio, l.price, l.commission,
       l.gross_amount, l.net_amount, l.currency, l.fx_rate_to_cad, l.group_id, l.counterparty_account_id
FROM core.v_ledger l
ORDER BY l.trade_date, l.imported_at, l.source_row_no NULLS LAST, l.id;

-- name: HeldSecurities :many
-- The securities in the user's ledger.
SELECT s.id, s.name, s.security_type, s.trading_currency
FROM core.securities s
WHERE s.id IN (SELECT DISTINCT l.security_id FROM core.v_ledger l WHERE l.security_id IS NOT NULL);

-- name: HeldListings :many
-- Every symbol period of those securities; the current listing is picked in Go (a GIC has none).
SELECT ss.security_id, ss.symbol, ss.exchange, ss.valid_from, ss.valid_to
FROM core.security_symbols ss
WHERE ss.security_id IN (SELECT DISTINCT l.security_id FROM core.v_ledger l WHERE l.security_id IS NOT NULL)
ORDER BY ss.security_id, ss.valid_from NULLS FIRST;

-- name: SecuritySymbols :many
SELECT ss.symbol, ss.exchange, ss.valid_from, ss.valid_to
FROM core.security_symbols ss
WHERE ss.security_id = @security_id
ORDER BY ss.valid_from NULLS FIRST, ss.symbol;

-- name: SecurityLedger :many
-- One security's ledger rows with their source (NFR-2, rule 5).
SELECT l.id, l.account_id, l.type, l.trade_date, l.quantity, l.split_ratio, l.price, l.commission, l.gross_amount,
       l.net_amount, l.currency, l.fx_rate_to_cad, l.origin, l.corrected::boolean AS corrected, l.source_row_no, l.import_batch_id,
       f.id AS file_id, f.original_name AS file_name
FROM core.v_ledger l
LEFT JOIN imports.source_files f ON f.id = l.source_file_id
WHERE l.security_id = @security_id
ORDER BY l.trade_date, l.imported_at, l.source_row_no NULLS LAST, l.id;
