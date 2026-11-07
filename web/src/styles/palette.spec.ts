// The palette's contrast, computed from the stylesheets themselves (Q-UI-88, Q-UI-96,
// docs/developer/design-language.md). jsdom paints nothing, so axe's `color-contrast` runs only in the
// browser tier; this test holds the accent pairs in the unit tier, where a colour change is made.
import tokensScss from './_tokens.scss' with { loader: 'text' };

type Scheme = 'light' | 'dark';
type Rgba = readonly [number, number, number, number];

/** Splits at the commas that are not inside parentheses: `rgb(1 2 3 / 4%), #fff`. */
function splitTop(s: string): string[] {
  const out: string[] = [];
  let depth = 0;
  let cur = '';
  for (const ch of s) {
    if (ch === '(') depth++;
    if (ch === ')') depth--;
    if (ch === ',' && depth === 0) {
      out.push(cur.trim());
      cur = '';
    } else {
      cur += ch;
    }
  }
  out.push(cur.trim());
  return out;
}

/** Every `light-dark()` pair of the trove tokens. */
function readPalette(): Map<string, readonly [string, string]> {
  const vals = new Map<string, readonly [string, string]>();
  const root = tokensScss.split(':root {')[1]?.split('\n}')[0] ?? '';
  for (const m of root.matchAll(/^\s*(--trove-[a-z0-9-]+):\s*(.+);\s*$/gm)) {
    const v = (m[2] ?? '').trim();
    const pair = /^light-dark\((.+)\)$/.exec(v);
    const [light = '', dark = ''] = pair ? splitTop(pair[1] ?? '') : [v, v];
    vals.set(m[1] ?? '', [light, dark]);
  }
  return vals;
}

const palette = readPalette();

function colour(name: string, scheme: Scheme): Rgba {
  const pair = palette.get(name);
  if (!pair) throw new Error(`${name} is not set in _tokens.scss`);
  return parse(scheme === 'light' ? pair[0] : pair[1], scheme);
}

function parse(v: string, scheme: Scheme): Rgba {
  const ref = /var\((--[a-z0-9-]+)\)/.exec(v);
  if (ref?.[1] && !v.startsWith('#')) return colour(ref[1], scheme);
  const hex = /^#([0-9a-f]{2})([0-9a-f]{2})([0-9a-f]{2})$/i.exec(v);
  if (hex) return [parseInt(hex[1] ?? '', 16), parseInt(hex[2] ?? '', 16), parseInt(hex[3] ?? '', 16), 1];
  const rgb = /^rgb\((\d+) (\d+) (\d+) \/ (\d+)%\)$/.exec(v);
  if (rgb) return [Number(rgb[1]), Number(rgb[2]), Number(rgb[3]), Number(rgb[4]) / 100];
  throw new Error(`not a colour: ${v}`);
}

/** A translucent colour over an opaque one, composited in sRGB as a browser paints it. */
function over(fg: Rgba, bg: Rgba): Rgba {
  const a = fg[3];
  const mix = (i: 0 | 1 | 2) => a * fg[i] + (1 - a) * bg[i];
  return [mix(0), mix(1), mix(2), 1];
}

function luminance(c: Rgba): number {
  const ch = (x: number) => {
    const s = x / 255;
    return s <= 0.04045 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
  };
  return 0.2126 * ch(c[0]) + 0.7152 * ch(c[1]) + 0.0722 * ch(c[2]);
}

function ratio(a: Rgba, b: Rgba): number {
  const [la, lb] = [luminance(a), luminance(b)];
  return (Math.max(la, lb) + 0.05) / (Math.min(la, lb) + 0.05);
}

/** Hue in degrees and HSL saturation. */
function hueOf(c: Rgba): { hue: number; saturation: number } {
  const [r, g, b] = [c[0] / 255, c[1] / 255, c[2] / 255];
  const max = Math.max(r, g, b);
  const min = Math.min(r, g, b);
  const l = (max + min) / 2;
  const d = max - min;
  if (d === 0) return { hue: 0, saturation: 0 };
  const saturation = d / (1 - Math.abs(2 * l - 1));
  const h = max === r ? ((g - b) / d) % 6 : max === g ? (b - r) / d + 2 : (r - g) / d + 4;
  return { hue: (h * 60 + 360) % 360, saturation };
}

