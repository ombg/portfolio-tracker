// Decimal display without floats (CLAUDE.md rule 3): the API sends decimals as strings; these helpers round and
// group them with BigInt arithmetic. null means unknown and is never shown as 0 (NFR-4); callers decide the words.

/** Rounds a decimal string to `places` (half away from zero) and returns it with exactly that many places. */
export function roundDecimal(value: string, places: number): string {
  const m = /^(-?)(\d*)(?:\.(\d*))?$/.exec(value.trim());
  if (!m) return value;
  const [, sign, intPart, frac = ""] = m;
  const digits = (intPart || "0") + frac.padEnd(places + 1, "0").slice(0, places + 1);
  let n = BigInt(digits);
  n = n / 10n + (n % 10n >= 5n ? 1n : 0n); // the extra digit decides the rounding
  let s = n.toString().padStart(places + 1, "0");
  const whole = s.slice(0, s.length - places) || "0";
  const part = places > 0 ? "." + s.slice(s.length - places) : "";
  s = whole + part;
  return n === 0n ? s : sign + s;
}

/** Groups thousands in the integer part: 1234567.5 -> 1,234,567.5 */
export function group(value: string): string {
  const [whole, part] = value.split(".");
  const sign = whole.startsWith("-") ? "-" : "";
  const grouped = whole.replace("-", "").replace(/\B(?=(\d{3})+(?!\d))/g, ",");
  return sign + grouped + (part !== undefined ? "." + part : "");
}

/** Removes trailing fractional zeros: 10.500 -> 10.5, 3.000 -> 3 */
export function trim(value: string): string {
  return value.includes(".") ? value.replace(/\.?0+$/, "") : value;
}

/** Money with two places and grouping, e.g. 1,499.10. */
export function money(value: string): string {
  return group(roundDecimal(value, 2));
}

/** A per-share cost: four places, trailing zeros beyond two removed (5.3566, 20.00). */
export function unitCost(value: string): string {
  const r = roundDecimal(value, 4);
  const [whole, part] = r.split(".");
  const kept = part.replace(/0+$/, "").padEnd(2, "0");
  return group(`${whole}.${kept}`);
}

/** A quantity as exact as the ledger keeps it, grouped: 1,250.5 */
export function quantity(value: string): string {
  return group(trim(value));
}

/** A fraction as a percentage with two places: 0.190840 -> 19.08% */
export function percent(fraction: string): string {
  const m = /^(-?)(\d*)(?:\.(\d*))?$/.exec(fraction.trim());
  if (!m) return fraction;
  const [, sign, whole, frac = ""] = m;
  const shifted = `${sign}${whole || "0"}${frac.padEnd(2, "0").slice(0, 2)}.${frac.slice(2) || "0"}`;
  return `${roundDecimal(shifted, 2)}%`;
}
