package db_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ombg/portfolio-tracker/services/api/internal/db"
)

// Synthetic ids for the ledger and import tests (D-20).
var (
	security = uuid.MustParse("30000000-0000-0000-0000-000000000001")
	fileA    = uuid.MustParse("40000000-0000-0000-0000-00000000000a")
	fileB    = uuid.MustParse("40000000-0000-0000-0000-00000000000b")
	batchA   = uuid.MustParse("50000000-0000-0000-0000-00000000000a")
	batchB   = uuid.MustParse("50000000-0000-0000-0000-00000000000b")
	txA      = uuid.MustParse("60000000-0000-0000-0000-00000000000a")
	txB      = uuid.MustParse("60000000-0000-0000-0000-00000000000b")
)

const (
	importerRole = "pt_importer"
	hashA        = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hashB        = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// seedLedger adds, for each user, one stored file, one committed batch with one row and one imported BUY.
func seedLedger(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	seed(t, pool)
	ctx := context.Background()
	b := &pgx.Batch{}
	b.Queue("TRUNCATE core.securities CASCADE")
	b.Queue(`INSERT INTO core.securities (id, name, security_type, trading_currency)
		VALUES ($1, 'Synthetic Equity ETF', 'etf', 'CAD')`, security)
	b.Queue(`INSERT INTO core.security_symbols (security_id, symbol, exchange) VALUES ($1, 'QZGR', 'TSX')`, security)
	for _, u := range []struct {
		user, account, file, batch, tx uuid.UUID
		hash                           string
	}{{userA, accountA, fileA, batchA, txA, hashA}, {userB, accountB, fileB, batchB, txB, hashB}} {
		b.Queue(`INSERT INTO imports.source_files (id, user_id, sha256, original_name, size, media_type, stored_path)
			VALUES ($1, $2, $3, 'activities.csv', 10, 'text/csv', 'files/aa/x')`, u.file, u.user, u.hash)
		b.Queue(`INSERT INTO imports.import_batches (id, user_id, source_file_id, broker_id, parser, parser_version,
			status, committed_at) SELECT $1, $2, $3, id, 'questrade', '1', 'committed', now()
			FROM core.brokers WHERE code = 'questrade'`, u.batch, u.user, u.file)
		b.Queue(`INSERT INTO imports.import_rows (batch_id, row_no, user_id, raw, status)
			VALUES ($1, 1, $2, '{}', 'ok')`, u.batch, u.user)
		b.Queue(`INSERT INTO core.transactions (id, user_id, account_id, type, trade_date, security_id, quantity,
			price, net_amount, currency, origin, source_file_id, source_row_no, import_batch_id, natural_key)
			VALUES ($1, $2, $3, 'BUY', '2025-01-02', $4, 10, 25, -250, 'CAD', 'import', $5, 1, $6, $7)`,
			u.tx, u.user, u.account, security, u.file, u.batch, u.hash)
	}
	if err := pool.SendBatch(ctx, b).Close(); err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
}

// asRole runs fn as role with app.user_id = user, like db.WithUser but for any role. The transaction always rolls
// back, so cases do not affect each other.
func asRole(t *testing.T, pool *pgxpool.Pool, role string, user uuid.UUID, fn func(pgx.Tx) error) error {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+role); err != nil {
		t.Fatal(err)
	}
	if err := db.SetUser(ctx, tx, user); err != nil {
		t.Fatal(err)
	}
	return fn(tx)
}

func execAs(t *testing.T, pool *pgxpool.Pool, role string, user uuid.UUID, sql string, args ...any) (int64, error) {
	t.Helper()
	var n int64
	err := asRole(t, pool, role, user, func(tx pgx.Tx) error {
		tag, err := tx.Exec(context.Background(), sql, args...)
		n = tag.RowsAffected()
		return err
	})
	return n, err
}

const insertTx = `INSERT INTO core.transactions (user_id, account_id, type, trade_date, security_id, quantity,
	split_ratio, net_amount, currency, origin, source_file_id, source_row_no, import_batch_id, natural_key)
	VALUES ($1, $2, $3, '2025-02-03', $4, $5, $6, 0, 'CAD', $7, $8, $9, $10, $11)`

