// Shared pieces of the holdings and security pages (D-38): unknown values and flags are visible conditions, never 0.
import type { HeldSecurity, PositionFlag } from "./api";
import { money, unitCost } from "./format";
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

/** Market value until M4: "No price", with the reason on hover; nothing for a closed position. */
export function MarketValue({ value, reason }: { value: string | null; reason: string }) {
  if (value !== null) return <>{money(value)}</>;
  if (!reason) return <span className="sub">–</span>;
  return (
    <span className="unknown" title="Market data arrives with M4; values without a price are left out of totals.">
      No price
    </span>
  );
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
