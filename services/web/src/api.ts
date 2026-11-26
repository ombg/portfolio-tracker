// Thin client for portfolio-api (specs/architecture.md). Same origin: Caddy serves the app and proxies /api.

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    public field?: string,
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
  if (res.status === 204) return undefined as T;
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    throw new ApiError(res.status, data.error ?? "unknown", data.message ?? res.statusText, data.field);
  }
  return data as T;
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
};

export const TYPE_LABELS: Record<AccountType, string> = {
  TFSA: "TFSA",
  FHSA: "FHSA",
  RRSP: "RRSP",
  RESP: "RESP",
  NON_REGISTERED: "Non-registered",
  OTHER: "Other",
};
