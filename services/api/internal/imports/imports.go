// Package imports reads staged imports for the preview and history and changes what portfolio-api may change:
// a batch's status (discard) and, for an untracked account in a file, the account number of an existing account
// (FR-3, D-22, D-35, D-36). Parsing and row corrections belong to the importer service.
package imports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ombg/portfolio-tracker/services/api/internal/db"
	"github.com/ombg/portfolio-tracker/services/api/internal/db/dbq"
)

// HistoryLimit caps GET /api/imports.
const HistoryLimit = 500

// Error is a refusal with an HTTP status and a stable code for the web client.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

var (
	// Another user's import looks exactly like a missing one (FR-1).
	ErrNotFound    = &Error{404, "not_found", "import not found"}
	ErrRowNotFound = &Error{404, "not_found", "row not found"}
)

func closed(status string) error {
	return &Error{409, "batch_closed", fmt.Sprintf("the import is %s and can no longer change", status)}
}

func isOpen(status string) bool { return status == "parsed" || status == "needs_review" }

type Broker struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

type File struct {
	ID         uuid.UUID `json:"id"`
	Name       string    `json:"name"`
	Size       int64     `json:"size"`
	SHA256     string    `json:"sha256"`
	UploadedAt time.Time `json:"uploaded_at"`
}

// Summary is one line of the import history (FR-3 item 7). Accounts are those the batch's rows belong to.
type Summary struct {
	ID              uuid.UUID       `json:"id"`
	Status          string          `json:"status"`
	Broker          Broker          `json:"broker"`
	TargetAccountID *uuid.UUID      `json:"target_account_id"`
	Accounts        json.RawMessage `json:"accounts"`
	File            File            `json:"file"`
	Parser          string          `json:"parser"`
	ParserVersion   string          `json:"parser_version"`
	Counts          json.RawMessage `json:"counts"`
	CanCommit       bool            `json:"can_commit"`
	CreatedAt       time.Time       `json:"created_at"`
	CommittedAt     *time.Time      `json:"committed_at"`
}

// Row is a staged row as the importer left it: source values, the normalized row, status and messages.
type Row struct {
	RowNo      int32           `json:"row_no"`
	Status     string          `json:"status"`
	Raw        json.RawMessage `json:"raw"`
	Normalized json.RawMessage `json:"normalized"`
	Messages   json.RawMessage `json:"messages"`
	UserEdits  json.RawMessage `json:"user_edits"`
}

// Untracked is an account number the file has rows for but the user does not track, with the importer's proposal
// for creating it (D-22).
type Untracked struct {
	Number   string         `json:"number"`
	Proposal map[string]any `json:"proposal"`
	Rows     int            `json:"rows"`
}

type Detail struct {
	Summary
	Untracked []Untracked `json:"untracked_accounts"`
	Rows      []Row       `json:"rows"`
}

type Store struct{ Pool *pgxpool.Pool }

func (s *Store) List(ctx context.Context, userID uuid.UUID) ([]Summary, error) {
	out := []Summary{}
	err := db.WithUser(ctx, s.Pool, userID, func(tx pgx.Tx) error {
		rows, err := dbq.New(tx).ListImports(ctx, dbq.ListImportsParams{MaxRows: HistoryLimit})
		for _, r := range rows {
			out = append(out, toSummary(r))
		}
		return err
	})
	return out, err
}

func (s *Store) Summary(ctx context.Context, userID, id uuid.UUID) (Summary, error) {
	var out Summary
	err := db.WithUser(ctx, s.Pool, userID, func(tx pgx.Tx) error {
		var err error
		out, err = summary(ctx, dbq.New(tx), id)
		return err
	})
	return out, err
}

func (s *Store) Get(ctx context.Context, userID, id uuid.UUID) (Detail, error) {
	var out Detail
	err := db.WithUser(ctx, s.Pool, userID, func(tx pgx.Tx) error {
		q := dbq.New(tx)
		sum, err := summary(ctx, q, id)
		if err != nil {
			return err
		}
		rows, err := q.ListImportRows(ctx, id)
		if err != nil {
			return err
		}
		out = Detail{Summary: sum, Rows: make([]Row, 0, len(rows)), Untracked: []Untracked{}}
		seen := map[string]int{}
		for _, r := range rows {
			row := Row{RowNo: r.RowNo, Status: r.Status, Raw: r.Raw, Normalized: raw(r.Normalized),
				Messages: r.Messages, UserEdits: raw(r.UserEdits)}
			out.Rows = append(out.Rows, row)
			if number, proposal, ok := untracked(row); ok {
				if i, dup := seen[number]; dup {
					out.Untracked[i].Rows++
				} else {
					seen[number] = len(out.Untracked)
					out.Untracked = append(out.Untracked, Untracked{Number: number, Proposal: proposal, Rows: 1})
				}
			}
		}
		return nil
	})
	return out, err
}

func (s *Store) Row(ctx context.Context, userID, id uuid.UUID, rowNo int32) (Row, error) {
	var out Row
	err := db.WithUser(ctx, s.Pool, userID, func(tx pgx.Tx) error {
		r, err := dbq.New(tx).GetImportRow(ctx, dbq.GetImportRowParams{BatchID: id, RowNo: rowNo})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrRowNotFound
		}
		out = Row{RowNo: r.RowNo, Status: r.Status, Raw: r.Raw, Normalized: raw(r.Normalized), Messages: r.Messages,
			UserEdits: raw(r.UserEdits)}
		return err
	})
	return out, err
}