// TestLedgerWrites: SQLSTATE per statement run by user A as pt_app. 42501 = privilege or RLS, 23503 = foreign key,
// 23514 = check. "" = must succeed with wantRows rows.
func TestLedgerWrites(t *testing.T) {
	pool := ownerPool(t)
	seedLedger(t, pool)

	cases := []struct {
		name     string
		sql      string
		args     []any
		wantCode string
		wantRows int64
	}{
		{"insert own manual BUY", insertTx,
			[]any{userA, accountA, "BUY", security, 1, nil, "manual", nil, nil, nil, hashA}, "", 1},
		{"insert own imported BUY", insertTx,
			[]any{userA, accountA, "BUY", security, 1, nil, "import", fileA, 2, batchA, hashA}, "", 1},
		{"D-7: update own transaction", "UPDATE core.transactions SET quantity = 11 WHERE id = $1",
			[]any{txA}, "42501", 0},
		{"D-7: delete own transaction", "DELETE FROM core.transactions WHERE id = $1", []any{txA}, "42501", 0},
		{"insert transaction owned by B", insertTx,
			[]any{userB, accountB, "BUY", security, 1, nil, "manual", nil, nil, nil, hashA}, "42501", 0},
		{"insert own transaction into B's account", insertTx,
			[]any{userA, accountB, "BUY", security, 1, nil, "manual", nil, nil, nil, hashA}, "23503", 0},
		{"own row pointing at B's file and batch", insertTx,
			[]any{userA, accountA, "BUY", security, 1, nil, "import", fileB, 1, batchB, hashA}, "23503", 0},
		{"NFR-2: imported row without source", insertTx,
			[]any{userA, accountA, "BUY", security, 1, nil, "import", nil, nil, nil, hashA}, "23514", 0},
		{"BUY without security", insertTx,
			[]any{userA, accountA, "BUY", nil, 1, nil, "manual", nil, nil, nil, hashA}, "23514", 0},
		{"SPLIT with ratio and delta", insertTx,
			[]any{userA, accountA, "SPLIT", security, 1, 2, "manual", nil, nil, nil, hashA}, "23514", 0},
		{"SPLIT with ratio only", insertTx,
			[]any{userA, accountA, "SPLIT", security, nil, 2, "manual", nil, nil, nil, hashA}, "", 1},
		{"ratio on a BUY", insertTx,
			[]any{userA, accountA, "BUY", security, 1, 2, "manual", nil, nil, nil, hashA}, "23514", 0},
		{"natural key not a sha256", insertTx,
			[]any{userA, accountA, "BUY", security, 1, nil, "manual", nil, nil, nil, "abc"}, "23514", 0},
		{"correct own transaction",
			`INSERT INTO core.transaction_corrections (user_id, transaction_id, field, new_value, reason)
			 VALUES ($1, $2, 'quantity', '12', 'broker statement')`, []any{userA, txA}, "", 1},
		{"correct B's transaction",
			`INSERT INTO core.transaction_corrections (user_id, transaction_id, field, new_value, reason)
			 VALUES ($1, $2, 'quantity', '12', 'x')`, []any{userA, txB}, "23503", 0},
		{"correction without reason",
			`INSERT INTO core.transaction_corrections (user_id, transaction_id, field, new_value, reason)
			 VALUES ($1, $2, 'quantity', '12', ' ')`, []any{userA, txA}, "23514", 0},
		{"update a correction", "UPDATE core.transaction_corrections SET reason = 'x'", nil, "42501", 0},
		{"delete own staged row", "DELETE FROM imports.import_rows WHERE batch_id = $1", []any{batchA}, "42501", 0},
		{"edit own staged row (importer's table)", "UPDATE imports.import_rows SET status = 'ok' WHERE batch_id = $1",
			[]any{batchA}, "42501", 0},
		{"discard own batch", "UPDATE imports.import_batches SET status = 'discarded', committed_at = NULL WHERE id = $1",
			[]any{batchA}, "", 1},
		{"discard B's batch", "UPDATE imports.import_batches SET status = 'discarded', committed_at = NULL WHERE id = $1",
			[]any{batchB}, "", 0},
		{"rename own source file (importer's table)", "UPDATE imports.source_files SET original_name = 'x' WHERE id = $1",
			[]any{fileA}, "42501", 0},
		{"close own symbol period", "UPDATE core.security_symbols SET valid_to = '2025-06-01' WHERE security_id = $1",
			[]any{security}, "", 1},
		{"rename a symbol", "UPDATE core.security_symbols SET symbol = 'QZGX' WHERE security_id = $1",
			[]any{security}, "42501", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n, err := execAs(t, pool, db.RequestRole, userA, tc.sql, tc.args...)
			assertSQLState(t, err, tc.wantCode)
			if tc.wantCode == "" && n != tc.wantRows {
				t.Errorf("rows affected = %d, want %d", n, tc.wantRows)
			}
		})
	}
}

