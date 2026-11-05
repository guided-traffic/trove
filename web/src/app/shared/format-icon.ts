/**
 * The Material icon that stands for a format, in the package list's type column and on the format
 * tiles of the repository wizard (R-UI-006, R-UI-021). The UI knows no list of formats (FMT-001): a
 * format without an icon of its own gets the neutral package glyph.
 */
export function formatIcon(name: string): string {
  return name === 'nuget' ? 'extension' : 'inventory_2';
}