// The surfaces an accent sits on; a pair of names is a translucent fill over a surface.
const surfaces: Record<string, string | readonly [string, string]> = {
  page: '--trove-surface',
  raised: '--trove-surface-raised',
  'table head': '--trove-table-head-bg',
  'row hover': '--trove-row-hover',
  menu: '--trove-surface-container',
  dialog: '--trove-dialog-bg',
  'filled field': '--trove-surface-variant',
  'selected row': '--trove-accent-container',
  'primary-container': '--trove-accent-container',
  'inverse surface': '--trove-inverse-surface',
  primary: '--trove-accent',
  'side bar': '--trove-nav-bg',
  'open group': '--trove-nav-bg-nested',
  'top bar': '--trove-nav-bg-deep',
  'active entry': ['--trove-nav-active', '--trove-nav-bg'],
  'active entry in an open group': ['--trove-nav-active', '--trove-nav-bg-nested'],
  // trove's own controls (Q-UI-126, _controls.scss).
  'accent fill': '--trove-accent-fill',
  'accent fill hover': '--trove-accent-fill-hover',
  'default button': ['--trove-btn-default-bg', '--trove-surface'],
  'default button hover': ['--trove-btn-default-bg-hover', '--trove-surface'],
  'tertiary button hover': ['--trove-btn-tertiary-bg-hover', '--trove-surface'],
  'danger fill': '--trove-danger-fill',
  control: ['--trove-control-bg', '--trove-surface'],
  // The reference's translucent fills, measured over what they sit on (Q-UI-128): the default button on the page and on
  // the frame (the Admin button), a menu item under the pointer, the search field on the frame, a card's strong fill.
  'default button on the frame': ['--trove-btn-default-bg', '--trove-nav-bg'],
  'default button hover on the frame': ['--trove-btn-default-bg-hover', '--trove-nav-bg'],
  'top bar hover': ['--trove-nav-hover', '--trove-nav-bg'],
  'menu item hover': ['--trove-menu-hover', '--trove-menu-bg'],
  // The panel of the project switcher (Q-UI-129): the menu's fill.
  'switcher panel': '--trove-menu-bg',
  // The badges (Q-UI-128): each status's pill.
  'neutral badge': '--trove-badge-neutral-bg',
  'positive badge': '--trove-badge-ok-bg',
  'warning badge': '--trove-badge-warn-bg',
  'error badge': '--trove-badge-bad-bg',
  'info badge': '--trove-badge-info-bg',
  search: ['--trove-search-bg', '--trove-nav-bg'],
  card: '--trove-card-bg',
};

function surface(name: string, scheme: Scheme): Rgba {
  const s = surfaces[name];
  if (s === undefined) throw new Error(`unknown surface ${name}`);
  return typeof s === 'string' ? colour(s, scheme) : over(colour(s[0], scheme), colour(s[1], scheme));
}

function measure(fg: string, on: string, scheme: Scheme): number {
  const bg = surface(on, scheme);
  let f = colour(fg, scheme);
  if (f[3] < 1) f = over(f, bg);
  return ratio(f, bg);
}

// Text: WCAG 1.4.3, 4.5:1. The same pairs as the table of design-language.md.
const textPairs: readonly (readonly [string, string])[] = [
  ['--trove-accent', 'page'],
  ['--trove-accent', 'raised'],
  ['--trove-accent', 'table head'],
  ['--trove-accent', 'row hover'],
  ['--trove-accent', 'menu'],
  ['--trove-accent', 'dialog'],
  ['--trove-accent', 'filled field'],
  ['--trove-on-accent', 'primary'],
  ['--trove-link', 'page'],
  ['--trove-link', 'raised'],
  ['--trove-link', 'table head'],
  ['--trove-link', 'row hover'],
  ['--trove-link', 'dialog'],
  ['--trove-link', 'selected row'],
  ['--trove-on-accent-container', 'selected row'],
  ['--trove-on-surface', 'selected row'],
  ['--trove-text-muted', 'selected row'],
  ['--trove-on-accent-container', 'primary-container'],
  ['--trove-inverse-accent', 'inverse surface'],
  ['--trove-on-surface', 'page'],
  ['--trove-on-surface', 'raised'],
  ['--trove-text-muted', 'page'],
  ['--trove-text-muted', 'filled field'],
  ['--trove-text-muted', 'dialog'],
  ['--trove-status-bad', 'page'],
  ['--trove-status-bad', 'filled field'],
  ['--trove-status-warn', 'page'],
  ['--trove-status-warn', 'filled field'],
  ['--trove-status-ok', 'page'],
  ['--trove-status-ok', 'raised'],
  ['--trove-status-ok', 'dialog'],
  ['--trove-status-ok', 'selected row'],
  ['--trove-nav-active-fg', 'active entry'],
  ['--trove-nav-active-fg', 'active entry in an open group'],
  // trove's own controls (Q-UI-126): the label of each button on its fill, at rest and under the pointer, and the
  // text and placeholder of a field.
  ['--trove-on-accent', 'accent fill'],
  ['--trove-on-accent', 'accent fill hover'],
  ['--trove-control-fg', 'default button'],
  ['--trove-control-fg', 'default button hover'],
  ['--trove-control-fg', 'tertiary button hover'],
  ['--trove-control-fg', 'page'],
  ['--trove-on-danger', 'danger fill'],
  ['--trove-control-fg', 'control'],
  ['--trove-control-placeholder', 'control'],
  // The frame and its controls (Q-UI-128): the Admin button, the hover of the top bar and the sidebar, menus, the
  // search field, the sidebar's text and a card's title and muted text.
  ['--trove-control-fg', 'default button on the frame'],
  ['--trove-control-fg', 'default button hover on the frame'],
  ['--trove-nav-fg', 'side bar'],
  ['--trove-nav-fg', 'top bar hover'],
  ['--trove-control-fg', 'menu item hover'],
  // The project switcher (Q-UI-129): the projects on the menu fill, and the headings and keys in the muted tone of the frame.
  ['--trove-control-fg', 'switcher panel'],
  ['--trove-nav-fg-muted', 'switcher panel'],
  ['--trove-search-fg', 'search'],
  ['--trove-search-muted', 'search'],
  ['--trove-heading', 'card'],
  ['--trove-text-muted', 'card'],
  // A badge's text on its pill (Q-UI-128).
  ['--trove-badge-neutral-fg', 'neutral badge'],
  ['--trove-badge-ok-fg', 'positive badge'],
  ['--trove-badge-warn-fg', 'warning badge'],
  ['--trove-badge-bad-fg', 'error badge'],
  ['--trove-badge-info-fg', 'info badge'],
];