// TestImporterRole: the importer writes only the imports schema, for the user it was given (D-35).
func TestImporterRole(t *testing.T) {
	pool := ownerPool(t)
	seedLedger(t, pool)

	cases := []struct {
		name     string
		sql      string
		args     []any
		wantCode string
		wantRows int64
	}{
		{"store own file",
			`INSERT INTO imports.source_files (user_id, sha256, original_name, size, media_type, stored_path)
			 VALUES ($1, $2, 'b.csv', 1, 'text/csv', 'files/cc/x')`,
			[]any{userA, "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}, "", 1},
		{"D-8: same bytes twice for one user",
			`INSERT INTO imports.source_files (user_id, sha256, original_name, size, media_type, stored_path)
			 VALUES ($1, $2, 'again.csv', 10, 'text/csv', 'files/aa/x')`, []any{userA, hashA}, "23505", 0},
		{"D-8: same bytes as another user's file",
			`INSERT INTO imports.source_files (user_id, sha256, original_name, size, media_type, stored_path)
			 VALUES ($1, $2, 'b.csv', 10, 'text/csv', 'files/bb/x')`, []any{userA, hashB}, "", 1},
		{"store file for B",
			`INSERT INTO imports.source_files (user_id, sha256, original_name, size, media_type, stored_path)
			 VALUES ($1, $2, 'b.csv', 1, 'text/csv', 'files/cc/x')`,
			[]any{userB, "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}, "42501", 0},
		{"stage row in B's batch",
			`INSERT INTO imports.import_rows (batch_id, row_no, user_id, raw, status) VALUES ($1, 2, $2, '{}', 'ok')`,
			[]any{batchB, userA}, "23503", 0},
		{"edit own staged row", "UPDATE imports.import_rows SET status = 'warning' WHERE batch_id = $1",
			[]any{batchA}, "", 1},
		{"edit B's staged row", "UPDATE imports.import_rows SET status = 'warning' WHERE batch_id = $1",
			[]any{batchB}, "", 0},
		{"delete own staged row", "DELETE FROM imports.import_rows WHERE batch_id = $1", []any{batchA}, "42501", 0},
		{"write the ledger", insertTx,
			[]any{userA, accountA, "BUY", security, 1, nil, "manual", nil, nil, nil, hashA}, "42501", 0},
		{"correct the ledger",
			`INSERT INTO core.transaction_corrections (user_id, transaction_id, field, new_value, reason)
			 VALUES ($1, $2, 'voided', 'true', 'x')`, []any{userA, txA}, "42501", 0},
		{"create a security",
			"INSERT INTO core.securities (name, security_type, trading_currency) VALUES ('x', 'stock', 'CAD')",
			nil, "42501", 0},
		{"create an account", insertAccount,
			[]any{userA, portfolioA, "TFSA", true, "self_directed", "security"}, "42501", 0},
		{"save an alias",
			`INSERT INTO core.security_aliases (user_id, broker_id, alias, security_id)
			 SELECT $1, id, 'H000001', $2 FROM core.brokers WHERE code = 'questrade'`, []any{userA, security},
			"42501", 0},
		{"read users", "SELECT 1 FROM core.users", nil, "42501", 0},
		{"read sessions", "SELECT 1 FROM core.sessions", nil, "42501", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n, err := execAs(t, pool, importerRole, userA, tc.sql, tc.args...)
			assertSQLState(t, err, tc.wantCode)
			if tc.wantCode == "" && n != tc.wantRows {
				t.Errorf("rows affected = %d, want %d", n, tc.wantRows)
			}
		})
	}
}

// TestLedgerAndImportsAreIsolated: each role sees only its user's rows in every new user table and in v_ledger.
func TestLedgerAndImportsAreIsolated(t *testing.T) {
	pool := ownerPool(t)
	seedLedger(t, pool)
	ctx := context.Background()

	tables := map[string]string{
		"core.transactions":            "SELECT count(*) FROM core.transactions",
		"core.v_ledger":                "SELECT count(*) FROM core.v_ledger",
		"core.transaction_corrections": "SELECT count(*) FROM core.transaction_corrections",
		"imports.source_files":         "SELECT count(*) FROM imports.source_files",
		"imports.import_batches":       "SELECT count(*) FROM imports.import_batches",
		"imports.import_rows":          "SELECT count(*) FROM imports.import_rows",
	}
	if _, err := pool.Exec(ctx, `INSERT INTO core.transaction_corrections (user_id, transaction_id, field, new_value,
		reason) VALUES ($1, $2, 'quantity', '9', 'x'), ($3, $4, 'quantity', '9', 'x')`, userA, txA, userB, txB); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{db.RequestRole, importerRole} {
		for name, q := range tables {
			if role == importerRole && (name == "core.v_ledger" || name == "core.transaction_corrections") {
				continue // the importer has no use for corrections (D-35)
			}
			for _, user := range []uuid.UUID{userA, userB, uuid.Nil} {
				want := 1
				if user == uuid.Nil {
					want = 0 // app.user_id unset: fails closed
				}
				err := asRole(t, pool, role, user, func(tx pgx.Tx) error {
					if user == uuid.Nil {
						if _, err := tx.Exec(ctx, "SELECT set_config('app.user_id', '', true)"); err != nil {
							return err
						}
					}
					var n int
					if err := tx.QueryRow(ctx, q).Scan(&n); err != nil {
						return err
					}
					if n != want {
						t.Errorf("%s as %s for user %s: %d rows, want %d", name, role, user, n, want)
					}
					return nil
				})
				if err != nil {
					t.Fatalf("%s as %s: %v", name, role, err)
				}
			}
		}
	}
}

// TestLedgerViewAppliesCorrections: the latest correction per field wins, JSON null clears a field, voided rows
// disappear, and the stored row never changes (D-7).
func TestLedgerViewAppliesCorrections(t *testing.T) {
	pool := ownerPool(t)
	seedLedger(t, pool)
	ctx := context.Background()

	correct := func(field, value, at string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `INSERT INTO core.transaction_corrections (user_id, transaction_id, field,
			new_value, reason, created_at) VALUES ($1, $2, $3, $4::jsonb, 'test', $5::timestamptz)`,
			userA, txA, field, value, at); err != nil {
			t.Fatal(err)
		}
	}
	correct("quantity", `"12.5"`, "2025-03-01T00:00:00Z")
	correct("quantity", `"11"`, "2025-03-02T00:00:00Z") // latest wins
	correct("quantity", `"99"`, "2025-02-01T00:00:00Z") // older, ignored
	correct("price", `null`, "2025-03-01T00:00:00Z")

	err := asRole(t, pool, db.RequestRole, userA, func(tx pgx.Tx) error {
		var qty, stored string
		var price *string
		var corrected bool
		if err := tx.QueryRow(ctx, `SELECT quantity::text, price::text, corrected FROM core.v_ledger WHERE id = $1`,
			txA).Scan(&qty, &price, &corrected); err != nil {
			return err
		}
		if qty != "11.00000000" || price != nil || !corrected {
			t.Errorf("v_ledger quantity=%s price=%v corrected=%v, want 11.00000000, NULL, true", qty, price, corrected)
		}
		if err := tx.QueryRow(ctx, "SELECT quantity::text FROM core.transactions WHERE id = $1", txA).Scan(&stored); err != nil {
			return err
		}
		if stored != "10.00000000" {
			t.Errorf("stored quantity = %s, want the original 10.00000000", stored)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	correct("voided", `true`, "2025-03-03T00:00:00Z")
	var n int
	if err := asRole(t, pool, db.RequestRole, userA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT count(*) FROM core.v_ledger").Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("voided row still in v_ledger (%d rows)", n)
	}
	correct("voided", `false`, "2025-03-04T00:00:00Z") // un-void is a later correction
	if err := asRole(t, pool, db.RequestRole, userA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT count(*) FROM core.v_ledger").Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("un-voided row missing from v_ledger (%d rows)", n)
	}
}

// TestSecurityAliases: global aliases are visible to all, a user's own aliases only to that user (D-15, D-35).
func TestSecurityAliases(t *testing.T) {
	pool := ownerPool(t)
	seedLedger(t, pool)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO core.security_aliases (user_id, broker_id, alias, security_id)
		SELECT u, b.id, a, $3 FROM (VALUES (NULL::uuid, 'H000001'), ($1::uuid, 'H000002'), ($2::uuid, 'H000003')) v(u, a)
		CROSS JOIN (SELECT id FROM core.brokers WHERE code = 'questrade') b`, userA, userB, security); err != nil {
		t.Fatal(err)
	}

	for _, role := range []string{db.RequestRole, importerRole} {
		err := asRole(t, pool, role, userA, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, "SELECT alias FROM core.security_aliases ORDER BY alias")
			if err != nil {
				return err
			}
			got, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				return err
			}
			if len(got) != 2 || got[0] != "H000001" || got[1] != "H000002" {
				t.Errorf("%s as A sees aliases %v, want [H000001 H000002]", role, got)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	insertAlias := `INSERT INTO core.security_aliases (user_id, broker_id, alias, security_id)
		SELECT $1, id, $2, $3 FROM core.brokers WHERE code = 'questrade'`
	for _, tc := range []struct {
		name     string
		user     any
		alias    string
		wantCode string
	}{
		{"own alias", userA, "H000004", ""},
		{"global alias", nil, "H000005", "42501"},
		{"alias for B", userB, "H000006", "42501"},
		{"own duplicate", userA, "H000002", "23505"},
		{"own copy of a global alias", userA, "H000001", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := execAs(t, pool, db.RequestRole, userA, insertAlias, tc.user, tc.alias, security)
			assertSQLState(t, err, tc.wantCode)
		})
	}
}

// TestSymbolPeriodsDoNotOverlap: one symbol on one exchange names one security at a time (D-15).
func TestSymbolPeriodsDoNotOverlap(t *testing.T) {
	pool := ownerPool(t)
	seedLedger(t, pool)
	ctx := context.Background()
	other := uuid.MustParse("30000000-0000-0000-0000-000000000002")
	if _, err := pool.Exec(ctx, `INSERT INTO core.securities (id, name, security_type, trading_currency)
		VALUES ($1, 'Synthetic Successor', 'etf', 'CAD')`, other); err != nil {
		t.Fatal(err)
	}
	// QZGR is open-ended from seedLedger: a second open period overlaps.
	_, err := pool.Exec(ctx, `INSERT INTO core.security_symbols (security_id, symbol, exchange, valid_from)
		VALUES ($1, 'QZGR', 'TSX', '2025-06-01')`, other)
	assertSQLState(t, err, "23P01")
	// Same symbol on another exchange is a different listing.
	_, err = pool.Exec(ctx, `INSERT INTO core.security_symbols (security_id, symbol, exchange) VALUES ($1, 'QZGR', 'US')`, other)
	assertSQLState(t, err, "")
	// After closing the first period, the symbol can be reused from that date (valid_to is exclusive).
	if _, err := pool.Exec(ctx, `UPDATE core.security_symbols SET valid_to = '2025-06-01'
		WHERE security_id = $1 AND exchange = 'TSX'`, security); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO core.security_symbols (security_id, symbol, exchange, valid_from)
		VALUES ($1, 'QZGR', 'TSX', '2025-06-01')`, other)
	assertSQLState(t, err, "")
}

// TestNoRoleMayDeleteOrRewriteTheLedger guards NFR-3 and D-7 at the privilege level for every table, so a future
// GRANT cannot quietly break it.
func TestNoRoleMayDeleteOrRewriteTheLedger(t *testing.T) {
	ctx := context.Background()
	pool := ownerPool(t)
	rows, err := pool.Query(ctx, `
		SELECT r.role, n.nspname || '.' || c.relname,
		       has_table_privilege(r.role, c.oid, 'DELETE') OR has_table_privilege(r.role, c.oid, 'TRUNCATE'),
		       c.relname IN ('transactions', 'transaction_corrections') AND n.nspname = 'core'
		         AND has_any_column_privilege(r.role, c.oid, 'UPDATE')
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		CROSS JOIN (VALUES ('pt_app'), ('pt_importer')) r(role)
		WHERE c.relkind IN ('r', 'v') AND n.nspname IN ('core', 'imports', 'market')`)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for rows.Next() {
		var role, table string
		var canDelete, canRewrite bool
		if err := rows.Scan(&role, &table, &canDelete, &canRewrite); err != nil {
			t.Fatal(err)
		}
		checked++
		if canDelete {
			t.Errorf("%s may DELETE or TRUNCATE %s (NFR-3)", role, table)
		}
		if canRewrite {
			t.Errorf("%s may UPDATE %s (D-7)", role, table)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if checked < 2*13 {
		t.Fatalf("checked %d role/table pairs, expected at least 26", checked)
	}
}
