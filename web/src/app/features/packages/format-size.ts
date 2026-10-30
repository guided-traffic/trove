const UNITS = ['B', 'KiB', 'MiB', 'GiB', 'TiB'] as const;

/** A size in bytes in binary units, one decimal from KiB up (`47.1 KiB`); the unit is not translated. */
export function formatSize(bytes: number): string {
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < UNITS.length - 1) {
    value /= 1024;
    unit++;
  }
  return unit === 0 ? `${value} B` : `${value.toFixed(1)} ${UNITS[unit]}`;
}
