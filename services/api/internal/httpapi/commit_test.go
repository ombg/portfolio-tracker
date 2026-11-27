package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/ombg/portfolio-tracker/services/api/internal/imports"
)

// Commit (#18): golden fixture testdata/synthetic/duplicates and hand-written linking cases. Synthetic (D-20).

var (
	acct1 = uuid.MustParse("20000000-0000-0000-0000-000000000001")
	acct2 = uuid.MustParse("20000000-0000-0000-0000-000000000002")
	acct3 = uuid.MustParse("20000000-0000-0000-0000-000000000003")
)

func testdataDir() string {
	_, f, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(f), "..", "..", "..", "..", "testdata")
}

type stagedRow struct {
	RowNo      int32           `json:"row_no"`
	Status     string          `json:"status"`
	Raw        json.RawMessage `json:"raw"`
	Normalized json.RawMessage `json:"normalized"`
}

// seedAccount creates an account with a fixed id for user A, as the owner (fixtures name accounts by id).
func (h *importHarness) seedAccount(id uuid.UUID, broker, number string) {
	h.t.Helper()
	if _, err := h.store.Pool.Exec(context.Background(), `INSERT INTO core.accounts (id, user_id, portfolio_id, broker_id,
		external_number, name, type, is_registered, base_currency)
		SELECT $1, $2, p.id, b.id, $3, 'Synthetic ' || $3, 'TFSA', true, 'CAD'
		FROM core.portfolios p, core.brokers b WHERE p.user_id = $2 AND b.code = $4`, id, h.userA, number, broker); err != nil {
		h.t.Fatal(err)
	}
}

