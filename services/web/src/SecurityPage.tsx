import { useQuery } from "@tanstack/react-query";
import { api, type LedgerRow, type Position, type SecurityDetail } from "./api";
import { Amount, FlagTags, MarketValue, UnitCost, flagCodes, flagInfo, securityLabel } from "./holdingsUi";
import { money, quantity, trim } from "./format";
import { href } from "./route";

// One security (FR-7, AC-8): the combined line, every account's position and every ledger row behind them with its
// source, so each number on the page can be traced (rule 5, NFR-2).
export function SecurityPage({ id }: { id: string }) {
  const detail = useQuery({ queryKey: ["security", id], queryFn: () => api.security(id) });
  return (
    <section>
      <p>
        <a href={href({ page: "holdings" })}>← Holdings</a>
      </p>
      {detail.isPending && <p className="status">Loading security…</p>}
      {detail.error && (
        <p className="error" role="alert">
          The security could not be loaded: {detail.error.message}
        </p>
      )}
      {detail.data && <Detail d={detail.data} />}
    </section>
  );
}

function Detail({ d }: { d: SecurityDetail }) {
  const s = d.security;
  const earlier = d.symbols.filter((x) => x.valid_to !== null);
  const flags = d.positions.flatMap((p) => p.position.flags.map((f) => ({ ...f, account: p.account.name })));
  return (
    <>
      <h1>
        {securityLabel(s)}
        {s.symbol && <span className="sub"> {s.name}</span>}
      </h1>
      <p className="sub">
        {s.type ? s.type.toUpperCase() : "Not classified yet"} · trades in {s.trading_currency}
        {earlier.length > 0 && (
          <>
            {" "}
            · earlier:{" "}
            {earlier.map((x) => `${x.symbol} (${x.exchange}) until ${x.valid_to}`).join(", ")}
          </>
        )}
      </p>

      {d.combined && (
        <div className="card">
          <h2>All accounts</h2>
          <dl className="totals" aria-label="Combined position">
            <div>
              <dt>Quantity</dt>
              <dd>{quantity(d.combined.quantity)}</dd>
            </div>
            <div>
              <dt>Average cost (informational)</dt>
              <dd>
                {d.combined.book_currency ? (
                  <UnitCost value={d.combined.avg_cost} currency={d.combined.book_currency} held={d.combined.open} />
                ) : (
                  <UnitCost value={d.combined.avg_cost_cad} currency="CAD" held={d.combined.open} />
                )}
              </dd>
            </div>
            <div>
              <dt>Book value</dt>
              <dd>
                <Amount value={d.combined.book_value_cad} currency="CAD" />
              </dd>
            </div>
            <div>
              <dt>Realized gain</dt>
              <dd>
                <Amount value={d.combined.realized_gain_cad} currency="CAD" />
              </dd>
            </div>
            <div>
              <dt>Income received</dt>
              <dd>
                <Amount value={d.combined.income_cad} currency="CAD" />
              </dd>
            </div>
            <div>
              <dt>Market value</dt>
              <dd>
                <MarketValue value={null} reason={d.combined.open ? "no_price" : ""} />
              </dd>
            </div>
          </dl>
          {d.excluded.length > 0 && (
            <p className="sub">
              Excludes {d.excluded.map((e) => e.name).join(", ")}: managed account, reported at account level only.
            </p>
          )}
        </div>
      )}

      {flags.length > 0 && (
        <div className="notice" role="note" aria-label="Conditions">
          <ul>
            {flags.map((f) => (
              <li key={`${f.account}-${f.code}`}>
                <strong>{flagInfo(f.code).label}</strong> in {f.account} from {f.date}: {flagInfo(f.code).explain}
              </li>
            ))}
          </ul>
        </div>
      )}

      <h2>By account</h2>
      <div className="scroll">
        <table aria-label="Positions by account">
          <thead>
            <tr>
              <th scope="col">Account</th>
              <th scope="col" className="num">Quantity</th>
              <th scope="col" className="num">Average cost</th>
              <th scope="col" className="num">Book value</th>
              <th scope="col" className="num">Book value (CAD)</th>
              <th scope="col" className="num">Realized gain (CAD)</th>
              <th scope="col" className="num">Income (CAD)</th>
              <th scope="col" className="num">Market value</th>
            </tr>
          </thead>
          <tbody>
            {d.positions.map(({ account, position }) => (
              <PositionRow key={account.id} name={account.name} registered={account.is_registered}
                managed={account.management === "robo"} p={position} />
            ))}
          </tbody>
        </table>
      </div>

      <h2>Ledger</h2>
      <div className="scroll">
        <table aria-label="Ledger rows">
          <thead>
            <tr>
              <th scope="col">Date</th>
              <th scope="col">Account</th>
              <th scope="col">Type</th>
              <th scope="col" className="num">Quantity</th>
              <th scope="col" className="num">Price</th>
              <th scope="col" className="num">Amount</th>
              <th scope="col">Source</th>
            </tr>
          </thead>
          <tbody>
            {d.transactions.map((r) => (
              <LedgerLine key={r.id} r={r} />
            ))}
          </tbody>
        </table>
      </div>
    </>
  );
}

function PositionRow(props: { name: string; registered: boolean; managed: boolean; p: Position }) {
  const { p } = props;
  return (
    <tr className={p.open ? undefined : "closed"}>
      <td>
        {props.name}
        <div className="sub">
          {props.registered ? "Registered" : "Non-registered"}
          {props.managed && " · managed, account-level only"}
        </div>
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
        <Amount value={p.realized_gain_cad} />
      </td>
      <td className="num">
        <Amount value={p.income_cad} />
      </td>
      <td className="num">
        <MarketValue value={p.market_value} reason={p.market_value_reason} />
      </td>
    </tr>
  );
}

const TYPE_TEXT: Record<string, string> = {
  BUY: "Buy",
  SELL: "Sell",
  DIVIDEND: "Dividend",
  DISTRIBUTION: "Distribution",
  DRIP: "DRIP",
  TRANSFER_IN: "Transfer in",
  TRANSFER_OUT: "Transfer out",
  INTEREST: "Interest",
  SPLIT: "Split",
  SYMBOL_CHANGE: "Symbol change",
  JOURNAL: "Journal",
  RETURN_OF_CAPITAL: "Return of capital",
  WITHHOLDING_TAX: "Withholding tax",
};

function LedgerLine({ r }: { r: LedgerRow }) {
  const qty = r.split_ratio ? `× ${trim(r.split_ratio)}` : r.quantity !== null ? quantity(r.quantity) : "";
  return (
    <tr>
      <td className="nowrap">{r.trade_date}</td>
      <td>{r.account.name}</td>
      <td>
        {TYPE_TEXT[r.type] ?? r.type}
        {r.corrected && <span className="tag">Corrected</span>}
      </td>
      <td className="num">{qty}</td>
      <td className="num">{r.price !== null ? trim(r.price) : ""}</td>
      <td className="num nowrap">
        {money(r.net_amount)} <span className="sub">{r.currency}</span>
      </td>
      <td>
        <Source r={r} />
      </td>
    </tr>
  );
}

function Source({ r }: { r: LedgerRow }) {
  if (r.origin === "opening_balance") return <>Opening balance</>;
  if (r.origin === "manual") return <>Manual</>;
  const where = `${r.source.file_name ?? "file"}, row ${r.source.row_no ?? "?"}`;
  if (!r.source.import_id) return <>{where}</>;
  return <a href={href({ page: "import", id: r.source.import_id })}>{where}</a>;
}
