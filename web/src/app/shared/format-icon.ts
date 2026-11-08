/**
 * The Material icon that stands for a format, in the package list's type column, on the package page and on the format
 * tiles of the repository wizard (R-UI-006, R-UI-021). The UI knows no list of formats (FMT-001): a
 * format without an icon of its own gets the neutral package glyph. OCI gets the cube of a container image.
 */
export function formatIcon(name: string): string {
  switch (name) {
    case 'nuget':
      return 'extension';
    case 'oci':
      return 'view_in_ar';
    default:
      return 'inventory_2';
  }
}
