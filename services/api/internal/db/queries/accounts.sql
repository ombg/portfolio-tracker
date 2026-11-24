-- Accounts (FR-2). Run in db.WithUser: RLS limits every statement to the signed-in user.

-- name: ListAccounts :many
SELECT sqlc.embed(a), b.code AS broker_code, b.name AS broker_name
FROM core.accounts a JOIN core.brokers b ON b.id = a.broker_id
WHERE @include_archived::boolean OR a.status <> 'archived'
ORDER BY array_position(ARRAY['open', 'closed', 'archived'], a.status), lower(a.name), a.id;

-- name: GetAccount :one
SELECT sqlc.embed(a), b.code AS broker_code, b.name AS broker_name
FROM core.accounts a JOIN core.brokers b ON b.id = a.broker_id
WHERE a.id = @id;

-- name: GetAccountForUpdate :one
SELECT sqlc.embed(a), b.code AS broker_code, b.name AS broker_name
FROM core.accounts a JOIN core.brokers b ON b.id = a.broker_id
WHERE a.id = @id
FOR UPDATE OF a;

-- name: InsertAccount :one
INSERT INTO core.accounts (
    user_id, portfolio_id, broker_id, external_number, name, type, is_registered, base_currency, status,
    management, analysis_level, opened_on, closed_on, notes, migration_meta
) VALUES (
    @user_id, @portfolio_id, @broker_id, @external_number, @name, @type, @is_registered, @base_currency, @status,
    @management, @analysis_level, @opened_on, @closed_on, @notes, @migration_meta
)
RETURNING id;

-- name: UpdateAccount :exec
UPDATE core.accounts SET
    name = @name, type = @type, is_registered = @is_registered, base_currency = @base_currency,
    external_number = @external_number, status = @status, management = @management,
    analysis_level = @analysis_level, opened_on = @opened_on, closed_on = @closed_on, notes = @notes,
    migration_meta = @migration_meta, updated_at = now()
WHERE id = @id;

-- name: BrokerIDByCode :one
SELECT id FROM core.brokers WHERE code = @code;

-- One portfolio per user in v1 (D-2).
-- name: UserPortfolioID :one
SELECT id FROM core.portfolios ORDER BY name LIMIT 1;

-- name: ListBrokers :many
SELECT code, name FROM core.brokers ORDER BY name;

-- name: ListCurrencies :many
SELECT code::text AS code, name, minor_units FROM core.currencies ORDER BY code;
