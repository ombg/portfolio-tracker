DROP INDEX IF EXISTS core.transactions_import_row_key;
-- Fails while unclassified securities exist; classify them first.
ALTER TABLE core.securities ALTER COLUMN security_type SET NOT NULL;
