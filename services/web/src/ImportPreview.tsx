import { useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ApiError,
  EXCHANGES,
  LEDGER_TYPES,
  ROW_STATUSES,
  TYPE_LABELS,
  api,
  type Account,
  type CommitResult,
  type ImportDetail,
  type ImportRow,
  type RowEdit,
  type RowStatus,
} from "./api";
import { AccountForm } from "./AccountForm";
import { IMPORT_STATUS_LABELS, countsText, formatTime } from "./ImportsPage";
import { href } from "./route";

// Which fixes the importer accepts for a row (services/importer/importer/imports.py, D-36). The server decides; the
// page only offers what it would accept.
const ACTION_FIXABLE = new Set(["unmapped_action", "action_rule"]);
const SECURITY_FIXABLE = new Set(["unknown_broker_code", "proposed_security", "unknown_symbol", "symbol_rule"]);

const STATUS_LABELS: Record<RowStatus, string> = {
  error: "Error",
  warning: "Warning",
  ok: "OK",
  duplicate: "Duplicate",
  skipped: "Skipped",
  excluded: "Excluded",
};

// Rows a commit refused (`unresolved_securities`), with the reason and the row's security at that moment.
type Refusal = { reason: string; security: string };
type Unresolved = Map<number, string>;

function codes(row: ImportRow): Set<string> {
  return new Set((row.messages ?? []).map((m) => m.code));
}

function canFixType(row: ImportRow): boolean {
  return [...codes(row)].some((c) => ACTION_FIXABLE.has(c));
}

function canFixSecurity(row: ImportRow, unresolved: Unresolved): boolean {
  if (unresolved.has(row.row_no)) return true;
  if ([...codes(row)].some((c) => SECURITY_FIXABLE.has(c))) return true;
  // A listed symbol whose exchange the file does not state (Wealthsimple, D-37).
  const sec = row.normalized?.security;
  return !!sec && !sec.exchange && !sec.security_id && sec.security_type !== "gic" && !!sec.symbol;
}

function securityText(row: ImportRow): string {
  const sec = row.normalized?.security;
  if (!sec) return "";
  if (sec.symbol) return sec.exchange ? `${sec.symbol} (${sec.exchange})` : `${sec.symbol} (exchange unknown)`;
  if (sec.proposed) {
    const p = sec.proposed;
    // A broker code, or a symbol the file gives without its exchange (Questrade CAD dividends).
    const from = sec.broker_code ?? String(row.raw.Symbol ?? "");
    return `${from} → ${p.symbol}${p.exchange ? ` (${p.exchange})` : ""}?`;
  }
  return sec.broker_code ?? "";
}

