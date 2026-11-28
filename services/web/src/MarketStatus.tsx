import { useQuery } from "@tanstack/react-query";
import { api, type MarketState, type MarketStatus } from "./api";
import { formatTime } from "./ImportsPage";
import { href } from "./route";

// The status card (FR-6, AC-12): last successful refresh, next one, and a state that distinguishes Success,
// Partial, Failed and Stale; an unreachable service or one turned off is said so, never shown as up to date (NFR-4).
export const STATE_TEXT: Record<MarketState, string> = {
  success: "Up to date",
  partial: "Partial: some prices could not be refreshed",
  failed: "Refresh failed",
  stale: "Some prices are stale",
  off: "Market data is turned off",
  unavailable: "Market data service is not reachable",
  never: "No refresh has run yet",
};

export function useMarketStatus() {
  return useQuery({ queryKey: ["market-status"], queryFn: api.marketStatus, refetchInterval: 60_000 });
}

export function MarketStatusCard({ link = true }: { link?: boolean }) {
  const q = useMarketStatus();
  if (q.isPending) return <p className="status">Loading market data status…</p>;
  if (q.error)
    return (
      <p className="error" role="alert">
        Market data status could not be loaded: {q.error.message}
      </p>
    );
  return <StatusBody s={q.data} link={link} />;
}

function StatusBody({ s, link }: { s: MarketStatus; link: boolean }) {
  const problems = s.stale.length + s.unpriced.length;
  return (
    <section className={`card market-card state-${s.state}`} aria-label="Market data status">
      <p className="market-state">
        <strong>{STATE_TEXT[s.state]}</strong>
      </p>
      <dl className="totals">
        <div>
          <dt>Last successful refresh</dt>
          <dd>{s.last_success_at ? formatTime(s.last_success_at) : <span className="unknown">Never</span>}</dd>
        </div>
        <div>
          <dt>Market</dt>
          <dd>
            {s.market_open === null
              ? "Unknown"
              : s.market_open
                ? "Open"
                : `Market closed - last close ${s.last_close_date ?? "unknown"}`}
          </dd>
        </div>
        <div>
          <dt>Next refresh</dt>
          <dd>{s.next_run_at ? formatTime(s.next_run_at) : <span className="sub">None scheduled</span>}</dd>
        </div>
        <div>
          <dt>Prices</dt>
          <dd>
            {s.stale.length === 0 && s.unpriced.length === 0 ? (
              "All held securities priced"
            ) : (
              <>
                {s.stale.length > 0 && <div className="unknown">{s.stale.length} stale</div>}
                {s.unpriced.length > 0 && <div className="unknown">{s.unpriced.length} without a price</div>}
              </>
            )}
          </dd>
        </div>
      </dl>
      {link && (problems > 0 || s.state !== "success") && (
        <p className="sub">
          <a href={href({ page: "market" })}>Market data details</a>
        </p>
      )}
    </section>
  );
}
