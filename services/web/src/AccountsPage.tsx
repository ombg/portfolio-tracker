import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { TYPE_LABELS, api, type Account } from "./api";
import { AccountForm } from "./AccountForm";

type Editing = { mode: "create" } | { mode: "edit"; account: Account } | null;

export function AccountsPage() {
  const [includeArchived, setIncludeArchived] = useState(false);
  const [editing, setEditing] = useState<Editing>(null);
  const accounts = useQuery({
    queryKey: ["accounts", includeArchived],
    queryFn: () => api.accounts(includeArchived),
  });

  return (
    <section>
      <div className="toolbar">
        <h1>Accounts</h1>
        <span className="spacer" />
        <label className="inline">
          <input type="checkbox" checked={includeArchived} onChange={(e) => setIncludeArchived(e.target.checked)} />
          Show archived
        </label>
        <button type="button" onClick={() => setEditing({ mode: "create" })}>
          Add account
        </button>
      </div>

      {editing && (
        <AccountForm
          key={editing.mode === "edit" ? editing.account.id : "new"}
          account={editing.mode === "edit" ? editing.account : undefined}
          onDone={() => setEditing(null)}
        />
      )}

      {accounts.isPending && <p className="status">Loading accounts…</p>}
      {accounts.error && (
        <p className="error" role="alert">
          Accounts could not be loaded: {accounts.error.message}
        </p>
      )}
      {accounts.data && accounts.data.length === 0 && <p className="status">No accounts yet. Add your first account.</p>}
      {accounts.data && accounts.data.length > 0 && (
        <table aria-label="Accounts">
          <thead>
            <tr>
              <th scope="col">Name</th>
              <th scope="col">Broker</th>
              <th scope="col">Type</th>
              <th scope="col">Tax</th>
              <th scope="col">Currency</th>
              <th scope="col">Status</th>
              <th scope="col">
                <span className="visually-hidden">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {accounts.data.map((a) => (
              <tr key={a.id}>
                <td>
                  {a.name}
                  {a.management === "robo" && <span className="tag">Managed, account-level only</span>}
                </td>
                <td>{a.broker.name}</td>
                <td>{TYPE_LABELS[a.type]}</td>
                <td>{a.is_registered ? "Registered" : "Non-registered"}</td>
                <td>{a.base_currency}</td>
                <td className={`status-${a.status}`}>{a.status}</td>
                <td>
                  <button type="button" className="link" onClick={() => setEditing({ mode: "edit", account: a })}>
                    Edit<span className="visually-hidden"> {a.name}</span>
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  );
}