export function ImportPreview({ id }: { id: string }) {
  const qc = useQueryClient();
  const detail = useQuery({ queryKey: ["import", id], queryFn: () => api.importDetail(id) });
  const accounts = useQuery({ queryKey: ["accounts", true], queryFn: () => api.accounts(true) });
  const [filter, setFilter] = useState<RowStatus | "all">("all");
  const [confirm, setConfirm] = useState<"commit" | "discard" | null>(null);
  const [result, setResult] = useState<CommitResult | null>(null);
  const [refused, setRefused] = useState<Map<number, Refusal>>(new Map());

  const refresh = () => {
    qc.invalidateQueries({ queryKey: ["import", id] });
    qc.invalidateQueries({ queryKey: ["imports"] });
  };

  const edit = useMutation({
    mutationFn: ({ row, change }: { row: number; change: RowEdit }) => api.editRow(id, row, change),
    onSuccess: refresh,
  });
  const reparse = useMutation({ mutationFn: () => api.reparse(id), onSuccess: refresh });
  const discard = useMutation({
    mutationFn: () => api.discard(id),
    onSuccess: () => {
      setConfirm(null);
      refresh();
    },
  });
  const commit = useMutation({
    mutationFn: () => api.commit(id),
    onSuccess: (res) => {
      setConfirm(null);
      setResult(res.result);
      setRefused(new Map());
      refresh();
    },
    onError: (err) => {
      setConfirm(null);
      if (err instanceof ApiError && err.code === "unresolved_securities") {
        const rows = (err.data.rows as { row: number; reason: string }[] | undefined) ?? [];
        const current = new Map((detail.data?.rows ?? []).map((r) => [r.row_no, r]));
        setRefused(
          new Map(
            rows.map((r) => [
              r.row,
              { reason: r.reason, security: JSON.stringify(current.get(r.row)?.normalized?.security ?? null) },
            ]),
          ),
        );
      }
    },
  });

  if (detail.isPending) return <p className="status">Loading import…</p>;
  if (detail.error) {
    const missing = detail.error instanceof ApiError && detail.error.status === 404;
    return (
      <section>
        <p className="error" role="alert">
          {missing ? "This import does not exist." : `The import could not be loaded: ${detail.error.message}`}
        </p>
        <a href={href({ page: "imports" })}>Back to imports</a>
      </section>
    );
  }

  const d = detail.data;
  // A refused row stays listed until a fix changes its security or it is excluded.
  const byNo = new Map(d.rows.map((r) => [r.row_no, r]));
  const unresolved: Unresolved = new Map(
    [...refused]
      .filter(([no, f]) => {
        const r = byNo.get(no);
        return r && r.status !== "excluded" && JSON.stringify(r.normalized?.security ?? null) === f.security;
      })
      .map(([no, f]) => [no, f.reason]),
  );
  const open = d.status === "parsed" || d.status === "needs_review";
  const accountNames = new Map((accounts.data ?? []).map((a) => [a.id, a.name]));
  const present = ROW_STATUSES.filter((s) => d.rows.some((r) => r.status === s));
  const rows = filter === "all" ? d.rows : d.rows.filter((r) => r.status === filter);
  const errors = d.counts.error ?? 0;
  const toWrite = (d.counts.ok ?? 0) + (d.counts.warning ?? 0);
  const warnings = d.counts.warning ?? 0;
  const busy = edit.isPending || reparse.isPending || commit.isPending || discard.isPending;
  const actionError = edit.error ?? reparse.error ?? discard.error ?? commit.error;

  return (
    <section>
      <p>
        <a href={href({ page: "imports" })}>← Imports</a>
      </p>
      <div className="toolbar">
        <h1>{d.file.name}</h1>
        <span className={`tag import-${d.status}`}>{IMPORT_STATUS_LABELS[d.status]}</span>
      </div>
      <p className="status">
        {d.broker.name} · uploaded {formatTime(d.created_at)} · parser {d.parser} v{d.parser_version}
        {d.committed_at && <> · committed {formatTime(d.committed_at)}</>}
      </p>
      <p className="status sub" aria-label="File fingerprint">
        Stored original, SHA-256 <code>{d.file.sha256}</code>
      </p>
      <p aria-label="Row counts">{countsText(d.counts, d.status)}</p>

      {d.status === "failed" && (
        <p className="error" role="alert">
          The file could not be read, so nothing was staged: {d.counts.failure ?? "no reason given"}. Check that the
          broker is right and the file is the activities export.
        </p>
      )}
      {d.status === "discarded" && (
        <p className="notice">Discarded. Nothing was written to the ledger; the same file can be uploaded again.</p>
      )}
      {d.status === "committed" && (
        <p className="notice" role="status">
          {result
            ? `Committed: ${result.inserted} added to the ledger, ${result.duplicate} already there (duplicates), ${result.linked_transfers} internal transfers linked.`
            : `Committed: ${d.counts.inserted ?? 0} added to the ledger, ${d.counts.duplicate ?? 0} already there (duplicates).`}
        </p>
      )}

      {open && (
        <>
          <p className={errors ? "notice blocking" : "notice"} role="status">
            {errors > 0
              ? `${errors} ${errors === 1 ? "row needs" : "rows need"} a fix or must be excluded before this import can be committed.`
              : toWrite > 0
                ? `${toWrite} rows will be written to the ledger${warnings ? `, ${warnings} of them with warnings` : ""}. Rows already in the ledger are reported as duplicates and not added again.`
                : "No row would reach the ledger."}
          </p>
          {d.untracked_accounts.length > 0 && (
            <UntrackedAccounts
              detail={d}
              accounts={accounts.data ?? []}
              busy={busy}
              onLink={(row, accountId) => edit.mutate({ row, change: { account_id: accountId } })}
              onCreated={() => reparse.mutate()}
            />
          )}
        </>
      )}

      {unresolved.size > 0 && (
        <div className="card" role="alert" aria-label="Unresolved securities">
          <h2>Commit refused: securities that cannot be placed</h2>
          <p>Nothing was written. Choose the listing for these rows (Fix), or exclude them, then commit again.</p>
          <ul>
            {[...unresolved].map(([row, reason]) => (
              <li key={row}>
                Row {row}: {reason}
              </li>
            ))}
          </ul>
        </div>
      )}

      {actionError && !(actionError instanceof ApiError && actionError.code === "unresolved_securities") && (
        <p className="error" role="alert">
          {actionError.message}
        </p>
      )}

      {open && (
        <div className="actions" aria-label="Import actions">
          {confirm === null && (
            <>
              <button
                type="button"
                disabled={!d.can_commit || busy}
                title={d.can_commit ? undefined : "Fix or exclude every error row first"}
                onClick={() => setConfirm("commit")}
              >
                Commit…
              </button>
              <button type="button" className="secondary" disabled={busy} onClick={() => setConfirm("discard")}>
                Discard…
              </button>
            </>
          )}
          {confirm === "commit" && (
            <div className="confirm" role="group" aria-label="Confirm commit">
              <span>
                Write {toWrite} rows to the ledger{warnings ? `, including ${warnings} with warnings` : ""}? Ledger rows
                are never edited afterwards; later fixes are recorded as corrections.
              </span>
              <button type="button" disabled={busy} onClick={() => commit.mutate()}>
                Commit now
              </button>
              <button type="button" className="secondary" onClick={() => setConfirm(null)}>
                Cancel
              </button>
            </div>
          )}
          {confirm === "discard" && (
            <div className="confirm" role="group" aria-label="Confirm discard">
              <span>Discard this import? Nothing is written; the file stays stored and can be uploaded again.</span>
              <button type="button" disabled={busy} onClick={() => discard.mutate()}>
                Discard now
              </button>
              <button type="button" className="secondary" onClick={() => setConfirm(null)}>
                Cancel
              </button>
            </div>
          )}
        </div>
      )}

      {d.rows.length > 0 && (
        <>
          <div className="filters" role="group" aria-label="Filter rows">
            {(["all", ...present] as const).map((s) => (
              <button
                key={s}
                type="button"
                className={filter === s ? "" : "secondary"}
                aria-pressed={filter === s}
                onClick={() => setFilter(s)}
              >
                {s === "all" ? `All (${d.rows.length})` : `${STATUS_LABELS[s]} (${d.rows.filter((r) => r.status === s).length})`}
              </button>
            ))}
          </div>
          <table aria-label="Import rows" className="rows">
            <thead>
              <tr>
                <th scope="col">Row</th>
                <th scope="col">Status</th>
                <th scope="col">Date</th>
                <th scope="col">Account</th>
                <th scope="col">Type</th>
                <th scope="col">Security</th>
                <th scope="col" className="num">
                  Quantity
                </th>
                <th scope="col" className="num">
                  Amount
                </th>
                <th scope="col">Messages</th>
                <th scope="col">
                  <span className="visually-hidden">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <RowView
                  key={r.row_no}
                  row={r}
                  importId={id}
                  open={open}
                  busy={busy}
                  unresolved={unresolved}
                  accountName={r.normalized?.account_id ? accountNames.get(r.normalized.account_id) : undefined}
                  onExclude={(excluded) => edit.mutate({ row: r.row_no, change: { excluded } })}
                  onFixed={refresh}
                />
              ))}
            </tbody>
          </table>
        </>
      )}
    </section>
  );
}

