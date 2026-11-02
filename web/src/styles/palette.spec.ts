// The palette's contrast, computed from the stylesheets themselves (Q-UI-88, Q-UI-96,
// docs/developer/design-language.md). jsdom paints nothing, so axe's `color-contrast` runs only in the
// browser tier; this test holds the accent pairs in the unit tier, where a colour change is made.
import mainScss from './main.scss' with { loader: 'text' };
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

/** Every `light-dark()` pair of mat.theme-overrides (as --mat-sys-*) and of the trove tokens. */
function readPalette(): Map<string, readonly [string, string]> {
  const vals = new Map<string, readonly [string, string]>();
  const overrides = mainScss.split('mat.theme-overrides(')[1]?.split(');')[0] ?? '';
  for (const m of overrides.matchAll(/^\s*([a-z-]+):\s*light-dark\((.+)\),\s*$/gm)) {
    const [light = '', dark = ''] = splitTop(m[2] ?? '');
    vals.set(`--mat-sys-${m[1]}`, [light, dark]);
  }
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
  if (!pair) throw new Error(`${name} is set neither in main.scss nor in _tokens.scss`);
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
  page: '--mat-sys-surface',
  raised: '--trove-surface-raised',
  'table head': '--trove-table-head-bg',
  'row hover': '--trove-row-hover',
  menu: '--mat-sys-surface-container',
  dialog: '--mat-sys-surface-container-high',
  'filled field': '--mat-sys-surface-variant',
  'selected row': '--mat-sys-secondary-container',
  'primary-container': '--mat-sys-primary-container',
  'inverse surface': '--mat-sys-inverse-surface',
  primary: '--mat-sys-primary',
  'side bar': '--trove-nav-bg',
  'open group': '--trove-nav-bg-nested',
  'top bar': '--trove-nav-bg-deep',
  'active entry': ['--trove-nav-active', '--trove-nav-bg'],
  'active entry in an open group': ['--trove-nav-active', '--trove-nav-bg-nested'],
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
  ['--mat-sys-primary', 'page'],
  ['--mat-sys-primary', 'raised'],
  ['--mat-sys-primary', 'table head'],
  ['--mat-sys-primary', 'row hover'],
  ['--mat-sys-primary', 'menu'],
  ['--mat-sys-primary', 'dialog'],
  ['--mat-sys-primary', 'filled field'],
  ['--mat-sys-on-primary', 'primary'],
  ['--trove-link', 'page'],
  ['--trove-link', 'raised'],
  ['--trove-link', 'table head'],
  ['--trove-link', 'row hover'],
  ['--trove-link', 'dialog'],
  ['--trove-link', 'selected row'],
  ['--mat-sys-on-secondary-container', 'selected row'],
  ['--mat-sys-on-surface', 'selected row'],
  ['--mat-sys-on-surface-variant', 'selected row'],
  ['--mat-sys-on-primary-container', 'primary-container'],
  ['--mat-sys-inverse-primary', 'inverse surface'],
  ['--mat-sys-on-surface', 'page'],
  ['--mat-sys-on-surface', 'raised'],
  ['--mat-sys-on-surface-variant', 'page'],
  ['--mat-sys-on-surface-variant', 'filled field'],
  ['--mat-sys-on-surface-variant', 'dialog'],
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
];

// Marks: WCAG 1.4.11, 3:1 (focus ring, tab underline, checkbox, the navigation's bar and icon).
const markPairs: readonly (readonly [string, string])[] = [
  ['--mat-sys-primary', 'page'],
  ['--mat-sys-primary', 'raised'],
  ['--mat-sys-primary', 'dialog'],
  ['--mat-sys-primary', 'filled field'],
  ['--trove-nav-accent', 'side bar'],
  ['--trove-nav-accent', 'open group'],
  ['--trove-nav-accent', 'top bar'],
  ['--trove-nav-accent', 'active entry'],
  ['--trove-nav-accent', 'active entry in an open group'],
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
    const ring = '2px solid var(--mat-sys-primary)';
    expect(palette.get('--trove-focus-ring')).toEqual([ring, ring]);
  });

  it('R-UI-002 the accent is blue, and green marks a positive status only (Q-UI-96)', () => {
    const isGreen = (c: Rgba) => {
      const { hue, saturation } = hueOf(c);
      return saturation > 0.15 && hue >= 75 && hue <= 165;
    };
    for (const scheme of ['light', 'dark'] as const) {
      for (const name of ['--mat-sys-primary', '--trove-link', '--trove-nav-accent']) {
        const { hue } = hueOf(colour(name, scheme));
        expect(hue, `${name} (${scheme})`).toBeGreaterThanOrEqual(200);
        expect(hue, `${name} (${scheme})`).toBeLessThanOrEqual(220);
      }
      expect(isGreen(colour('--trove-status-ok', scheme)), `--trove-status-ok (${scheme})`).toBe(true);
      const green: string[] = [];
      for (const name of palette.keys()) {
        if (name === '--trove-status-ok') continue;
        let c: Rgba;
        try {
          c = colour(name, scheme);
        } catch {
          continue; // a length, a shadow or a token Material generates
        }
        if (isGreen(c)) green.push(name);
      }
      expect(green, scheme).toEqual([]);
    }
  });
});