// CheckOpen fails unless the batch exists for this user and can still be corrected.
func (s *Store) CheckOpen(ctx context.Context, userID, id uuid.UUID) error {
	return db.WithUser(ctx, s.Pool, userID, func(tx pgx.Tx) error {
		_, err := lockOpen(ctx, dbq.New(tx), id)
		return err
	})
}

// Discard closes an open batch for good; its rows and file stay (NFR-3), and the same file can be imported again
// (D-8).
func (s *Store) Discard(ctx context.Context, userID, id uuid.UUID) (Summary, error) {
	var out Summary
	err := db.WithUser(ctx, s.Pool, userID, func(tx pgx.Tx) error {
		q := dbq.New(tx)
		if _, err := lockOpen(ctx, q, id); err != nil {
			return err
		}
		if err := q.DiscardImport(ctx, id); err != nil {
			return err
		}
		var err error
		out, err = summary(ctx, q, id)
		return err
	})
	return out, err
}

// LinkAccount says that a skipped row's untracked account number is the user's existing account: the number is
// stored on the account, so this and every later file finds it. An account that already has another number is
// refused rather than overwritten. The caller then has the importer parse the batch again.
func (s *Store) LinkAccount(ctx context.Context, userID, batchID uuid.UUID, rowNo int32, accountID uuid.UUID) error {
	return db.WithUser(ctx, s.Pool, userID, func(tx pgx.Tx) error {
		q := dbq.New(tx)
		b, err := lockOpen(ctx, q, batchID)
		if err != nil {
			return err
		}
		r, err := q.GetImportRow(ctx, dbq.GetImportRowParams{BatchID: batchID, RowNo: rowNo})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrRowNotFound
		} else if err != nil {
			return err
		}
		number, _, ok := untracked(Row{Status: r.Status, Normalized: raw(r.Normalized)})
		if !ok {
			return &Error{422, "not_applicable", "only rows of an untracked account can be given an account"}
		}
		a, err := q.GetAccountForLink(ctx, accountID)
		if errors.Is(err, pgx.ErrNoRows) {
			return &Error{422, "unknown_account", "account not found"}
		} else if err != nil {
			return err
		}
		switch {
		case a.BrokerID != b.BrokerID:
			return &Error{422, "account_broker_mismatch", "the account belongs to another broker"}
		case a.ExternalNumber != nil && *a.ExternalNumber == number:
			return nil
		case a.ExternalNumber != nil:
			return &Error{409, "account_has_number", "the account already has another account number; change it on the account first"}
		}
		err = q.SetAccountNumber(ctx, dbq.SetAccountNumberParams{ID: accountID, ExternalNumber: &number})
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return &Error{409, "duplicate_account_number", "another account at this broker already has this account number"}
		}
		return err
	})
}

func lockOpen(ctx context.Context, q *dbq.Queries, id uuid.UUID) (dbq.LockImportRow, error) {
	b, err := q.LockImport(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, ErrNotFound
	} else if err != nil {
		return b, err
	}
	if !isOpen(b.Status) {
		return b, closed(b.Status)
	}
	return b, nil
}

func summary(ctx context.Context, q *dbq.Queries, id uuid.UUID) (Summary, error) {
	rows, err := q.ListImports(ctx, dbq.ListImportsParams{ID: &id, MaxRows: 1})
	if err != nil {
		return Summary{}, err
	}
	if len(rows) == 0 {
		return Summary{}, ErrNotFound
	}
	return toSummary(rows[0]), nil
}

func toSummary(r dbq.ListImportsRow) Summary {
	return Summary{
		ID: r.ID, Status: r.Status, Broker: Broker{Code: r.BrokerCode, Name: r.BrokerName},
		TargetAccountID: r.AccountID, Accounts: r.Accounts,
		File:   File{ID: r.FileID, Name: r.OriginalName, Size: r.Size, SHA256: r.Sha256, UploadedAt: r.UploadedAt},
		Parser: r.Parser, ParserVersion: r.ParserVersion, Counts: r.Counts, CanCommit: r.CanCommit,
		CreatedAt: r.CreatedAt, CommittedAt: r.CommittedAt,
	}
}

// untracked reads the importer's {"untracked_account": {...}} on a skipped row.
func untracked(r Row) (string, map[string]any, bool) {
	if r.Status != "skipped" || len(r.Normalized) == 0 {
		return "", nil, false
	}
	var n struct {
		Untracked map[string]any `json:"untracked_account"`
	}
	if json.Unmarshal(r.Normalized, &n) != nil || n.Untracked == nil {
		return "", nil, false
	}
	number, _ := n.Untracked["number"].(string)
	if number == "" {
		return "", nil, false
	}
	proposal := map[string]any{}
	for k, v := range n.Untracked {
		if k != "number" {
			proposal[k] = v
		}
	}
	return number, proposal, true
}

// raw turns a NULL jsonb into JSON null.
func raw(b []byte) json.RawMessage {
	if b == nil {
		return json.RawMessage("null")
	}
	return b
}
