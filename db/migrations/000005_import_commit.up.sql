-- M2 #18: commit of staged imports into the ledger (D-37).

-- Securities created at commit (D-35) are known by symbol and exchange only; their type comes from the provider
-- later (M4). NULL means "not classified yet" and is reported as such, never guessed (NFR-4).
ALTER TABLE core.securities ALTER COLUMN security_type DROP NOT NULL;

-- A staged row becomes at most one ledger row (NFR-2 traceability, D-8); also serves the preview's row outcome.
CREATE UNIQUE INDEX transactions_import_row_key ON core.transactions (import_batch_id, source_row_no)
    WHERE import_batch_id IS NOT NULL;
