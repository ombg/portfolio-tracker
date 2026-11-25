import { useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ACCOUNT_TYPES,
  ApiError,
  TYPE_LABELS,
  api,
  type Account,
  type AccountInput,
  type AccountStatus,
  type AccountType,
  type Management,
} from "./api";

const FIELD_LABELS: Record<string, string> = {
  broker: "Broker",
  name: "Name",
  type: "Account type",
  is_registered: "Registered",
  base_currency: "Base currency",
  external_number: "Account number",
  management: "Management",
  analysis_level: "Management",
  opened_on: "Opened on",
  closed_on: "Closed on",
  status: "Status",
  notes: "Notes",
};

// One form for create and edit (FR-2). The server derives is_registered from the type except for OTHER and the
// analysis level from management (D-14); the form only offers the choices the server accepts.
export function AccountForm({ account, onDone }: { account?: Account; onDone: () => void }) {
  const qc = useQueryClient();
  const brokers = useQuery({ queryKey: ["brokers"], queryFn: api.brokers });
  const currencies = useQuery({ queryKey: ["currencies"], queryFn: api.currencies });

  const [broker, setBroker] = useState(account?.broker.code ?? "");
  const [name, setName] = useState(account?.name ?? "");
  const [type, setType] = useState<AccountType>(account?.type ?? "TFSA");
  const [isRegistered, setIsRegistered] = useState(account?.is_registered ?? false);
  const [currency, setCurrency] = useState(account?.base_currency ?? "CAD");
  const [number, setNumber] = useState(account?.external_number ?? "");
  const [management, setManagement] = useState<Management>(account?.management ?? "self_directed");
  const [status, setStatus] = useState<AccountStatus>(account?.status ?? "open");
  const [openedOn, setOpenedOn] = useState(account?.opened_on ?? "");
  const [closedOn, setClosedOn] = useState(account?.closed_on ?? "");
  const [notes, setNotes] = useState(account?.notes ?? "");

  const save = useMutation({
    mutationFn: () => {
      const input: AccountInput = {
        name,
        type,
        base_currency: currency,
        external_number: number.trim() || null,
        management,
        opened_on: openedOn || null,
        notes: notes.trim() || null,
        ...(type === "OTHER" ? { is_registered: isRegistered } : {}),
      };
      if (!account) return api.createAccount({ ...input, broker });
      return api.updateAccount(account.id, { ...input, status, closed_on: status === "open" ? null : closedOn || null });
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["accounts"] });
      onDone();
    },
  });

  function submit(e: FormEvent) {
    e.preventDefault();
    save.mutate();
  }

  const err = save.error;
  const errorText =
    err instanceof ApiError && err.field
      ? `${FIELD_LABELS[err.field] ?? err.field}: ${err.message}`
      : err?.message;

  return (
    <form className="card" onSubmit={submit} aria-label={account ? `Edit ${account.name}` : "New account"}>
      <h2>{account ? `Edit ${account.name}` : "New account"}</h2>
      <div className="grid">
        <label>
          Broker
          {account ? (
            <input value={account.broker.name} disabled title="The broker cannot change after creation" />
          ) : (
            <select required value={broker} onChange={(e) => setBroker(e.target.value)}>
              <option value="">Choose…</option>
              {brokers.data?.map((b) => (
                <option key={b.code} value={b.code}>
                  {b.name}
                </option>
              ))}
            </select>
          )}
        </label>
        <label>
          Name
          <input required maxLength={200} value={name} onChange={(e) => setName(e.target.value)} />
        </label>
        <label>
          Account type
          <select value={type} onChange={(e) => setType(e.target.value as AccountType)}>
            {ACCOUNT_TYPES.map((t) => (
              <option key={t} value={t}>
                {TYPE_LABELS[t]}
              </option>
            ))}
          </select>
        </label>
        {type === "OTHER" && (
          <label className="inline">
            <input type="checkbox" checked={isRegistered} onChange={(e) => setIsRegistered(e.target.checked)} />
            Registered
          </label>
        )}
        <label>
          Base currency
          <select value={currency} onChange={(e) => setCurrency(e.target.value)}>
            {(currencies.data ?? [{ code: "CAD", name: "Canadian dollar", minor_units: 2 }]).map((c) => (
              <option key={c.code} value={c.code}>
                {c.code}
              </option>
            ))}
          </select>
        </label>
        <label>
          Account number
          <input value={number} maxLength={64} onChange={(e) => setNumber(e.target.value)} />
        </label>
        <label>
          Management
          <select value={management} onChange={(e) => setManagement(e.target.value as Management)}>
            <option value="self_directed">Self-directed</option>
            <option value="robo">Robo / managed portfolio</option>
          </select>
        </label>
        <label>
          Opened on
          <input type="date" value={openedOn} onChange={(e) => setOpenedOn(e.target.value)} />
        </label>
        {account && (
          <>
            <label>
              Status
              <select value={status} onChange={(e) => setStatus(e.target.value as AccountStatus)}>
                <option value="open">Open</option>
                <option value="closed">Closed</option>
                <option value="archived">Archived</option>
              </select>
            </label>
            {status !== "open" && (
              <label>
                Closed on
                <input type="date" value={closedOn} onChange={(e) => setClosedOn(e.target.value)} />
              </label>
            )}
          </>
        )}
        <label className="wide">
          Notes
          <textarea rows={2} value={notes} onChange={(e) => setNotes(e.target.value)} />
        </label>
      </div>
      {errorText && (
        <p className="error" role="alert">
          {errorText}
        </p>
      )}
      <div className="actions">
        <button type="submit" disabled={save.isPending}>
          {account ? "Save changes" : "Create account"}
        </button>
        <button type="button" className="secondary" onClick={onDone}>
          Cancel
        </button>
      </div>
    </form>
  );
}
