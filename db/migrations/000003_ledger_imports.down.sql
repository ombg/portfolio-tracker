DROP VIEW IF EXISTS core.v_ledger;
DROP TABLE IF EXISTS core.transaction_corrections;
DROP TABLE IF EXISTS core.transactions;
DROP TABLE IF EXISTS imports.import_rows;
DROP TABLE IF EXISTS imports.import_batches;
DROP TABLE IF EXISTS imports.source_files;
DROP TABLE IF EXISTS core.security_aliases;
DROP TABLE IF EXISTS core.security_symbols;
DROP TABLE IF EXISTS core.securities;

REVOKE ALL ON SCHEMA imports FROM pt_app, pt_importer;
REVOKE ALL ON SCHEMA core FROM pt_importer;
REVOKE ALL ON FUNCTION core.current_user_id() FROM pt_importer;

-- btree_gist and the cluster-wide pt_importer role are deliberately left in place.
