import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { TYPE_LABELS, api, type AccountHoldings, type CombinedPosition, type HoldingsReport, type Total } from "./api";
import { Amount, FlagTags, MarketValue, SecurityLink, UnitCost, Unrealized, Weight, flagCodes } from "./holdingsUi";
import { money, quantity } from "./format";
import { MarketStatusCard } from "./MarketStatus";

type View = "account" | "security";

// Holdings (FR-7, D-38): by account or by security, for a scope. Until market data exists (M4) market values read
// "No price" and totals say what they leave out.
export function HoldingsPage() {
  const [scope, setScope] = useState("all");
  const [view, setView] = useState<View>("account");
  const [showClosed, setShowClosed] = useState(false);
  const accounts = useQuery({ queryKey: ["accounts", false], queryFn: () => api.accounts(false) });
  const report = useQuery({ queryKey: ["holdings", scope], queryFn: () => api.holdings(scope) });

  return (
    <section>
      <div className="toolbar">
        <h1>Holdings</h1>
        <span className="spacer" />
        <label className="inline">
          Scope{" "}
          <select value={scope} onChange={(e) => setScope(e.target.value)}>
            <option value="all">All accounts</option>
            <option value="registered">Registered</option>
            <option value="non_registered">Non-registered</option>
            {(accounts.data ?? []).map((a) => (
              <option key={a.id} value={`account:${a.id}`}>
                {a.name}
              </option>
            ))}
          </select>
        </label>
        <div className="segmented" role="group" aria-label="View">
          <button type="button" aria-pressed={view === "account"} onClick={() => setView("account")}>
            By account
          </button>
          <button type="button" aria-pressed={view === "security"} onClick={() => setView("security")}>
            By security
          </button>
        </div>
        <label className="inline">
          <input type="checkbox" checked={showClosed} onChange={(e) => setShowClosed(e.target.checked)} />
          Show closed positions
        </label>
      </div>

      <MarketStatusCard />
      {report.isPending && <p className="status">Loading holdings…</p>}
      {report.error && (
        <p className="error" role="alert">
          Holdings could not be loaded: {report.error.message}
        </p>
      )}
      {report.data && <Report report={report.data} view={view} showClosed={showClosed} />}
    </section>
  );
}

function Report({ report, view, showClosed }: { report: HoldingsReport; view: View; showClosed: boolean }) {
  if (report.accounts.length === 0) {
    return <p className="status">No accounts in this scope.</p>;
  }
  const hasRows = report.accounts.some((a) => a.positions.length > 0 || a.cash.length > 0);
  if (!hasRows) {
    return <p className="status">Nothing in the ledger yet. Import a broker export on the Imports page.</p>;
  }
  return (
    <>
      <Totals report={report} />
      {view === "account" &&
        report.accounts.map((a) => <AccountSection key={a.account.id} holdings={a} showClosed={showClosed} />)}
      {view === "security" && <SecurityTable report={report} showClosed={showClosed} />}
    </>
  );
}

function excludesText(t: Total, what: string): string | null {
  if (t.excludes === 0) return null;
  return `excludes ${t.excludes} ${t.excludes === 1 ? "position" : "positions"} ${what}`;
}

function TotalItem({ label, total, what }: { label: string; total: Total; what: string }) {
  const note = excludesText(total, what);
  return (
    <div>
      <dt>{label}</dt>
      <dd>
        {total.amount === null ? <span className="unknown">Unknown</span> : `${money(total.amount)} CAD`}
        {note && <div className="sub">{note}</div>}
      </dd>
    </div>
  );
}

function Totals({ report }: { report: HoldingsReport }) {
  const t = report.totals;
  const mv = t.market_value;
  return (
    <div className="card">
      <dl className="totals" aria-label="Totals">
        <TotalItem label="Book value" total={t.book_value_cad} what="with an unknown cost" />
        <div>
          <dt>Market value</dt>
          <dd>
            {mv.amount === null ? <span className="unknown">No price</span> : `${money(mv.amount)} CAD`}
            {mv.excludes > 0 && (
              <div className="sub">
                excludes {mv.excludes} {mv.excludes === 1 ? "position" : "positions"} without a value
              </div>
            )}
          </dd>
        </div>
        <TotalItem label="Unrealized gain" total={t.unrealized_gain_cad} what="without a value or cost" />
        <TotalItem label="Realized gain" total={t.realized_gain_cad} what="with an unknown CAD value" />
        <TotalItem label="Income received" total={t.income_cad} what="with an unknown CAD value" />
        <div>
          <dt>Cash</dt>
          <dd>
            {t.cash.length === 0 && <span className="sub">None</span>}
            {t.cash.map((c) => (
              <div key={c.currency}>
                {money(c.amount)} {c.currency}
              </div>
            ))}
          </dd>
        </div>
      </dl>
      {report.as_of && <p className="sub">Ledger up to {report.as_of}.</p>}
    </div>
  );
}

