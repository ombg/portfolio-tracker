// Thin client for portfolio-api (specs/architecture.md). Same origin: Caddy serves the app and proxies /api.

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    public field?: string,
    // The whole error body, for errors that carry more than a message (duplicate_file, unresolved_securities).
    public data: Record<string, unknown> = {},
  ) {
    super(message);
  }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    credentials: "same-origin",
    // Writes must be JSON (D-33); GETs carry no body.
    headers: method === "GET" ? {} : { "Content-Type": "application/json" },
    body: body === undefined ? (method === "GET" ? undefined : "{}") : JSON.stringify(body),
  });
  return decode<T>(res);
}

async function decode<T>(res: Response): Promise<T> {
  if (res.status === 204) return undefined as T;
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    throw new ApiError(res.status, data.error ?? "unknown", data.message ?? res.statusText, data.field, data);
  }
  return data as T;
}

// Uploads are multipart; the header is the CSRF guard for this one non-JSON write (D-36). The browser sets the
// multipart Content-Type with its boundary.
async function upload(file: File, broker: string, accountId: string | null): Promise<UploadResult> {
  const form = new FormData();
  form.append("broker", broker);
  if (accountId) form.append("account_id", accountId);
  form.append("file", file);
  const res = await fetch("/api/imports", {
    method: "POST",
    credentials: "same-origin",
    headers: { "X-Requested-With": "portfolio-tracker" },
    body: form,
  });
  return decode<UploadResult>(res);
}

export interface Profile {
  id: string;
  email: string;
  display_name: string;
  reporting_currency: string;
}

export interface Broker {
  code: string;
  name: string;
}

export interface Currency {
  code: string;
  name: string;
  minor_units: number;
}

export const ACCOUNT_TYPES = ["TFSA", "FHSA", "RRSP", "RESP", "NON_REGISTERED", "OTHER"] as const;
export type AccountType = (typeof ACCOUNT_TYPES)[number];
export type AccountStatus = "open" | "closed" | "archived";
export type Management = "self_directed" | "robo";

export interface Account {
  id: string;
  name: string;
  broker: Broker;
  external_number: string | null;
  type: AccountType;
  is_registered: boolean;
  base_currency: string;
  status: AccountStatus;
  management: Management;
  analysis_level: "security" | "account";
  opened_on: string | null;
  closed_on: string | null;
  notes: string | null;
  created_at: string;
  updated_at: string;
}

export type AccountInput = Partial<{
  broker: string;
  name: string;
  type: AccountType;
  is_registered: boolean;
  base_currency: string;
  external_number: string | null;
  management: Management;
  status: AccountStatus;
  opened_on: string | null;
  closed_on: string | null;
  notes: string | null;
}>;

export type ImportStatus = "parsed" | "needs_review" | "committed" | "discarded" | "failed";
export type RowStatus = "ok" | "warning" | "error" | "duplicate" | "skipped" | "excluded";
export const ROW_STATUSES: RowStatus[] = ["error", "warning", "ok", "duplicate", "skipped", "excluded"];

// Row counts per status; a failed batch carries `failure` instead; committed batches add inserted/duplicate.
export type ImportCounts = Partial<Record<RowStatus | "inserted", number>> & { failure?: string };

export interface ImportSummary {
  id: string;
  status: ImportStatus;
  broker: Broker;
  target_account_id: string | null;
  accounts: { id: string; name: string }[];
  file: { id: string; name: string; size: number; sha256: string; uploaded_at: string };
  parser: string;
  parser_version: string;
  counts: ImportCounts;
  can_commit: boolean;
  created_at: string;
  committed_at: string | null;
}

export interface RowMessage {
  level: "info" | "warning" | "error";
  code: string;
  text: string;
}

// The parser's proposal for one ledger row (specs/data-model.md, "Normalized import row"). Decimals are strings.
export interface NormalizedRow {
  account_id?: string;
  type?: string;
  trade_date?: string;
  security?: {
    symbol?: string | null;
    exchange?: string | null;
    broker_code?: string | null;
    security_id?: string | null;
    proposed?: { symbol: string; exchange: string | null; from_row: number } | null;
    name?: string | null;
    security_type?: string | null;
  } | null;
  quantity?: string | null;
  price?: string | null;
  net_amount?: string;
  currency?: string;
  untracked_account?: UntrackedProposal;
}

export interface UntrackedProposal {
  number: string;
  account_type?: string;
  proposed_type?: AccountType;
  proposed_management?: Management;
  proposed_base_currency?: string;
}

export interface ImportRow {
  row_no: number;
  status: RowStatus;
  raw: Record<string, unknown>;
  normalized: NormalizedRow | null;
  messages: RowMessage[] | null;
  user_edits: { excluded?: boolean } | null;
  transaction_id?: string;
  outcome?: "inserted" | "duplicate";
}

export interface ImportDetail extends ImportSummary {
  rows: ImportRow[];
  untracked_accounts: { number: string; proposal: UntrackedProposal; rows: number }[];
}

export interface UploadResult {
  batch_id: string;
  source_file_id: string;
  status: ImportStatus;
  counts: ImportCounts;
}

export interface CommitResult {
  inserted: number;
  duplicate: number;
  linked_transfers: number;
}

export type RowEdit = Partial<{
  type: string;
  security: { symbol: string; exchange: string };
  excluded: boolean;
  account_id: string;
}>;