function RowView({
  row,
  importId,
  open,
  busy,
  unresolved,
  accountName,
  onExclude,
  onFixed,
}: {
  row: ImportRow;
  importId: string;
  open: boolean;
  busy: boolean;
  unresolved: Unresolved;
  accountName?: string;
  onExclude: (excluded: boolean) => void;
  onFixed: () => void;
}) {
  const [fixing, setFixing] = useState(false);
  const n = row.normalized;
  const fixType = open && canFixType(row);
  const fixSecurity = open && canFixSecurity(row, unresolved);
  const excluded = row.status === "excluded";
  const status = row.outcome ? (row.outcome === "inserted" ? "Inserted" : "Duplicate") : STATUS_LABELS[row.status];
  const untracked = n?.untracked_account;

  return (
    <>
      <tr className={`row-${row.status}${unresolved.has(row.row_no) ? " row-unresolved" : ""}`}>
        <td>{row.row_no}</td>
        <td className={`status-${row.status}`}>{status}</td>
        <td className="nowrap">{n?.trade_date ?? ""}</td>
        <td>{accountName ?? (untracked ? `not tracked (${untracked.number})` : "")}</td>
        <td>{n?.type ?? ""}</td>
        <td>
          {securityText(row)}
          {n?.security?.name && <div className="sub">{n.security.name}</div>}
        </td>
        <td className="num">{n?.quantity ?? ""}</td>
        <td className="num">{n?.net_amount !== undefined ? `${n.net_amount} ${n.currency ?? ""}` : ""}</td>
        <td>
          {(row.messages ?? []).map((m, i) => (
            <div key={i} className={`msg msg-${m.level}`}>
              {m.text}
            </div>
          ))}
          {unresolved.has(row.row_no) && <div className="msg msg-error">{unresolved.get(row.row_no)}</div>}
          <details>
            <summary>Source row</summary>
            <pre>{JSON.stringify(row.raw, null, 2)}</pre>
          </details>
        </td>
        <td className="row-actions">
          {(fixType || fixSecurity) && (
            <button type="button" className="link" onClick={() => setFixing(!fixing)} aria-expanded={fixing}>
              Fix<span className="visually-hidden"> row {row.row_no}</span>
            </button>
          )}
          {open && row.status !== "skipped" && (
            <button type="button" className="link" disabled={busy} onClick={() => onExclude(!excluded)}>
              {excluded ? "Include" : "Exclude"}
              <span className="visually-hidden"> row {row.row_no}</span>
            </button>
          )}
        </td>
      </tr>
      {fixing && (
        <tr className="fix-row">
          <td colSpan={10}>
            <FixForm
              row={row}
              importId={importId}
              type={fixType}
              security={fixSecurity}
              onDone={() => {
                setFixing(false);
                onFixed();
              }}
            />
          </td>
        </tr>
      )}
    </>
  );
}

