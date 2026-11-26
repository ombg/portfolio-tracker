// Package accounts implements account management (FR-2, D-2, D-14). Every query runs through
// db.WithUser, so RLS limits it to the signed-in user's rows (D-1).
package accounts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ombg/portfolio-tracker/services/api/internal/db"
	"github.com/ombg/portfolio-tracker/services/api/internal/db/dbq"
)

var (
	ErrNotFound        = errors.New("account not found")
	ErrDuplicateNumber = errors.New("another account at this broker already has this account number")
)

// ValidationError names the field that is wrong; the API returns it as 422.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Message }

func invalid(field, format string, args ...any) error {
	return &ValidationError{Field: field, Message: fmt.Sprintf(format, args...)}
}

var (
	accountTypes = map[string]bool{"TFSA": true, "FHSA": true, "RRSP": true, "RESP": true, "NON_REGISTERED": true, "OTHER": true}
	registered   = map[string]bool{"TFSA": true, "FHSA": true, "RRSP": true, "RESP": true}
	statuses     = map[string]bool{"open": true, "closed": true, "archived": true}
	managements  = map[string]bool{"self_directed": true, "robo": true}
	levels       = map[string]bool{"security": true, "account": true}
)

const (
	maxNameLen  = 200
	maxNotesLen = 10_000
	maxMetaLen  = 64 << 10
)

// Date is a calendar date, JSON "YYYY-MM-DD".
type Date struct{ time.Time }

func (d Date) MarshalJSON() ([]byte, error) { return json.Marshal(d.Format(time.DateOnly)) }

func (d *Date) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return errors.New("must be a date YYYY-MM-DD")
	}
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return errors.New("must be a date YYYY-MM-DD")
	}
	d.Time = t
	return nil
}

type Broker struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

