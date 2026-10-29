const UNITS: readonly (readonly [unit: 'day' | 'hour' | 'minute' | 'second', seconds: number])[] = [
  ['day', 86_400],
  ['hour', 3600],
  ['minute', 60],
  ['second', 1],
];

/**
 * A duration in the largest unit that divides it evenly ("90 days", "90 Tage"), formatted with
 * `Intl` in the locale of the running build (R-UI-045), so no plural messages are needed.
 */
export function formatDuration(seconds: number, locale: string): string {
  const [unit, size] = UNITS.find(([, s]) => seconds % s === 0 && seconds >= s) ?? UNITS[3]!;
  return new Intl.NumberFormat(locale, { style: 'unit', unit, unitDisplay: 'long' }).format(seconds / size);
}

/** Seconds as a day count for a number field ("90", "0.5"). */
export function toDays(seconds: number): string {
  return String(Math.round((seconds / 86_400) * 1000) / 1000);
}

/** A day count from a number field as whole seconds, or NaN for an unusable entry. */
export function fromDays(days: string): number {
  const n = Number(days.trim());
  return days.trim() === '' || !Number.isFinite(n) ? Number.NaN : Math.round(n * 86_400);
}
