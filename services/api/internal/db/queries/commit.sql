-- Commit of a staged import into the ledger (FR-3, D-7, D-8, D-27, D-35, D-37). Run in db.WithUser.

-- name: CommitLock :exec
-- One commit at a time: the duplicate counts and the shared security master must not change underneath it.
SELECT pg_advisory_xact_lock(4242001);

-- name: GetBatchForCommit :one
SELECT b.status, b.source_file_id, b.broker_id, b.counts,
       (SELECT count(*) FROM imports.import_rows r WHERE r.batch_id = b.id AND r.status = 'error')::bigint AS errors
FROM imports.import_batches b
WHERE b.id = @id
FOR UPDATE OF b;

-- name: ListRowsToCommit :many
SELECT row_no, raw, normalized
FROM imports.import_rows
WHERE batch_id = @batch_id AND status IN ('ok', 'warning')
ORDER BY row_no;

-- name: StoredByNaturalKey :many
-- D-35: stored rows count, voided or not; v_ledger gives the current links of the ones still visible.
SELECT t.id, t.natural_key, l.group_id, l.withholding_tax
FROM core.transactions t
LEFT JOIN core.v_ledger l ON l.id = t.id
WHERE t.natural_key = ANY(@keys::text[])
ORDER BY t.natural_key, t.imported_at, t.source_row_no NULLS LAST, t.id;

-- name: LedgerWithholding :many
SELECT id, withholding_tax FROM core.v_ledger WHERE id = ANY(@ids::uuid[]);

-- name: AwaitingTransfers :many
SELECT id, account_id, type, trade_date, net_amount, currency, security_id, quantity, counterparty_account_id
FROM core.v_ledger
WHERE transfer_status = 'awaiting_counterpart'
ORDER BY trade_date, imported_at, id;

-- name: SecurityByListing :many
SELECT s.security_id, s.exchange
FROM core.security_symbols s
WHERE s.symbol = @symbol
  AND (sqlc.narg(exchange)::text IS NULL OR s.exchange = sqlc.narg(exchange)::text)
  AND daterange(s.valid_from, s.valid_to) @> @on_date::date
ORDER BY s.exchange, s.security_id;

-- name: ListingPeriods :one
SELECT count(*)::bigint FROM core.security_symbols WHERE symbol = @symbol AND exchange = @exchange;

-- name: AliasSecurity :one
-- The user's own alias wins over a global one.
SELECT security_id FROM core.security_aliases
WHERE broker_id = @broker_id AND alias = @alias
ORDER BY user_id NULLS LAST
LIMIT 1;

-- name: InsertSecurity :one
INSERT INTO core.securities (name, security_type, trading_currency, gic_issuer, gic_rate, gic_maturity)
VALUES (@name, sqlc.narg(security_type), @trading_currency, sqlc.narg(gic_issuer), sqlc.narg(gic_rate),
        sqlc.narg(gic_maturity))
RETURNING id;

-- name: InsertSecuritySymbol :exec
INSERT INTO core.security_symbols (security_id, symbol, exchange, valid_from)
VALUES (@security_id, @symbol, @exchange, sqlc.narg(valid_from));

-- name: CloseSecuritySymbols :exec
-- SYMBOL_CHANGE: the security's open symbol periods end on the change date.
UPDATE core.security_symbols SET valid_to = @on_date::date
WHERE security_id = @security_id AND valid_to IS NULL AND (valid_from IS NULL OR valid_from < @on_date::date);

-- name: UpsertAlias :exec
INSERT INTO core.security_aliases (user_id, broker_id, alias, security_id)
VALUES (@user_id, @broker_id, @alias, @security_id)
ON CONFLICT ON CONSTRAINT security_aliases_key DO UPDATE SET security_id = EXCLUDED.security_id;

-- name: InsertTransaction :exec
INSERT INTO core.transactions (
    id, user_id, account_id, type, trade_date, settle_date, record_date, security_id, quantity, split_ratio, price,
    commission, gross_amount, net_amount, currency, fx_rate_to_cad, fx_source, withholding_tax, related_id,
    link_basis, group_id, transfer_status, counterparty_account_id, origin, source_file_id, source_row_no,
    import_batch_id, natural_key, raw
) VALUES (
    @id, @user_id, @account_id, @type, @trade_date, sqlc.narg(settle_date), sqlc.narg(record_date),
    sqlc.narg(security_id), sqlc.narg(quantity), sqlc.narg(split_ratio), sqlc.narg(price), @commission,
    sqlc.narg(gross_amount), @net_amount, @currency, sqlc.narg(fx_rate_to_cad), sqlc.narg(fx_source),
    sqlc.narg(withholding_tax), sqlc.narg(related_id), sqlc.narg(link_basis), sqlc.narg(group_id),
    sqlc.narg(transfer_status), sqlc.narg(counterparty_account_id), 'import', @source_file_id, @source_row_no,
    @import_batch_id, @natural_key, @raw
);

-- name: InsertCorrection :exec
INSERT INTO core.transaction_corrections (user_id, transaction_id, field, old_value, new_value, reason)
VALUES (@user_id, @transaction_id, @field, @old_value, @new_value, @reason);

-- name: MarkCommitted :exec
UPDATE imports.import_batches SET status = 'committed', committed_at = now(), counts = @counts WHERE id = @id;
