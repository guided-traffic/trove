const UNITS: readonly (readonly [Intl.RelativeTimeFormatUnit, number])[] = [
  ['year', 365 * 86_400_000],
  ['month', 30 * 86_400_000],
  ['week', 7 * 86_400_000],
  ['day', 86_400_000],
  ['hour', 3_600_000],
  ['minute', 60_000],
];

/**
 * A point in time relative to now in the viewer's language ("3 days ago"), for list cells whose
 * absolute time sits in the `<time>` element's `datetime` and title (R-UI-006). A time less than a
 * minute away reads "now". The locale is the one of the build (`LOCALE_ID`).
 */
export function relativeTime(iso: string, locale: string, now: number = Date.now()): string {
  const diff = Date.parse(iso) - now;
  const format = new Intl.RelativeTimeFormat(locale, { numeric: 'auto' });
  for (const [unit, ms] of UNITS) {
    if (Math.abs(diff) >= ms) return format.format(Math.trunc(diff / ms), unit);
  }
  return format.format(0, 'minute');
}