// Fixes are saved as rules for this broker and the whole file is parsed again (D-36), so one fix settles every row
// with the same action or code, here and in later imports.
function FixForm({
  row,
  importId,
  type: offerType,
  security: offerSecurity,
  onDone,
}: {
  row: ImportRow;
  importId: string;
  type: boolean;
  security: boolean;
  onDone: () => void;
}) {
  const sec = row.normalized?.security;
  const [type, setType] = useState(row.normalized?.type ?? "");
  const [symbol, setSymbol] = useState(sec?.proposed?.symbol ?? sec?.symbol ?? "");
  const [exchange, setExchange] = useState(sec?.proposed?.exchange ?? sec?.exchange ?? "");

  const save = useMutation({
    mutationFn: () => {
      const change: RowEdit = {};
      if (offerType && type) change.type = type;
      if (offerSecurity && symbol && exchange) change.security = { symbol: symbol.trim(), exchange };
      return api.editRow(importId, row.row_no, change);
    },
    onSuccess: onDone,
  });

  function submit(e: FormEvent) {
    e.preventDefault();
    save.mutate();
  }

  const code = sec?.broker_code ?? sec?.symbol;
  return (
    <form className="fix" onSubmit={submit} aria-label={`Fix row ${row.row_no}`}>
      {offerType && (
        <label>
          Transaction type
          <select required value={type} onChange={(e) => setType(e.target.value)}>
            <option value="">Choose…</option>
            {LEDGER_TYPES.map((t) => (
              <option key={t} value={t}>
                {t}
              </option>
            ))}
          </select>
        </label>
      )}
      {offerSecurity && (
        <>
          <label>
            Symbol
            <input required value={symbol} maxLength={20} onChange={(e) => setSymbol(e.target.value.toUpperCase())} />
          </label>
          <label>
            Exchange
            <select required value={exchange} onChange={(e) => setExchange(e.target.value)}>
              <option value="">Choose…</option>
              {EXCHANGES.map((x) => (
                <option key={x} value={x}>
                  {x}
                </option>
              ))}
            </select>
          </label>
        </>
      )}
      <p className="status wide">
        Saved as a rule for this broker: every row with the same {offerType ? "action" : ""}
        {offerType && offerSecurity ? " or " : ""}
        {offerSecurity ? `code${code ? ` (${code})` : ""}` : ""} uses it, in this file and later imports.
      </p>
      {save.error && (
        <p className="error wide" role="alert">
          {save.error.message}
        </p>
      )}
      <div className="actions wide">
        <button type="submit" disabled={save.isPending}>
          Save fix
        </button>
        <button type="button" className="secondary" onClick={onDone}>
          Cancel
        </button>
      </div>
    </form>
  );
}