type Account struct {
	ID             uuid.UUID       `json:"id"`
	Name           string          `json:"name"`
	Broker         Broker          `json:"broker"`
	ExternalNumber *string         `json:"external_number"`
	Type           string          `json:"type"`
	IsRegistered   bool            `json:"is_registered"`
	BaseCurrency   string          `json:"base_currency"`
	Status         string          `json:"status"`
	Management     string          `json:"management"`
	AnalysisLevel  string          `json:"analysis_level"`
	OpenedOn       *Date           `json:"opened_on"`
	ClosedOn       *Date           `json:"closed_on"`
	Notes          *string         `json:"notes"`
	MigrationMeta  json.RawMessage `json:"migration_meta"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

// Optional is a PATCH field: Set when present in the request, Null when it was JSON null.
type Optional[T any] struct {
	Set   bool
	Null  bool
	Value T
}

func (o *Optional[T]) UnmarshalJSON(b []byte) error {
	o.Set = true
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		o.Null = true
		return nil
	}
	return json.Unmarshal(b, &o.Value)
}

// CreateInput is the POST body. Broker, name and type are required; the rest have defaults:
// base_currency CAD, management self_directed, analysis_level from management (D-14),
// is_registered from type (required choice only for OTHER, default false).
type CreateInput struct {
	Broker         string          `json:"broker"`
	Name           string          `json:"name"`
	Type           string          `json:"type"`
	IsRegistered   *bool           `json:"is_registered"`
	BaseCurrency   string          `json:"base_currency"`
	ExternalNumber *string         `json:"external_number"`
	Management     string          `json:"management"`
	AnalysisLevel  string          `json:"analysis_level"`
	OpenedOn       *Date           `json:"opened_on"`
	Notes          *string         `json:"notes"`
	MigrationMeta  json.RawMessage `json:"migration_meta"`
}

// PatchInput is the PATCH body. Absent fields stay; null clears a nullable field.
// The broker cannot change: imported transactions depend on it.
type PatchInput struct {
	Name           Optional[string]          `json:"name"`
	Type           Optional[string]          `json:"type"`
	IsRegistered   Optional[bool]            `json:"is_registered"`
	BaseCurrency   Optional[string]          `json:"base_currency"`
	ExternalNumber Optional[string]          `json:"external_number"`
	Status         Optional[string]          `json:"status"`
	Management     Optional[string]          `json:"management"`
	AnalysisLevel  Optional[string]          `json:"analysis_level"`
	OpenedOn       Optional[Date]            `json:"opened_on"`
	ClosedOn       Optional[Date]            `json:"closed_on"`
	Notes          Optional[string]          `json:"notes"`
	MigrationMeta  Optional[json.RawMessage] `json:"migration_meta"`
	Broker         Optional[json.RawMessage] `json:"broker"` // present only to reject it clearly
}

type Store struct{ Pool *pgxpool.Pool }

// toAccount converts a generated row (account embedded with its broker) to the API shape.
func toAccount(r dbq.CoreAccount, brokerCode, brokerName string) Account {
	a := Account{
		ID: r.ID, Name: r.Name, Broker: Broker{Code: brokerCode, Name: brokerName}, ExternalNumber: r.ExternalNumber,
		Type: r.Type, IsRegistered: r.IsRegistered, BaseCurrency: strings.TrimSpace(r.BaseCurrency), Status: r.Status,
		Management: r.Management, AnalysisLevel: r.AnalysisLevel, Notes: r.Notes, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	if r.OpenedOn != nil {
		a.OpenedOn = &Date{*r.OpenedOn}
	}
	if r.ClosedOn != nil {
		a.ClosedOn = &Date{*r.ClosedOn}
	}
	if r.MigrationMeta != nil {
		a.MigrationMeta = r.MigrationMeta
	}
	return a
}

func getAccount(ctx context.Context, q *dbq.Queries, id uuid.UUID) (Account, error) {
	r, err := q.GetAccount(ctx, id)
	if err != nil {
		return Account{}, err
	}
	return toAccount(r.CoreAccount, r.BrokerCode, r.BrokerName), nil
}

// List returns the user's accounts, open first, then closed, then archived (only when asked).
func (s *Store) List(ctx context.Context, userID uuid.UUID, includeArchived bool) ([]Account, error) {
	out := []Account{}
	err := db.WithUser(ctx, s.Pool, userID, func(tx pgx.Tx) error {
		rows, err := dbq.New(tx).ListAccounts(ctx, includeArchived)
		for _, r := range rows {
			out = append(out, toAccount(r.CoreAccount, r.BrokerCode, r.BrokerName))
		}
		return err
	})
	return out, err
}

func (s *Store) Get(ctx context.Context, userID, id uuid.UUID) (Account, error) {
	var a Account
	err := db.WithUser(ctx, s.Pool, userID, func(tx pgx.Tx) error {
		var err error
		a, err = getAccount(ctx, dbq.New(tx), id)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	return a, err
}

// fields is the full, editable state of an account, validated as a whole.
type fields struct {
	name, accountType, currency, status, management, level string
	isRegistered                                           bool
	externalNumber, notes                                  *string
	openedOn, closedOn                                     *time.Time
	meta                                                   []byte
}

func (f *fields) normalizeAndValidate() error {
	f.name = strings.TrimSpace(f.name)
	f.currency = strings.ToUpper(strings.TrimSpace(f.currency))
	switch {
	case f.name == "":
		return invalid("name", "is required")
	case len(f.name) > maxNameLen:
		return invalid("name", "must be at most %d characters", maxNameLen)
	case len(f.currency) != 3:
		return invalid("base_currency", "must be a 3-letter currency code")
	case !accountTypes[f.accountType]:
		return invalid("type", "must be one of TFSA, FHSA, RRSP, RESP, NON_REGISTERED, OTHER")
	case !statuses[f.status]:
		return invalid("status", "must be open, closed or archived")
	case !managements[f.management]:
		return invalid("management", "must be self_directed or robo")
	case !levels[f.level]:
		return invalid("analysis_level", "must be security or account")
	case f.management == "robo" && f.level != "account":
		return invalid("analysis_level", "robo accounts are analysed at account level (D-14)")
	}
	if f.accountType != "OTHER" && f.isRegistered != registered[f.accountType] {
		return invalid("is_registered", "is fixed by the account type %s", f.accountType)
	}
	if f.externalNumber != nil {
		n := strings.TrimSpace(*f.externalNumber)
		if n == "" {
			f.externalNumber = nil
		} else if len(n) > 64 {
			return invalid("external_number", "must be at most 64 characters")
		} else {
			f.externalNumber = &n
		}
	}
	if f.notes != nil && len(*f.notes) > maxNotesLen {
		return invalid("notes", "must be at most %d characters", maxNotesLen)
	}
	if f.status == "open" {
		f.closedOn = nil // reopening clears the closing date
	}
	if f.openedOn != nil && f.closedOn != nil && f.closedOn.Before(*f.openedOn) {
		return invalid("closed_on", "must not be before opened_on")
	}
	if f.meta != nil {
		if len(f.meta) > maxMetaLen {
			return invalid("migration_meta", "must be at most %d bytes", maxMetaLen)
		}
		var obj map[string]any
		if err := json.Unmarshal(f.meta, &obj); err != nil || obj == nil {
			return invalid("migration_meta", "must be a JSON object")
		}
	}
	return nil
}

// derive fills the values the type and management imply, unless the client chose them.
func derive(f *fields, registeredGiven, levelGiven bool) {
	if f.accountType != "OTHER" && !registeredGiven {
		f.isRegistered = registered[f.accountType]
	}
	if !levelGiven {
		if f.management == "robo" {
			f.level = "account"
		} else if f.level == "" {
			f.level = "security"
		}
	}
}

func (s *Store) Create(ctx context.Context, userID uuid.UUID, in CreateInput) (Account, error) {
	f := fields{
		name: in.Name, accountType: in.Type, currency: in.BaseCurrency, status: "open",
		management: in.Management, level: in.AnalysisLevel, externalNumber: in.ExternalNumber,
		notes: in.Notes, meta: nullIfJSONNull(in.MigrationMeta),
	}
	if f.currency == "" {
		f.currency = "CAD"
	}
	if f.management == "" {
		f.management = "self_directed"
	}
	if in.IsRegistered != nil {
		f.isRegistered = *in.IsRegistered
	}
	if in.OpenedOn != nil {
		f.openedOn = &in.OpenedOn.Time
	}
	derive(&f, in.IsRegistered != nil, in.AnalysisLevel != "")
	if strings.TrimSpace(in.Broker) == "" {
		return Account{}, invalid("broker", "is required")
	}
	if err := f.normalizeAndValidate(); err != nil {
		return Account{}, err
	}

	var a Account
	err := db.WithUser(ctx, s.Pool, userID, func(tx pgx.Tx) error {
		q := dbq.New(tx)
		brokerID, err := q.BrokerIDByCode(ctx, strings.ToLower(strings.TrimSpace(in.Broker)))
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return invalid("broker", "unknown broker %q", in.Broker)
			}
			return err
		}
		// One portfolio per user in v1 (D-2); RLS returns only this user's.
		portfolioID, err := q.UserPortfolioID(ctx)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return errors.New("user has no portfolio (create users with ptctl)")
			}
			return err
		}
		id, err := q.InsertAccount(ctx, dbq.InsertAccountParams{
			UserID: userID, PortfolioID: portfolioID, BrokerID: brokerID, ExternalNumber: f.externalNumber,
			Name: f.name, Type: f.accountType, IsRegistered: f.isRegistered, BaseCurrency: f.currency, Status: f.status,
			Management: f.management, AnalysisLevel: f.level, OpenedOn: f.openedOn, ClosedOn: f.closedOn,
			Notes: f.notes, MigrationMeta: f.meta,
		})
		if err != nil {
			return err
		}
		a, err = getAccount(ctx, q, id)
		return err
	})
	return a, mapDBError(err)
}

func (s *Store) Update(ctx context.Context, userID, id uuid.UUID, p PatchInput) (Account, error) {
	if p.Broker.Set {
		return Account{}, invalid("broker", "cannot be changed; create a new account instead")
	}
	var a Account
	err := db.WithUser(ctx, s.Pool, userID, func(tx pgx.Tx) error {
		q := dbq.New(tx)
		row, err := q.GetAccountForUpdate(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		cur := toAccount(row.CoreAccount, row.BrokerCode, row.BrokerName)
		f := fields{
			name: cur.Name, accountType: cur.Type, currency: cur.BaseCurrency, status: cur.Status,
			management: cur.Management, level: cur.AnalysisLevel, isRegistered: cur.IsRegistered,
			externalNumber: cur.ExternalNumber, notes: cur.Notes, meta: cur.MigrationMeta,
		}
		if cur.OpenedOn != nil {
			f.openedOn = &cur.OpenedOn.Time
		}
		if cur.ClosedOn != nil {
			f.closedOn = &cur.ClosedOn.Time
		}
		if err := applyPatch(&f, p); err != nil {
			return err
		}
		// A changed type or management re-derives what it implies unless the patch sets it too.
		if p.Type.Set && !p.IsRegistered.Set {
			derive(&f, false, true)
		}
		if p.Management.Set && !p.AnalysisLevel.Set {
			f.level = ""
			derive(&f, true, false)
		}
		if err := f.normalizeAndValidate(); err != nil {
			return err
		}
		if err := q.UpdateAccount(ctx, dbq.UpdateAccountParams{
			ID: id, Name: f.name, Type: f.accountType, IsRegistered: f.isRegistered, BaseCurrency: f.currency,
			ExternalNumber: f.externalNumber, Status: f.status, Management: f.management, AnalysisLevel: f.level,
			OpenedOn: f.openedOn, ClosedOn: f.closedOn, Notes: f.notes, MigrationMeta: f.meta,
		}); err != nil {
			return err
		}
		a, err = getAccount(ctx, q, id)
		return err
	})
	return a, mapDBError(err)
}

func applyPatch(f *fields, p PatchInput) error {
	required := func(field string, o Optional[string], dst *string) error {
		if !o.Set {
			return nil
		}
		if o.Null {
			return invalid(field, "cannot be null")
		}
		*dst = o.Value
		return nil
	}
	for _, r := range []struct {
		field string
		o     Optional[string]
		dst   *string
	}{
		{"name", p.Name, &f.name}, {"type", p.Type, &f.accountType}, {"base_currency", p.BaseCurrency, &f.currency},
		{"status", p.Status, &f.status}, {"management", p.Management, &f.management}, {"analysis_level", p.AnalysisLevel, &f.level},
	} {
		if err := required(r.field, r.o, r.dst); err != nil {
			return err
		}
	}
	if p.IsRegistered.Set {
		if p.IsRegistered.Null {
			return invalid("is_registered", "cannot be null")
		}
		f.isRegistered = p.IsRegistered.Value
	}
	nullable := func(o Optional[string], dst **string) {
		if o.Set {
			if o.Null {
				*dst = nil
			} else {
				v := o.Value
				*dst = &v
			}
		}
	}
	nullable(p.ExternalNumber, &f.externalNumber)
	nullable(p.Notes, &f.notes)
	date := func(o Optional[Date], dst **time.Time) {
		if o.Set {
			if o.Null {
				*dst = nil
			} else {
				t := o.Value.Time
				*dst = &t
			}
		}
	}
	date(p.OpenedOn, &f.openedOn)
	date(p.ClosedOn, &f.closedOn)
	if p.MigrationMeta.Set {
		f.meta = nil
		if !p.MigrationMeta.Null {
			f.meta = p.MigrationMeta.Value
		}
	}
	return nil
}

func nullIfJSONNull(m json.RawMessage) []byte {
	if len(m) == 0 || bytes.Equal(bytes.TrimSpace(m), []byte("null")) {
		return nil
	}
	return m
}

// mapDBError turns constraint violations the Go checks did not catch into API errors.
func mapDBError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch {
	case pgErr.Code == "23505" && pgErr.ConstraintName == "accounts_user_broker_number_key":
		return ErrDuplicateNumber
	case pgErr.Code == "23503" && strings.Contains(pgErr.ConstraintName, "base_currency"):
		return invalid("base_currency", "unknown currency")
	case pgErr.Code == "23514":
		return invalid(pgErr.ConstraintName, "violates an account rule")
	}
	return err
}

// Brokers and currencies are global reference data for the account form.
func (s *Store) Brokers(ctx context.Context, userID uuid.UUID) ([]Broker, error) {
	var out []Broker
	err := db.WithUser(ctx, s.Pool, userID, func(tx pgx.Tx) error {
		rows, err := dbq.New(tx).ListBrokers(ctx)
		for _, r := range rows {
			out = append(out, Broker{Code: r.Code, Name: r.Name})
		}
		return err
	})
	return out, err
}

type Currency struct {
	Code       string `json:"code"`
	Name       string `json:"name"`
	MinorUnits int16  `json:"minor_units"`
}

func (s *Store) Currencies(ctx context.Context, userID uuid.UUID) ([]Currency, error) {
	var out []Currency
	err := db.WithUser(ctx, s.Pool, userID, func(tx pgx.Tx) error {
		rows, err := dbq.New(tx).ListCurrencies(ctx)
		for _, r := range rows {
			out = append(out, Currency{Code: r.Code, Name: r.Name, MinorUnits: r.MinorUnits})
		}
		return err
	})
	return out, err
}
