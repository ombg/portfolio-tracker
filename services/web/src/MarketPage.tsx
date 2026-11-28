import { MarketStatusCard, useMarketStatus } from "./MarketStatus";
import { formatTime } from "./ImportsPage";
import { securityLabel } from "./holdingsUi";
import { trim } from "./format";
import { href } from "./route";

const KIND_TEXT = { intraday: "Intraday", close: "Close", backfill: "History" } as const;
const RUN_TEXT = { running: "Running", success: "Success", partial: "Partial", failed: "Failed" } as const;

// Market data (FR-6, AC-12): the status card, the caller's stale and unpriced securities, and recent runs with
// their errors for the caller's securities. Overrides follow in #29.
export function MarketPage() {
  const q = useMarketStatus();
  const s = q.data;
  return (
    <section>
      <h1>Market data</h1>
      <MarketStatusCard link={false} />
      {s && s.stale.length > 0 && (
        <div className="card">
          <h2>Stale prices</h2>
          <table aria-label="Stale prices">
            <thead>
              <tr>
                <th scope="col">Security</th>
                <th scope="col" className="num">Last price</th>
                <th scope="col">Observed</th>
                <th scope="col">Source</th>
              </tr>
            </thead>
            <tbody>
              {s.stale.map((x) => (
                <tr key={x.security.id}>
                  <td>
                    <a href={href({ page: "security", id: x.security.id })}>{securityLabel(x.security)}</a>
                  </td>
                  <td className="num">
                    {trim(x.price)} <span className="sub">{x.currency}</span>
                  </td>
                  <td className="unknown">{formatTime(x.observed_at)}</td>
                  <td>{x.source}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {s && s.unpriced.length > 0 && (
        <div className="card">
          <h2>Without a price</h2>
          <p className="sub">
            These holdings have no price from the provider yet, so they are left out of market values and totals.
          </p>
          <ul aria-label="Without a price">
            {s.unpriced.map((x) => (
              <li key={x.id}>
                <a href={href({ page: "security", id: x.id })}>{securityLabel(x)}</a> <span className="sub">{x.name}</span>
              </li>
            ))}
          </ul>
        </div>
      )}
      {s && (
        <div className="card">
          <h2>Recent runs</h2>
          {s.runs.length === 0 ? (
            <p className="status">No runs yet.</p>
          ) : (
            <table aria-label="Recent runs">
              <thead>
                <tr>
                  <th scope="col">Started</th>
                  <th scope="col">Kind</th>
                  <th scope="col">Status</th>
                  <th scope="col">Your securities that failed</th>
                </tr>
              </thead>
              <tbody>
                {s.runs.map((r, i) => (
                  <tr key={i}>
                    <td className="nowrap">{r.started_at ? formatTime(r.started_at) : ""}</td>
                    <td>{KIND_TEXT[r.kind]}</td>
                    <td className={`run-${r.status}`}>{RUN_TEXT[r.status]}</td>
                    <td>
                      {Object.keys(r.errors).length === 0
                        ? "None"
                        : Object.entries(r.errors)
                            .map(([sym, msg]) => `${sym}: ${msg}`)
                            .join("; ")}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      )}
    </section>
  );
}