// Marks: WCAG 1.4.11, 3:1 (focus ring, tab underline, checkbox, the navigation's bar and icon).
const markPairs: readonly (readonly [string, string])[] = [
  ['--trove-accent', 'page'],
  ['--trove-accent', 'raised'],
  ['--trove-accent', 'dialog'],
  ['--trove-accent', 'filled field'],
  ['--trove-nav-accent', 'side bar'],
  ['--trove-nav-accent', 'open group'],
  ['--trove-nav-accent', 'top bar'],
  ['--trove-nav-accent', 'active entry'],
  ['--trove-nav-accent', 'active entry in an open group'],
  // The search field's outline against the frame it sits on (Q-UI-127).
  ['--trove-search-border', 'top bar'],
  // The outline of a field against the page and its own fill, and the primary button against the page (Q-UI-126).
  ['--trove-control-border', 'page'],
  ['--trove-control-border', 'control'],
  ['--trove-accent-fill', 'page'],
  // The current tab's line on the page, a badge's icon on its pill (Q-UI-128).
  ['--trove-tab-indicator', 'page'],
  ['--trove-badge-neutral-icon', 'neutral badge'],
  ['--trove-badge-ok-icon', 'positive badge'],
  ['--trove-badge-warn-icon', 'warning badge'],
  ['--trove-badge-bad-icon', 'error badge'],
  ['--trove-badge-info-icon', 'info badge'],
  // The marks of the vulnerability trend (R-VULN-022), on the page and on the raised surface the chart sits on.
  ...['critical', 'high', 'medium', 'low', 'none', 'unknown'].flatMap((s): [string, string][] => [
    [`--trove-chart-${s}`, 'page'],
    [`--trove-chart-${s}`, 'raised'],
  ]),
];

function failures(pairs: readonly (readonly [string, string])[], min: number): string[] {
  const out: string[] = [];
  for (const [fg, on] of pairs) {
    for (const scheme of ['light', 'dark'] as const) {
      const r = measure(fg, on, scheme);
      if (r < min) out.push(`${fg} on ${on} (${scheme}): ${r.toFixed(2)}:1`);
    }
  }
  return out;
}

describe('palette', () => {
  it('R-UI-044 the accent, the link and their neighbours read as text at 4.5:1 on every surface they sit on, in both schemes', () => {
    expect(failures(textPairs, 4.5)).toEqual([]);
  });

  it('R-UI-044 the focus ring and the accent marks keep 3:1 against what is next to them (WCAG 1.4.11)', () => {
    expect(failures(markPairs, 3)).toEqual([]);
  });

  it('R-UI-044 the focus ring is the accent', () => {
    const ring = '2px solid var(--trove-accent)';
    expect(palette.get('--trove-focus-ring')).toEqual([ring, ring]);
  });

  it('R-UI-002 the accent is blue, and green marks a positive status only (Q-UI-96)', () => {
    const isGreen = (c: Rgba) => {
      const { hue, saturation } = hueOf(c);
      return saturation > 0.15 && hue >= 75 && hue <= 165;
    };
    for (const scheme of ['light', 'dark'] as const) {
      for (const name of ['--trove-accent', '--trove-link', '--trove-nav-accent']) {
        const { hue } = hueOf(colour(name, scheme));
        expect(hue, `${name} (${scheme})`).toBeGreaterThanOrEqual(200);
        expect(hue, `${name} (${scheme})`).toBeLessThanOrEqual(220);
      }
      expect(isGreen(colour('--trove-status-ok', scheme)), `--trove-status-ok (${scheme})`).toBe(true);
      const green: string[] = [];
      for (const name of palette.keys()) {
        // The positive status, as text or as its badge (Q-UI-128).
        if (name === '--trove-status-ok' || name.startsWith('--trove-badge-ok-')) continue;
        let c: Rgba;
        try {
          c = colour(name, scheme);
        } catch {
          continue; // a length, a shadow or another token that is no colour
        }
        if (isGreen(c)) green.push(name);
      }
      expect(green, scheme).toEqual([]);
    }
  });
});
