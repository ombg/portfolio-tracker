// Shared pieces of the holdings and security pages (D-38): unknown values and flags are visible conditions, never 0.
import type { HeldSecurity, MarketValueReason, PositionFlag, Valuation } from "./api";
import { money, percent, trim, unitCost } from "./format";
import { formatTime } from "./ImportsPage";
import { href } from "./route";

export const FLAGS: Record<string, { label: string; explain: string }> = {
  negative_holding: {
    label: "Missing history",
    explain:
      "More shares left this account than the ledger shows it holding, so part of its history is missing. " +
      "Its cost and value are left out until a fuller export or an opening position fills the gap.",
  },
  fx_missing: {
    label: "FX rate missing",
    explain:
      "A USD amount has no exchange rate in the export, so its CAD value is unknown. " +
      "Market data (M4) adds Bank of Canada rates.",
  },
  cost_unknown: {
    label: "Cost unknown",
    explain:
      "Shares arrived without a value (a transfer in), so their cost is unknown. " +
      "Market data (M4) values them on the transfer date.",
  },
  estimated_cost: {
    label: "Estimated cost",
    explain: "The cost is the market value on the opening date, not the real purchase cost (D-31).",
  },
  unsupported_event: {
    label: "Unsupported event",
    explain: "A merger or spin-off is not calculated yet, so the cost is unknown.",
  },
};

export function flagInfo(code: string) {
  return FLAGS[code] ?? { label: code, explain: code };
}

export function FlagTags({ codes }: { codes: string[] }) {
  return (
    <>
      {codes.map((c) => (
        <span key={c} className="tag flag" title={flagInfo(c).explain}>
          {flagInfo(c).label}
        </span>
      ))}
    </>
  );
}

export function flagCodes(flags: PositionFlag[]): string[] {
  return flags.map((f) => f.code);
}

/** A money value, or "Unknown" when nobody knows it yet. */
export function Amount({ value, currency }: { value: string | null; currency?: string }) {
  if (value === null) return <span className="unknown">Unknown</span>;
  return (
    <>
      {money(value)}
      {currency && <span className="sub"> {currency}</span>}
    </>
  );
}

/** A per-share cost; a position with nothing held has no average ("–"), which is not the same as unknown. */
export function UnitCost({ value, currency, held = true }: { value: string | null; currency?: string; held?: boolean }) {
  if (!held) return <span className="sub">–</span>;
  if (value === null) return <span className="unknown">Unknown</span>;
  return (
    <>
      {unitCost(value)}
      {currency && <span className="sub"> {currency}</span>}
    </>
  );
}

const REASON_TEXT: Record<Exclude<MarketValueReason, "">, { label: string; explain: string }> = {
  no_price: { label: "No price", explain: "The provider has no price for this security yet; it is left out of totals." },
  no_fx: { label: "No FX rate", explain: "There is no rate to CAD for its price currency yet; it is left out of totals." },
  missing_history: {
    label: "Missing history",
    explain: "More shares left than the ledger holds, so the position is not valued until the history is complete.",
  },
};

/** Where a market value comes from (NFR-2, FR-6): price, source and time, and the FX rate used. */
export function basisText(v: Valuation): string {
  const price =
    v.price_source === "principal"
      ? "valued at principal (GIC)"
      : `price ${trim(v.price)} ${v.price_currency} from ${v.price_source}` +
        (v.price_observed_at ? ` at ${formatTime(v.price_observed_at)}` : "") +
        (v.stale ? " (stale)" : "");
  const fx = v.fx_rate
    ? `; FX ${trim(v.fx_rate)} ${v.price_currency}/CAD from ${v.fx_source}` +
      (v.fx_observed_at ? ` at ${formatTime(v.fx_observed_at)}` : "")
    : "";
  return price + fx;
}

/** A market value in CAD with its basis on hover; amber and tagged when stale; the reason when there is none. */
export function MarketValue(props: { value: string | null; reason: MarketValueReason; valuation: Valuation | null }) {
  const { value, reason, valuation } = props;
  if (value !== null) {
    const stale = valuation?.stale === true;
    return (
      <span className={stale ? "unknown" : undefined} title={valuation ? basisText(valuation) : undefined}>
        {money(value)}
        {stale && <span className="tag flag">Stale</span>}
      </span>
    );
  }
  if (!reason) return <span className="sub">–</span>;
  return (
    <span className="unknown" title={REASON_TEXT[reason].explain}>
      {REASON_TEXT[reason].label}
    </span>
  );
}

/** Unrealized gain: only where a market value exists; "–" otherwise (the market value cell says why). */
export function Unrealized({ value }: { value: string | null }) {
  return value === null ? <span className="sub">–</span> : <>{money(value)}</>;
}

export function Weight({ value }: { value: string | null }) {
  return value === null ? <span className="sub">–</span> : <>{percent(value)}</>;
}

export function securityLabel(s: HeldSecurity): string {
  return s.symbol ? `${s.symbol} (${s.exchange})` : s.name;
}

export function SecurityLink({ security }: { security: HeldSecurity }) {
  return (
    <>
      <a href={href({ page: "security", id: security.id })}>{securityLabel(security)}</a>
      {security.symbol && <div className="sub">{security.name}</div>}
    </>
  );
}