function AccountSection({ holdings, showClosed }: { holdings: AccountHoldings; showClosed: boolean }) {
  const a = holdings.account;
  const rows = holdings.positions.filter((p) => showClosed || p.open);
  const closed = holdings.positions.length - holdings.positions.filter((p) => p.open).length;
  return (
    <section className="card" aria-label={`Account ${a.name}`}>
      <h2>
        {a.name}
        <span className="tag">{TYPE_LABELS[a.type]}</span>
        <span className="tag">{a.broker.name}</span>
        {a.management === "robo" && <span className="tag">Managed, account-level only</span>}
        {a.status !== "open" && <span className="tag">{a.status}</span>}
      </h2>
      {rows.length === 0 ? (
        <p className="status">{closed > 0 ? "No open positions." : "No positions."}</p>
      ) : (
        <div className="scroll">
          <table aria-label={`Holdings in ${a.name}`}>
            <thead>
              <tr>
                <th scope="col">Security</th>
                <th scope="col" className="num">Quantity</th>
                <th scope="col" className="num">Average cost</th>
                <th scope="col" className="num">Book value</th>
                <th scope="col" className="num">Book value (CAD)</th>
                <th scope="col" className="num">Market value (CAD)</th>
                <th scope="col" className="num">Unrealized (CAD)</th>
                <th scope="col" className="num">Weight</th>
                <th scope="col" className="num">Realized gain (CAD)</th>
                <th scope="col" className="num">Income (CAD)</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((p) => (
                <tr key={p.security.id} className={p.open ? undefined : "closed"}>
                  <td>
                    <SecurityLink security={p.security} />
                    {!p.open && <span className="tag">Closed</span>}
                    <FlagTags codes={flagCodes(p.flags)} />
                  </td>
                  <td className="num">{quantity(p.quantity)}</td>
                  <td className="num">
                    <UnitCost value={p.avg_cost} currency={p.book_currency} held={p.open} />
                  </td>
                  <td className="num">
                    <Amount value={p.book_value} currency={p.book_currency} />
                  </td>
                  <td className="num">
                    <Amount value={p.book_value_cad} />
                  </td>
                  <td className="num">
                    <MarketValue value={p.market_value} reason={p.market_value_reason} valuation={p.valuation} />
                  </td>
                  <td className="num">
                    <Unrealized value={p.unrealized_gain_cad} />
                  </td>
                  <td className="num">
                    <Weight value={p.weight} />
                  </td>
                  <td className="num">
                    <Amount value={p.realized_gain_cad} />
                  </td>
                  <td className="num">
                    <Amount value={p.income_cad} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {!showClosed && closed > 0 && (
        <p className="sub">
          {closed} closed {closed === 1 ? "position" : "positions"} hidden.
        </p>
      )}
      <p className="cash" aria-label={`Cash in ${a.name}`}>
        Cash:{" "}
        {holdings.cash.length === 0
          ? "none"
          : holdings.cash.map((c) => `${money(c.amount)} ${c.currency}`).join(" · ")}
      </p>
    </section>
  );
}

function SecurityTable({ report, showClosed }: { report: HoldingsReport; showClosed: boolean }) {
  const rows = report.securities.filter((s) => showClosed || s.open);
  return (
    <section className="card" aria-label="By security">
      {report.excluded.length > 0 && (
        <p className="notice">
          Excludes {report.excluded.map((e) => e.name).join(", ")}: managed{" "}
          {report.excluded.length === 1 ? "account" : "accounts"}, reported at account level only.
        </p>
      )}
      {rows.length === 0 ? (
        <p className="status">No open positions.</p>
      ) : (
        <div className="scroll">
          <table aria-label="Holdings by security">
            <thead>
              <tr>
                <th scope="col">Security</th>
                <th scope="col" className="num">Quantity</th>
                <th scope="col" className="num">
                  Average cost <span className="sub">(informational)</span>
                </th>
                <th scope="col" className="num">Book value (CAD)</th>
                <th scope="col" className="num">Market value (CAD)</th>
                <th scope="col" className="num">Unrealized (CAD)</th>
                <th scope="col" className="num">Weight</th>
                <th scope="col" className="num">Realized gain (CAD)</th>
                <th scope="col" className="num">Income (CAD)</th>
                <th scope="col" className="num">Accounts</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((s) => (
                <CombinedRow key={s.security.id} s={s} />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

function CombinedRow({ s }: { s: CombinedPosition }) {
  return (
    <tr className={s.open ? undefined : "closed"}>
      <td>
        <SecurityLink security={s.security} />
        {!s.open && <span className="tag">Closed</span>}
        <FlagTags codes={s.flags} />
      </td>
      <td className="num">{quantity(s.quantity)}</td>
      <td className="num">
        {s.book_currency ? (
          <UnitCost value={s.avg_cost} currency={s.book_currency} held={s.open} />
        ) : (
          <UnitCost value={s.avg_cost_cad} currency="CAD" held={s.open} />
        )}
      </td>
      <td className="num">
        <Amount value={s.book_value_cad} />
      </td>
      <td className="num">
        <MarketValue value={s.market_value} reason={s.market_value_reason} valuation={s.valuation} />
      </td>
      <td className="num">
        <Unrealized value={s.unrealized_gain_cad} />
      </td>
      <td className="num">
        <Weight value={s.weight} />
      </td>
      <td className="num">
        <Amount value={s.realized_gain_cad} />
      </td>
      <td className="num">
        <Amount value={s.income_cad} />
      </td>
      <td className="num">{s.accounts}</td>
    </tr>
  );
}
