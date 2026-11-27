import { useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, api, type ImportCounts, type ImportStatus } from "./api";
import { href, navigate } from "./route";

export const IMPORT_STATUS_LABELS: Record<ImportStatus, string> = {
  parsed: "Ready to commit",
  needs_review: "Needs review",
  committed: "Committed",
  discarded: "Discarded",
  failed: "Failed",
};

const COUNT_LABELS = {
  inserted: ["inserted", "inserted"],
  error: ["error", "errors"],
  warning: ["warning", "warnings"],
  ok: ["ok", "ok"],
  duplicate: ["duplicate", "duplicates"],
  skipped: ["skipped", "skipped"],
  excluded: ["excluded", "excluded"],
} as const;
type CountKey = keyof typeof COUNT_LABELS;
const OPEN_ORDER: CountKey[] = ["error", "warning", "ok", "skipped", "excluded"];
// After commit the ok/warning rows split into inserted and duplicate (D-37).
const COMMITTED_ORDER: CountKey[] = ["inserted", "duplicate", "skipped", "excluded"];

// "2 errors · 5 ok · 3 skipped"; a failed batch shows its reason instead of counts.
export function countsText(counts: ImportCounts, status: ImportStatus): string {
  if (counts.failure) return `Not readable: ${counts.failure}`;
  const order = status === "committed" ? COMMITTED_ORDER : OPEN_ORDER;
  const parts = order
    .filter((k) => (counts[k] ?? 0) > 0)
    .map((k) => `${counts[k]} ${COUNT_LABELS[k][counts[k] === 1 ? 0 : 1]}`);
  return parts.length ? parts.join(" · ") : "no rows";
}

export function formatTime(iso: string): string {
  return new Date(iso).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
}

export function ImportsPage() {
  const imports = useQuery({ queryKey: ["imports"], queryFn: api.imports });

  return (
    <section>
      <div className="toolbar">
        <h1>Imports</h1>
      </div>
      <UploadForm />

      <h2>History</h2>
      {imports.isPending && <p className="status">Loading imports…</p>}
      {imports.error && (
        <p className="error" role="alert">
          Imports could not be loaded: {imports.error.message}
        </p>
      )}
      {imports.data && imports.data.length === 0 && <p className="status">No imports yet.</p>}
      {imports.data && imports.data.length > 0 && (
        <table aria-label="Import history">
          <thead>
            <tr>
              <th scope="col">Uploaded</th>
              <th scope="col">File</th>
              <th scope="col">Broker</th>
              <th scope="col">Accounts</th>
              <th scope="col">Status</th>
              <th scope="col">Rows</th>
            </tr>
          </thead>
          <tbody>
            {imports.data.map((b) => (
              <tr key={b.id}>
                <td>{formatTime(b.created_at)}</td>
                <td>
                  <a href={href({ page: "import", id: b.id })}>{b.file.name}</a>
                </td>
                <td>{b.broker.name}</td>
                <td>{b.accounts.length ? b.accounts.map((a) => a.name).join(", ") : "none"}</td>
                <td className={`import-${b.status}`}>{IMPORT_STATUS_LABELS[b.status]}</td>
                <td>{countsText(b.counts, b.status)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  );
}

function UploadForm() {
  const qc = useQueryClient();
  const brokers = useQuery({ queryKey: ["brokers"], queryFn: api.brokers });
  const accounts = useQuery({ queryKey: ["accounts", false], queryFn: () => api.accounts(false) });
  const [broker, setBroker] = useState("");
  const [accountId, setAccountId] = useState("");
  const [file, setFile] = useState<File | null>(null);

  const send = useMutation({
    mutationFn: () => api.upload(file!, broker, accountId || null),
    onSuccess: (res) => {
      qc.invalidateQueries({ queryKey: ["imports"] });
      navigate({ page: "import", id: res.batch_id });
    },
  });

  function submit(e: FormEvent) {
    e.preventDefault();
    if (file) send.mutate();
  }

  const err = send.error;
  const earlier = err instanceof ApiError && err.code === "duplicate_file" ? String(err.data.batch_id ?? "") : "";
  const brokerAccounts = (accounts.data ?? []).filter((a) => a.broker.code === broker);

  return (
    <form className="card" onSubmit={submit} aria-label="Upload export">
      <h2>Upload a broker export</h2>
      <div className="grid">
        <label>
          Broker
          <select
            required
            value={broker}
            onChange={(e) => {
              setBroker(e.target.value);
              setAccountId("");
            }}
          >
            <option value="">Choose…</option>
            {brokers.data?.map((b) => (
              <option key={b.code} value={b.code}>
                {b.name}
              </option>
            ))}
          </select>
        </label>
        <label>
          Account
          <select value={accountId} onChange={(e) => setAccountId(e.target.value)} disabled={!broker}>
            <option value="">Every account in the export</option>
            {brokerAccounts.map((a) => (
              <option key={a.id} value={a.id}>
                {a.name}
              </option>
            ))}
          </select>
        </label>
        <label>
          File
          <input
            type="file"
            required
            accept=".csv,.xlsx,text/csv,application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
            onChange={(e) => setFile(e.target.files?.[0] ?? null)}
          />
        </label>
      </div>
      <p className="status">
        Nothing reaches the ledger until you review the rows and commit. Rows of accounts you do not track are skipped
        and listed.
      </p>
      {err && (
        <p className="error" role="alert">
          {err.message}
          {earlier && (
            <>
              {" "}
              <a href={href({ page: "import", id: earlier })}>Open the earlier import</a>
            </>
          )}
        </p>
      )}
      <div className="actions">
        <button type="submit" disabled={send.isPending || !file || !broker}>
          {send.isPending ? "Uploading…" : "Upload and preview"}
        </button>
      </div>
    </form>
  );
}