// Same lists as the importer (services/importer/importer/parsers/base.py).
export const LEDGER_TYPES = [
  "BUY", "SELL", "DIVIDEND", "DISTRIBUTION", "DRIP", "DEPOSIT", "WITHDRAWAL", "TRANSFER_IN", "TRANSFER_OUT", "FEE",
  "INTEREST", "FX_CONVERSION", "SPLIT", "SYMBOL_CHANGE", "JOURNAL", "MERGER", "SPIN_OFF", "RETURN_OF_CAPITAL",
  "WITHHOLDING_TAX", "GRANT", "CORRECTION_ADJUSTMENT",
] as const;
export const EXCHANGES = ["TSX", "TSXV", "NEO", "CSE", "US"] as const;

// Holdings (FR-7, D-38). Decimals are strings; null is unknown, never 0 (NFR-4).
export interface HeldSecurity {
  id: string;
  symbol: string | null; // null for a GIC
  exchange: string | null;
  name: string;
  type: string | null; // null: not classified yet
  trading_currency: string;
}

export interface Money {
  currency: string;
  amount: string;
}

export interface PositionFlag {
  code: string;
  date: string;
  transaction_id: string;
}

export interface Position {
  security: HeldSecurity;
  quantity: string;
  book_currency: string;
  avg_cost: string | null;
  book_value: string | null;
  avg_cost_cad: string | null;
  book_value_cad: string | null;
  realized_gain: string | null;
  realized_gain_cad: string | null;
  income: Money[];
  income_cad: string | null;
  market_value: string | null;
  market_value_reason: string;
  flags: PositionFlag[];
  open: boolean;
}

export interface HoldingsAccount {
  id: string;
  name: string;
  type: AccountType;
  broker: Broker;
  is_registered: boolean;
  management: Management;
  status: AccountStatus;
}

export interface AccountHoldings {
  account: HoldingsAccount;
  positions: Position[];
  cash: Money[];
}

export interface CombinedPosition {
  security: HeldSecurity;
  quantity: string;
  book_currency: string | null;
  avg_cost: string | null;
  book_value: string | null;
  avg_cost_cad: string | null;
  book_value_cad: string | null;
  realized_gain_cad: string | null;
  income_cad: string | null;
  accounts: number;
  flags: string[];
  open: boolean;
}

export interface ExcludedAccount {
  account_id: string;
  name: string;
  reason: "managed_account";
}

export interface Total {
  amount: string | null;
  excludes: number;
  reason?: string;
}

export interface HoldingsReport {
  scope: string;
  as_of: string | null;
  accounts: AccountHoldings[];
  securities: CombinedPosition[];
  excluded: ExcludedAccount[];
  totals: {
    book_value_cad: Total;
    realized_gain_cad: Total;
    income_cad: Total;
    market_value: Total;
    cash: Money[];
  };
}

export interface LedgerRow {
  id: string;
  account: { id: string; name: string };
  type: string;
  trade_date: string;
  quantity: string | null;
  split_ratio: string | null;
  price: string | null;
  commission: string;
  gross_amount: string | null;
  net_amount: string;
  currency: string;
  fx_rate_to_cad: string | null;
  origin: "import" | "manual" | "opening_balance";
  corrected: boolean;
  source: { file_id: string | null; file_name: string | null; row_no: number | null; import_id: string | null };
}

export interface SecurityDetail {
  security: HeldSecurity;
  symbols: { symbol: string; exchange: string; valid_from: string | null; valid_to: string | null }[];
  positions: { account: HoldingsAccount; position: Position }[];
  combined: CombinedPosition | null;
  excluded: ExcludedAccount[];
  transactions: LedgerRow[];
}

export const api = {
  me: () => request<Profile>("GET", "/api/me"),
  login: (email: string, password: string) => request<void>("POST", "/api/auth/login", { email, password }),
  logout: () => request<void>("POST", "/api/auth/logout"),
  brokers: () => request<{ brokers: Broker[] }>("GET", "/api/brokers").then((r) => r.brokers),
  currencies: () => request<{ currencies: Currency[] }>("GET", "/api/currencies").then((r) => r.currencies),
  accounts: (includeArchived: boolean) =>
    request<{ accounts: Account[] }>("GET", `/api/accounts${includeArchived ? "?include_archived=true" : ""}`).then(
      (r) => r.accounts,
    ),
  createAccount: (input: AccountInput) => request<Account>("POST", "/api/accounts", input),
  updateAccount: (id: string, input: AccountInput) => request<Account>("PATCH", `/api/accounts/${id}`, input),
  imports: () => request<{ imports: ImportSummary[] }>("GET", "/api/imports").then((r) => r.imports),
  importDetail: (id: string) => request<ImportDetail>("GET", `/api/imports/${id}`),
  upload,
  editRow: (id: string, row: number, edit: RowEdit) =>
    request<{ import: ImportSummary; row: ImportRow }>("PATCH", `/api/imports/${id}/rows/${row}`, edit),
  reparse: (id: string) => request<ImportSummary>("POST", `/api/imports/${id}/reparse`),
  discard: (id: string) => request<ImportSummary>("POST", `/api/imports/${id}/discard`),
  commit: (id: string) =>
    request<{ import: ImportSummary; result: CommitResult }>("POST", `/api/imports/${id}/commit`),
  holdings: (scope: string) => request<HoldingsReport>("GET", `/api/holdings?scope=${encodeURIComponent(scope)}`),
  security: (id: string) => request<SecurityDetail>("GET", `/api/securities/${id}`),
};

export const TYPE_LABELS: Record<AccountType, string> = {
  TFSA: "TFSA",
  FHSA: "FHSA",
  RRSP: "RRSP",
  RESP: "RESP",
  NON_REGISTERED: "Non-registered",
  OTHER: "Other",
};
