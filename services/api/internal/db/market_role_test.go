package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ombg/portfolio-tracker/services/api/internal/db"
	"github.com/ombg/portfolio-tracker/services/api/internal/testdb"
)

// TestMarketRole: market-data's role logs in only with its password, writes the market schema, learns what to price
// through market.universe() (securities and first trade dates, no holders or quantities) and cannot read user data
// directly (D-39).
func TestMarketRole(t *testing.T) {
	ctx := context.Background()
	pool := ownerPool(t)
	seedLedger(t, pool) // users A and B each bought 10 QZGR on 2025-01-02
	const password = "synthetic market-data password, long enough"
	t.Cleanup(func() { _ = db.SetServiceLogin(ctx, pool, db.MarketRole, "MARKET_DB_PASSWORD", "") })
	if err := db.SetServiceLogin(ctx, pool, db.MarketRole, "MARKET_DB_PASSWORD", "short"); err == nil {
		t.Fatal("short password accepted")
	}
	if err := db.SetServiceLogin(ctx, pool, "portfolio", "X", password); err == nil {
		t.Fatal("an arbitrary role was accepted")
	}
	if err := db.SetServiceLogin(ctx, pool, db.MarketRole, "MARKET_DB_PASSWORD", password); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgx.ParseConfig(testdb.URL())
	if err != nil {
		t.Fatal(err)
	}
	cfg.User, cfg.Password = db.MarketRole, password
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("login as %s: %v", db.MarketRole, err)
	}
	defer func() { _ = conn.Close(ctx) }()

	rows, err := conn.Query(ctx, "SELECT security_id, symbol, exchange, trading_currency, first_trade_date::text AS first_trade_date, held FROM market.universe()")
	if err != nil {
		t.Fatal(err)
	}
	var cols []string
	for _, f := range rows.FieldDescriptions() {
		cols = append(cols, f.Name)
	}
	type row struct {
		symbol, exchange, currency, first string
		held                              bool
	}
	var got []row
	for rows.Next() {
		var r row
		var id any
		if err := rows.Scan(&id, &r.symbol, &r.exchange, &r.currency, &r.first, &r.held); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if rows.Err() != nil {
		t.Fatal(rows.Err())
	}
	want := "security_id,symbol,exchange,trading_currency,first_trade_date,held"
	var fnCols []string
	if err := conn.QueryRow(ctx, `SELECT array_agg(a ORDER BY o) FROM unnest(
		(SELECT proargnames FROM pg_proc WHERE oid = 'market.universe'::regproc)) WITH ORDINALITY u(a, o)`).Scan(&fnCols); err != nil {
		t.Fatal(err)
	}
	if joined := join(fnCols); joined != want {
		t.Fatalf("universe returns %s, want %s (no user ids, no quantities)", joined, want)
	}
	if joined := join(cols); joined != want {
		t.Fatalf("universe columns %s, want %s (no user ids, no quantities)", joined, want)
	}
	if len(got) != 1 || got[0] != (row{"QZGR", "TSX", "CAD", "2025-01-02", true}) {
		t.Fatalf("universe %+v", got)
	}

	if _, err := conn.Exec(ctx, `INSERT INTO market.prices (security_id, kind, trade_date, observed_at, price,
		currency, source) VALUES ($1, 'close', '2025-01-02', '2025-01-02 21:00Z', 25.5, 'CAD', 'yahoo')`, security); err != nil {
		t.Fatalf("write a price: %v", err)
	}
	for _, q := range []string{
		"SELECT count(*) FROM core.transactions",
		"SELECT count(*) FROM core.accounts",
		"SELECT count(*) FROM core.users",
		"SELECT count(*) FROM imports.import_rows",
		"UPDATE core.securities SET name = 'x'",
	} {
		_, err := conn.Exec(ctx, q)
		var pg *pgconn.PgError
		if !errors.As(err, &pg) || pg.Code != "42501" {
			t.Errorf("%s as %s: %v, want permission denied", q, db.MarketRole, err)
		}
	}
}

func join(s []string) string {
	out := ""
	for i, v := range s {
		if i > 0 {
			out += ","
		}
		out += v
	}
	return out
}