// stage stores rows as the importer would, in a batch of their own (its own file).
func (h *importHarness) stage(rows []stagedRow) uuid.UUID {
	h.t.Helper()
	ctx := context.Background()
	pool := h.store.Pool
	var fileID, batchID uuid.UUID
	sha := strings.ReplaceAll(uuid.NewString()+uuid.NewString(), "-", "")
	if err := pool.QueryRow(ctx, `INSERT INTO imports.source_files (user_id, sha256, original_name, size, media_type,
		stored_path) VALUES ($1, $2, 'synthetic.csv', 1, 'text/csv', 'ab/x.csv') RETURNING id`, h.userA, sha).Scan(&fileID); err != nil {
		h.t.Fatal(err)
	}
	counts := map[string]int{}
	status := "parsed"
	for _, r := range rows {
		counts[r.Status]++
		if r.Status == "error" || r.Status == "warning" {
			status = "needs_review"
		}
	}
	cj, _ := json.Marshal(counts)
	if err := pool.QueryRow(ctx, `INSERT INTO imports.import_batches (user_id, source_file_id, broker_id, parser,
		parser_version, status, counts) SELECT $1, $2, id, 'synthetic', '1', $3, $4 FROM core.brokers WHERE code = 'questrade'
		RETURNING id`, h.userA, fileID, status, cj).Scan(&batchID); err != nil {
		h.t.Fatal(err)
	}
	for _, r := range rows {
		var n any
		if len(r.Normalized) > 0 && string(r.Normalized) != "null" {
			n = string(r.Normalized)
		}
		raw := r.Raw
		if len(raw) == 0 {
			raw = json.RawMessage(`{"Row":"synthetic"}`)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO imports.import_rows (batch_id, row_no, user_id, raw, normalized, status)
			VALUES ($1, $2, $3, $4, $5, $6)`, batchID, r.RowNo, h.userA, string(raw), n, r.Status); err != nil {
			h.t.Fatal(err)
		}
	}
	return batchID
}

type commitResponse struct {
	Import imports.Summary
	Result imports.CommitResult
}

func (h *importHarness) commit(id uuid.UUID) commitResponse {
	h.t.Helper()
	rec := h.do(http.MethodPost, "/api/imports/"+id.String()+"/commit", "", h.a)
	wantStatus(h.t, rec, http.StatusOK, "")
	return decodeJSON[commitResponse](h.t, rec)
}

func (h *importHarness) scalar(q string, args ...any) any {
	h.t.Helper()
	var v any
	if err := h.store.Pool.QueryRow(context.Background(), q, args...).Scan(&v); err != nil {
		h.t.Fatalf("%s: %v", q, err)
	}
	return v
}

// ledgerID is the transaction a staged row became.
func (h *importHarness) ledgerID(batch uuid.UUID, row int) uuid.UUID {
	h.t.Helper()
	var id uuid.UUID
	if err := h.store.Pool.QueryRow(context.Background(), `SELECT id FROM core.transactions
		WHERE import_batch_id = $1 AND source_row_no = $2`, batch, row).Scan(&id); err != nil {
		h.t.Fatalf("ledger row of batch row %d: %v", row, err)
	}
	return id
}

// view reads one column of a transaction with corrections applied (v_ledger, as the owner).
func (h *importHarness) view(id uuid.UUID, col string) string {
	h.t.Helper()
	var v *string
	if err := h.store.Pool.QueryRow(context.Background(), "SELECT "+col+"::text FROM core.v_ledger WHERE id = $1", id).Scan(&v); err != nil {
		h.t.Fatalf("v_ledger.%s: %v", col, err)
	}
	if v == nil {
		return "<null>"
	}
	return *v
}

func TestCommitGoldenDuplicates(t *testing.T) {
	h := newImportHarness(t)
	h.seedAccount(acct1, "questrade", "90000001")
	dir := filepath.Join(testdataDir(), "synthetic", "duplicates")
	var want struct {
		Batches []struct {
			Input               string
			Inserted, Duplicate []int32
			LedgerRows          int64 `json:"ledger_rows"`
		}
		Ledger struct {
			RowsPerKey   map[string]int64 `json:"rows_per_natural_key"`
			WithholdingOf struct {
				Input string
				Row   int
			} `json:"withholding_links_to_dividend_of"`
			DividendTax string     `json:"dividend_withholding_tax"`
			FxGrouped   bool       `json:"fx_legs_share_a_group"`
			Securities  [][]string `json:"securities_created"`
		}
	}
	readJSON(t, filepath.Join(dir, "expected", "outcomes.json"), &want)

	batches := map[string]uuid.UUID{}
	for _, b := range want.Batches {
		var in struct{ Rows []stagedRow }
		readJSON(t, filepath.Join(dir, "input", b.Input+".json"), &in)
		id := h.stage(in.Rows)
		batches[b.Input] = id
		res := h.commit(id)
		if res.Result.Inserted != len(b.Inserted) || res.Result.Duplicate != len(b.Duplicate) || res.Import.Status != "committed" {
			t.Errorf("%s: result %+v, status %s", b.Input, res.Result, res.Import.Status)
		}
		d := decodeJSON[imports.Detail](t, h.do(http.MethodGet, "/api/imports/"+id.String(), "", h.a))
		var ins, dup []int32
		for _, r := range d.Rows {
			switch r.Outcome {
			case "inserted":
				ins = append(ins, r.RowNo)
			case "duplicate":
				dup = append(dup, r.RowNo)
			}
		}
		if !equalRows(ins, b.Inserted) || !equalRows(dup, b.Duplicate) {
			t.Errorf("%s: inserted %v duplicate %v, want %v and %v", b.Input, ins, dup, b.Inserted, b.Duplicate)
		}
		if n := h.scalar("SELECT count(*) FROM core.transactions").(int64); n != b.LedgerRows {
			t.Errorf("%s: ledger has %d rows, want %d", b.Input, n, b.LedgerRows)
		}
		// Committing again changes nothing.
		wantStatus(t, h.do(http.MethodPost, "/api/imports/"+id.String()+"/commit", "", h.a), http.StatusConflict, "batch_closed")
	}

	for k, n := range want.Ledger.RowsPerKey {
		if got := h.scalar("SELECT count(*) FROM core.transactions WHERE natural_key = $1", strings.Repeat(k, 64)).(int64); got != n {
			t.Errorf("natural key %s: %d rows, want %d", k, got, n)
		}
	}
	div := h.ledgerID(batches[want.Ledger.WithholdingOf.Input], want.Ledger.WithholdingOf.Row)
	if n := h.scalar("SELECT count(*) FROM core.transactions WHERE type = 'WITHHOLDING_TAX' AND related_id = $1", div).(int64); n != 1 {
		t.Errorf("withholding rows linked to the dividend: %d, want 1", n)
	}
	if got := h.view(div, "withholding_tax"); !decEq(got, want.Ledger.DividendTax) {
		t.Errorf("dividend withholding_tax = %s, want %s", got, want.Ledger.DividendTax)
	}
	groups := h.scalar(`SELECT count(DISTINCT group_id) FILTER (WHERE group_id IS NOT NULL) || '/' || count(*)
		FROM core.transactions WHERE type = 'FX_CONVERSION'`).(string)
	if want.Ledger.FxGrouped && groups != "1/2" {
		t.Errorf("FX legs: %s distinct groups/legs, want 1/2", groups)
	}
	rows, err := h.store.Pool.Query(context.Background(), `SELECT y.symbol, y.exchange, s.security_type IS NULL
		FROM core.securities s JOIN core.security_symbols y ON y.security_id = s.id ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	var got [][]string
	for rows.Next() {
		var sym, ex string
		var untyped bool
		if err := rows.Scan(&sym, &ex, &untyped); err != nil {
			t.Fatal(err)
		}
		if !untyped {
			t.Errorf("%s: security type guessed; it must stay unknown until classified (NFR-4)", sym)
		}
		got = append(got, []string{sym, ex})
	}
	if gj, wj := mustJSON(got), mustJSON(want.Ledger.Securities); gj != wj {
		t.Errorf("securities %s, want %s", gj, wj)
	}
}

// --- linking ---

func norm(fields map[string]any) json.RawMessage {
	n := map[string]any{"account_id": acct1.String(), "trade_date": "2025-04-01", "commission": "0", "currency": "CAD",
		"natural_key": strings.ReplaceAll(uuid.NewString()+uuid.NewString(), "-", "")}
	for k, v := range fields {
		n[k] = v
	}
	b, _ := json.Marshal(n)
	return b
}

func listed(symbol string, exchange any) map[string]any {
	return map[string]any{"symbol": symbol, "exchange": exchange, "name": "SYNTHETIC " + symbol}
}

func row(no int32, status string, n json.RawMessage) stagedRow {
	return stagedRow{RowNo: no, Status: status, Normalized: n}
}

func (h *importHarness) seedSecurity(name string, listings ...[2]string) uuid.UUID {
	h.t.Helper()
	ctx := context.Background()
	var id uuid.UUID
	if err := h.store.Pool.QueryRow(ctx, `INSERT INTO core.securities (name, security_type, trading_currency)
		VALUES ($1, 'stock', 'CAD') RETURNING id`, name).Scan(&id); err != nil {
		h.t.Fatal(err)
	}
	for _, l := range listings {
		if _, err := h.store.Pool.Exec(ctx, `INSERT INTO core.security_symbols (security_id, symbol, exchange) VALUES ($1, $2, $3)`,
			id, l[0], l[1]); err != nil {
			h.t.Fatal(err)
		}
	}
	return id
}

func TestCommitLinks(t *testing.T) {
	h := newImportHarness(t)
	h.seedAccount(acct1, "questrade", "90000001")
	h.seedAccount(acct2, "questrade", "90000002")
	h.seedAccount(acct3, "questrade", "90000003")
	old := h.seedSecurity("SYNTHETIC OLD NAME", [2]string{"OLDL", "TSX"})
	nullq := h.seedSecurity("SYNTHETIC OPEN LISTING", [2]string{"NULLQ", "TSX"})
	a1, a2 := acct1.String(), acct2.String()
	gic := map[string]any{"broker_code": "1SYNB07", "security_type": "gic", "name": "SYNTHETIC BANK GIC 3.5% 2026-01-15",
		"trading_currency": "CAD", "gic": map[string]any{"issuer": "SYNTHETIC BANK", "rate": "3.5", "maturity": "2026-01-15"}}
	divKey := strings.Repeat("d", 64)
	fxKey := strings.Repeat("f", 64)

	first := h.stage([]stagedRow{
		row(2, "ok", norm(map[string]any{"type": "TRANSFER_OUT", "net_amount": "-500", "counterparty_account_id": a2, "transfer_status": "awaiting_counterpart"})),
		row(3, "ok", norm(map[string]any{"account_id": a2, "type": "TRANSFER_IN", "trade_date": "2025-04-02", "net_amount": "500", "counterparty_account_id": a1, "transfer_status": "awaiting_counterpart"})),
		row(4, "ok", norm(map[string]any{"type": "TRANSFER_OUT", "trade_date": "2025-05-01", "net_amount": "-300", "counterparty_account_id": a2, "transfer_status": "awaiting_counterpart"})),
		row(5, "ok", norm(map[string]any{"type": "DIVIDEND", "security": listed("ZZLK", "US"), "net_amount": "100", "gross_amount": "100", "currency": "USD", "natural_key": divKey})),
		row(6, "ok", norm(map[string]any{"type": "DIVIDEND", "security": listed("ZZLM", "US"), "net_amount": "33.33", "gross_amount": "33.33", "currency": "USD"})),
		row(7, "ok", norm(map[string]any{"type": "WITHHOLDING_TAX", "net_amount": "-20", "currency": "USD", "link_basis": "allocated",
			"allocations": []map[string]any{{"row": 5, "amount": "-15"}, {"row": 6, "amount": "-5"}}})),
		row(8, "ok", norm(map[string]any{"type": "SYMBOL_CHANGE", "trade_date": "2025-04-10", "quantity": "-30",
			"security": map[string]any{"broker_code": "H000123", "security_id": old.String()}, "net_amount": "0",
			"group": map[string]any{"kind": "symbol_change", "key": "symbol_change:8"}})),
		row(9, "ok", norm(map[string]any{"type": "SYMBOL_CHANGE", "trade_date": "2025-04-10", "quantity": "30",
			"security": listed("NEWL", "TSX"), "net_amount": "0", "group": map[string]any{"kind": "symbol_change", "key": "symbol_change:8"}})),
		row(10, "ok", norm(map[string]any{"type": "BUY", "security": gic, "quantity": "1000", "price": "1", "net_amount": "-1000"})),
		row(11, "ok", norm(map[string]any{"type": "SELL", "trade_date": "2025-06-01", "security": gic, "quantity": "1000", "price": "1", "net_amount": "1000"})),
		row(12, "warning", norm(map[string]any{"type": "SPLIT", "quantity": "5", "net_amount": "0",
			"security": map[string]any{"broker_code": "H000777", "proposed": map[string]any{"symbol": "ZZLK", "exchange": "US", "from_row": 5}}})),
		row(13, "ok", norm(map[string]any{"type": "BUY", "security": listed("NULLQ", nil), "quantity": "1", "price": "9", "net_amount": "-9"})),
		row(14, "ok", norm(map[string]any{"type": "FX_CONVERSION", "trade_date": "2025-04-20", "net_amount": "-136", "natural_key": fxKey})),
		row(15, "skipped", json.RawMessage(untrackedRow)),
		row(16, "ok", norm(map[string]any{"type": "BUY", "security": listed("ZZLK", "US"), "quantity": "1", "price": "70", "net_amount": "-70", "currency": "USD", "fx_rate": "1.36", "fx_source": "broker"})),
		row(17, "ok", norm(map[string]any{"type": "BUY", "security": listed("ZZLK", "US"), "quantity": "1", "price": "95.2", "net_amount": "-95.2", "fx_rate": "1.36", "fx_source": "broker"})),
		// Decoys for row 2, both on its own date: from another account, and a different amount.
		row(18, "ok", norm(map[string]any{"account_id": a2, "type": "TRANSFER_IN", "net_amount": "500", "counterparty_account_id": acct3.String(), "transfer_status": "awaiting_counterpart"})),
		row(19, "ok", norm(map[string]any{"account_id": a2, "type": "TRANSFER_IN", "net_amount": "499", "counterparty_account_id": a1, "transfer_status": "awaiting_counterpart"})),
		// A tax row listed before its dividend.
		row(20, "ok", norm(map[string]any{"type": "WITHHOLDING_TAX", "trade_date": "2025-07-15", "net_amount": "-3", "currency": "USD", "related_row": 21, "link_basis": "single"})),
		row(21, "ok", norm(map[string]any{"type": "DIVIDEND", "trade_date": "2025-07-15", "security": listed("ZZLK", "US"), "net_amount": "20", "gross_amount": "20", "currency": "USD"})),
	})
	res := h.commit(first)
	if res.Result != (imports.CommitResult{Inserted: 19, Duplicate: 0, LinkedTransfers: 1}) {
		t.Fatalf("result = %+v", res.Result)
	}
	tx := func(row int) uuid.UUID { return h.ledgerID(first, row) }

	t.Run("internal transfer paired within the file", func(t *testing.T) {
		if g := h.view(tx(2), "group_id"); g == "<null>" || g != h.view(tx(3), "group_id") {
			t.Errorf("transfer legs group %s and %s", g, h.view(tx(3), "group_id"))
		}
		for _, r := range []int{2, 3} {
			if s := h.view(tx(r), "transfer_status"); s != "linked" {
				t.Errorf("row %d transfer_status = %s", r, s)
			}
		}
		for _, r := range []int{4, 18, 19} {
			if s := h.view(tx(r), "transfer_status"); s != "awaiting_counterpart" {
				t.Errorf("row %d: unpaired transfer status = %s", r, s)
			}
		}
	})
	t.Run("tax row before its dividend", func(t *testing.T) {
		if r := h.view(tx(20), "related_id"); r != tx(21).String() || !decEq(h.view(tx(21), "withholding_tax"), "-3") {
			t.Errorf("related_id %s, dividend withholding %s", r, h.view(tx(21), "withholding_tax"))
		}
	})
	t.Run("allocated withholding", func(t *testing.T) {
		if r := h.view(tx(7), "related_id"); r != tx(5).String() || h.view(tx(7), "link_basis") != "allocated" {
			t.Errorf("tax related_id %s basis %s, want the larger share's dividend", r, h.view(tx(7), "link_basis"))
		}
		if !decEq(h.view(tx(5), "withholding_tax"), "-15") || !decEq(h.view(tx(6), "withholding_tax"), "-5") {
			t.Errorf("dividend withholding %s and %s, want -15 and -5", h.view(tx(5), "withholding_tax"), h.view(tx(6), "withholding_tax"))
		}
	})
	t.Run("symbol change keeps the security", func(t *testing.T) {
		if s := h.view(tx(9), "security_id"); s != old.String() {
			t.Errorf("new leg security %s, want the old security %s", s, old)
		}
		periods := h.scalar(`SELECT string_agg(symbol || ':' || coalesce(valid_from::text, '-') || ':' || coalesce(valid_to::text, '-'),
			',' ORDER BY symbol) FROM core.security_symbols WHERE security_id = $1`, old).(string)
		if periods != "NEWL:2025-04-10:-,OLDL:-:2025-04-10" {
			t.Errorf("symbol periods %s", periods)
		}
	})
	t.Run("one security per GIC, found again through the alias", func(t *testing.T) {
		s := h.view(tx(10), "security_id")
		if s != h.view(tx(11), "security_id") {
			t.Error("GIC buy and sell point at different securities")
		}
		terms := h.scalar(`SELECT security_type || ' ' || gic_issuer || ' ' || gic_rate::text || ' ' || gic_maturity::text
			FROM core.securities WHERE id = $1`, uuid.MustParse(s)).(string)
		if terms != "gic SYNTHETIC BANK 3.500000 2026-01-15" {
			t.Errorf("GIC terms %q", terms)
		}
		if a := h.scalar(`SELECT security_id::text FROM core.security_aliases WHERE alias = '1SYNB07' AND user_id = $1`, h.userA); a != s {
			t.Errorf("alias -> %v, want %s", a, s)
		}
	})
	t.Run("committing a proposal accepts it and saves the alias", func(t *testing.T) {
		s := h.view(tx(12), "security_id")
		if s != h.view(tx(5), "security_id") {
			t.Errorf("split security %s, want ZZLK's %s", s, h.view(tx(5), "security_id"))
		}
		if a := h.scalar(`SELECT security_id::text FROM core.security_aliases WHERE alias = 'H000777'`); a != s {
			t.Errorf("alias H000777 -> %v", a)
		}
	})
	t.Run("open exchange settled by the only listing", func(t *testing.T) {
		if s := h.view(tx(13), "security_id"); s != nullq.String() {
			t.Errorf("security %s, want %s", s, nullq)
		}
	})
	t.Run("source fields on every row (NFR-2)", func(t *testing.T) {
		if n := h.scalar(`SELECT count(*) FROM core.transactions WHERE import_batch_id = $1 AND origin = 'import'
			AND source_file_id = $2 AND raw ? 'Row'`, first, res.Import.File.ID).(int64); n != 19 {
			t.Errorf("%d rows carry their source, want 19", n)
		}
	})
	t.Run("a broker rate is a rate to CAD only on a USD row", func(t *testing.T) {
		if r, s := h.view(tx(16), "fx_rate_to_cad"), h.view(tx(16), "fx_source"); !decEq(r, "1.36") || s != "broker" {
			t.Errorf("USD row: rate %s source %s", r, s)
		}
		if r := h.view(tx(17), "fx_rate_to_cad"); r != "<null>" {
			t.Errorf("CAD row: rate %s, want none", r)
		}
	})

	// A later file: the transfer's other leg, a duplicate dividend with a new tax row, an FX pair whose first leg is
	// already in the ledger without a group, and the same symbol change in another account, now that the master
	// knows it (the old ticker's period ends on the change date).
	second := h.stage([]stagedRow{
		row(2, "ok", norm(map[string]any{"account_id": a2, "type": "TRANSFER_IN", "trade_date": "2025-05-03", "net_amount": "300", "counterparty_account_id": a1, "transfer_status": "awaiting_counterpart"})),
		row(3, "ok", norm(map[string]any{"type": "DIVIDEND", "security": listed("ZZLK", "US"), "net_amount": "100", "gross_amount": "100", "currency": "USD", "natural_key": divKey})),
		row(4, "ok", norm(map[string]any{"type": "WITHHOLDING_TAX", "net_amount": "-2", "currency": "USD", "related_row": 3, "link_basis": "single"})),
		row(5, "ok", norm(map[string]any{"type": "FX_CONVERSION", "trade_date": "2025-04-20", "net_amount": "-136", "natural_key": fxKey, "group": map[string]any{"kind": "fx", "key": "fx:5"}})),
		row(6, "ok", norm(map[string]any{"type": "FX_CONVERSION", "trade_date": "2025-04-20", "net_amount": "100", "currency": "USD", "group": map[string]any{"kind": "fx", "key": "fx:5"}})),
		row(7, "ok", norm(map[string]any{"account_id": a2, "type": "SYMBOL_CHANGE", "trade_date": "2025-04-10", "quantity": "-10",
			"security": listed("OLDL", "TSX"), "net_amount": "0", "group": map[string]any{"kind": "symbol_change", "key": "symbol_change:7"}})),
		row(8, "ok", norm(map[string]any{"account_id": a2, "type": "SYMBOL_CHANGE", "trade_date": "2025-04-10", "quantity": "10",
			"security": listed("NEWL", "TSX"), "net_amount": "0", "group": map[string]any{"kind": "symbol_change", "key": "symbol_change:7"}})),
	})
	res = h.commit(second)
	if res.Result != (imports.CommitResult{Inserted: 5, Duplicate: 2, LinkedTransfers: 1}) {
		t.Fatalf("second result = %+v", res.Result)
	}
	t.Run("a known symbol change: the old ticker is looked up before the change date", func(t *testing.T) {
		for _, r := range []int{7, 8} {
			if s := h.view(h.ledgerID(second, r), "security_id"); s != old.String() {
				t.Errorf("row %d security %s, want %s", r, s, old)
			}
		}
	})
	t.Run("late transfer leg links the ledger row by correction", func(t *testing.T) {
		in := h.ledgerID(second, 2)
		if g := h.view(tx(4), "group_id"); g == "<null>" || g != h.view(in, "group_id") {
			t.Errorf("groups %s and %s", g, h.view(in, "group_id"))
		}
		if s := h.view(tx(4), "transfer_status"); s != "linked" {
			t.Errorf("earlier leg status %s", s)
		}
		// The stored row itself is untouched (D-7); the link is a correction with its reason.
		if s := h.scalar("SELECT transfer_status FROM core.transactions WHERE id = $1", tx(4)); s != "awaiting_counterpart" {
			t.Errorf("stored row changed: %v", s)
		}
		if n := h.scalar(`SELECT count(*) FROM core.transaction_corrections WHERE transaction_id = $1
			AND reason LIKE 'linked automatically%'`, tx(4)).(int64); n != 2 {
			t.Errorf("%d corrections, want group_id and transfer_status", n)
		}
	})
	t.Run("tax on a dividend already in the ledger", func(t *testing.T) {
		tax := h.ledgerID(second, 4)
		if r := h.view(tax, "related_id"); r != tx(5).String() {
			t.Errorf("related_id %s, want the stored dividend %s", r, tx(5))
		}
		if w := h.view(tx(5), "withholding_tax"); !decEq(w, "-17") {
			t.Errorf("dividend withholding %s, want -15 - 2", w)
		}
	})
	t.Run("FX leg already in the ledger joins the new group", func(t *testing.T) {
		usd := h.ledgerID(second, 6)
		if g := h.view(tx(14), "group_id"); g == "<null>" || g != h.view(usd, "group_id") {
			t.Errorf("groups %s and %s", g, h.view(usd, "group_id"))
		}
	})
}

func TestCommitRefusals(t *testing.T) {
	h := newImportHarness(t)
	h.seedAccount(acct1, "questrade", "90000001")
	h.seedSecurity("SYNTHETIC TWO LISTINGS", [2]string{"TWOQ", "TSX"})
	h.seedSecurity("SYNTHETIC TWO LISTINGS US", [2]string{"TWOQ", "US"})
	h.seedSecurity("SYNTHETIC ENDED", [2]string{"ENDQ", "TSX"})
	if _, err := h.store.Pool.Exec(context.Background(), `UPDATE core.security_symbols SET valid_to = '2025-01-01' WHERE symbol = 'ENDQ'`); err != nil {
		t.Fatal(err)
	}
	counts := func() string {
		return h.scalar(`SELECT (SELECT count(*) FROM core.transactions) || '/' || (SELECT count(*) FROM core.securities) || '/' ||
			(SELECT count(*) FROM core.security_aliases)`).(string)
	}
	before := counts()

	unresolvedBatch := h.stage([]stagedRow{
		row(2, "ok", norm(map[string]any{"type": "BUY", "security": listed("QZOK", "TSX"), "quantity": "1", "price": "1", "net_amount": "-1"})),
		row(3, "ok", norm(map[string]any{"type": "BUY", "security": listed("UNKQ", nil), "quantity": "1", "price": "1", "net_amount": "-1"})),
		row(4, "ok", norm(map[string]any{"type": "BUY", "security": listed("TWOQ", nil), "quantity": "1", "price": "1", "net_amount": "-1"})),
		row(5, "ok", norm(map[string]any{"type": "BUY", "security": listed("ENDQ", "TSX"), "quantity": "1", "price": "1", "net_amount": "-1"})),
		row(6, "warning", norm(map[string]any{"type": "BUY", "security": map[string]any{"broker_code": "H000999"}, "quantity": "1", "price": "1", "net_amount": "-1"})),
	})
	rec := h.do(http.MethodPost, "/api/imports/"+unresolvedBatch.String()+"/commit", "", h.a)
	wantStatus(t, rec, http.StatusConflict, "")
	body := decodeJSON[struct {
		Error string
		Rows  []struct{ Row int32 }
	}](t, rec)
	var rows []int32
	for _, r := range body.Rows {
		rows = append(rows, r.Row)
	}
	if body.Error != "unresolved_securities" || !equalRows(rows, []int32{3, 4, 5, 6}) {
		t.Errorf("error %s rows %v, want unresolved_securities [3 4 5 6]", body.Error, rows)
	}
	// All or nothing: QZOK was not created either, and the batch stays open.
	if after := counts(); after != before {
		t.Errorf("transactions/securities/aliases %s -> %s after a refused commit", before, after)
	}
	if s := decodeJSON[imports.Summary](t, h.do(http.MethodGet, "/api/imports/"+unresolvedBatch.String(), "", h.a)); s.Status != "parsed" && s.Status != "needs_review" {
		t.Errorf("batch status %s after a refused commit", s.Status)
	}

	errorBatch := h.stage([]stagedRow{row(2, "ok", norm(map[string]any{"type": "DEPOSIT", "net_amount": "1"})), row(3, "error", nil)})
	wantStatus(t, h.do(http.MethodPost, "/api/imports/"+errorBatch.String()+"/commit", "", h.a), http.StatusConflict, "not_committable")
	empty := h.stage([]stagedRow{row(2, "skipped", json.RawMessage(untrackedRow))})
	wantStatus(t, h.do(http.MethodPost, "/api/imports/"+empty.String()+"/commit", "", h.a), http.StatusConflict, "not_committable")

	ok := h.stage([]stagedRow{row(2, "ok", norm(map[string]any{"type": "DEPOSIT", "net_amount": "1"}))})
	b := h.secondUser()
	wantStatus(t, h.do(http.MethodPost, "/api/imports/"+ok.String()+"/commit", "", b), http.StatusNotFound, "not_found")
	wantStatus(t, h.do(http.MethodPost, "/api/imports/"+uuid.NewString()+"/commit", "", h.a), http.StatusNotFound, "not_found")
	if after := counts(); after != before {
		t.Errorf("refusals changed the ledger: %s -> %s", before, after)
	}
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func equalRows(a, b []int32) bool {
	a, b = append([]int32(nil), a...), append([]int32(nil), b...)
	sort.Slice(a, func(i, j int) bool { return a[i] < a[j] })
	sort.Slice(b, func(i, j int) bool { return b[i] < b[j] })
	return mustJSON(a) == mustJSON(b)
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func decEq(got, want string) bool {
	g, err1 := decimal.NewFromString(got)
	w, err2 := decimal.NewFromString(want)
	return err1 == nil && err2 == nil && g.Equal(w)
}