// Rows of account numbers the user does not track are skipped (D-22). Either track the number as a new account or
// put it on an existing account of this broker that has no number yet; the file is then parsed again.
function UntrackedAccounts({
  detail,
  accounts,
  busy,
  onLink,
  onCreated,
}: {
  detail: ImportDetail;
  accounts: Account[];
  busy: boolean;
  onLink: (row: number, accountId: string) => void;
  onCreated: () => void;
}) {
  const [creating, setCreating] = useState<string | null>(null);
  const [linkTo, setLinkTo] = useState<Record<string, string>>({});
  const linkable = accounts.filter((a) => a.broker.code === detail.broker.code && !a.external_number);

  return (
    <div className="card" aria-label="Untracked accounts">
      <h2>Accounts in this file you do not track</h2>
      <p className="status">Their rows are skipped and nothing of them reaches the ledger unless you track them.</p>
      <ul className="untracked">
        {detail.untracked_accounts.map((u) => {
          const p = u.proposal;
          const firstRow = detail.rows.find((r) => r.normalized?.untracked_account?.number === u.number)?.row_no;
          return (
            <li key={u.number}>
              <div>
                <strong>{u.number}</strong> {p.account_type ? `(${p.account_type})` : ""}: {u.rows} {u.rows === 1 ? "row" : "rows"} skipped.
                Proposed: {detail.broker.name} {p.proposed_type ? TYPE_LABELS[p.proposed_type] : ""}
                {p.proposed_management === "robo" ? ", managed" : ""}
                {p.proposed_base_currency ? `, ${p.proposed_base_currency}` : ""}.
              </div>
              <div className="actions">
                <button type="button" disabled={busy} onClick={() => setCreating(u.number)}>
                  Track this account<span className="visually-hidden"> {u.number}</span>
                </button>
                {linkable.length > 0 && firstRow !== undefined && (
                  <>
                    <label className="inline">
                      <span className="visually-hidden">Existing account for {u.number}</span>
                      <select
                        value={linkTo[u.number] ?? ""}
                        onChange={(e) => setLinkTo({ ...linkTo, [u.number]: e.target.value })}
                      >
                        <option value="">Or link to an existing account…</option>
                        {linkable.map((a) => (
                          <option key={a.id} value={a.id}>
                            {a.name}
                          </option>
                        ))}
                      </select>
                    </label>
                    <button
                      type="button"
                      className="secondary"
                      disabled={busy || !linkTo[u.number]}
                      onClick={() => onLink(firstRow, linkTo[u.number])}
                    >
                      Link<span className="visually-hidden"> {u.number}</span>
                    </button>
                  </>
                )}
              </div>
              {creating === u.number && (
                <AccountForm
                  initial={{
                    broker: detail.broker.code,
                    name: `${detail.broker.name} ${p.proposed_type ? TYPE_LABELS[p.proposed_type] : ""}`.trim(),
                    type: p.proposed_type,
                    management: p.proposed_management,
                    base_currency: p.proposed_base_currency,
                    external_number: u.number,
                  }}
                  onSaved={onCreated}
                  onDone={() => setCreating(null)}
                />
              )}
            </li>
          );
        })}
      </ul>
    </div>
  );
}
