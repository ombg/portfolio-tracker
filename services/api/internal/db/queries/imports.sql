-- Imports (FR-3, D-36). Run in db.WithUser: RLS limits every statement to the signed-in user. The importer owns
-- these tables; portfolio-api reads them and changes only a batch's status (D-35).

-- name: ListImports :many
-- accounts: those with rows that may reach the ledger (not skipped, not excluded by the user).
SELECT b.id, b.status, b.parser, b.parser_version, b.counts, b.created_at, b.committed_at, b.account_id,
       br.code AS broker_code, br.name AS broker_name,
       f.id AS file_id, f.original_name, f.size, f.sha256, f.uploaded_at,
       COALESCE((
           SELECT jsonb_agg(jsonb_build_object('id', a.id, 'name', a.name) ORDER BY lower(a.name), a.id)
           FROM core.accounts a
           WHERE a.id IN (SELECT (r.normalized ->> 'account_id')::uuid FROM imports.import_rows r
                          WHERE r.batch_id = b.id AND r.status NOT IN ('skipped', 'excluded')
                            AND r.normalized ? 'account_id')
       ), '[]')::jsonb AS accounts,
       (b.status IN ('parsed', 'needs_review')
        AND NOT EXISTS (SELECT 1 FROM imports.import_rows r WHERE r.batch_id = b.id AND r.status = 'error')
        AND EXISTS (SELECT 1 FROM imports.import_rows r WHERE r.batch_id = b.id AND r.status IN ('ok', 'warning'))
       )::boolean AS can_commit
FROM imports.import_batches b
JOIN core.brokers br ON br.id = b.broker_id
JOIN imports.source_files f ON f.id = b.source_file_id
WHERE sqlc.narg(id)::uuid IS NULL OR b.id = sqlc.narg(id)::uuid
ORDER BY b.created_at DESC, b.id
LIMIT @max_rows;

-- name: ListImportRows :many
SELECT row_no, status, raw, normalized, messages, user_edits
FROM imports.import_rows
WHERE batch_id = @batch_id
ORDER BY row_no;

-- name: GetImportRow :one
SELECT row_no, status, raw, normalized, messages, user_edits
FROM imports.import_rows
WHERE batch_id = @batch_id AND row_no = @row_no;

-- name: LockImport :one
SELECT status, broker_id FROM imports.import_batches WHERE id = @id FOR UPDATE;

-- name: DiscardImport :exec
UPDATE imports.import_batches SET status = 'discarded' WHERE id = @id;

-- name: GetAccountForLink :one
SELECT broker_id, external_number FROM core.accounts WHERE id = @id FOR UPDATE;

-- name: SetAccountNumber :exec
UPDATE core.accounts SET external_number = @external_number, updated_at = now()
WHERE id = @id AND external_number IS NULL;
