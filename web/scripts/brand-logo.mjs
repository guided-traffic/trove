// Generates trove's logo as SVG files into web/public/brand/ (run: node scripts/brand-logo.mjs).
//
// The mark is the "v" of trove drawn as a flow: the left arm is one solid band, the package coming in once
// (an upload from CI, or the first fetch through a proxy); the point at the bottom is trove, the store; the
// right arm is the same band split into lanes that fan out, the requests trove serves from then on.
//
// The wordmark is outlined paths of Inter Bold (no <text>, so it looks the same on every platform), and the
// favicon is a reduced form of the mark (two lanes, thicker strokes) on a dark tile, as SVG and as an ICO whose
// PNG frames this script rasterises itself.
//
// Work in progress with the owner: the candidate and the alternatives below are drafts, shown on the sign-in
// page in development builds only (features/login/logo-preview.ts). Once one is chosen, the drafts directory
// and the preview go.
import { Buffer } from 'node:buffer';
import { mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { crc32, deflateSync } from 'node:zlib';
import opentype from 'opentype.js';

const webDir = join(fileURLToPath(new URL('.', import.meta.url)), '..');
const outDir = join(webDir, 'public', 'brand');
const draftsDir = join(outDir, 'drafts');

// ---- Geometry ----------------------------------------------------------------------------------

const r = (n) => Math.round(n * 100) / 100;
const sub = (a, b) => [a[0] - b[0], a[1] - b[1]];
const add = (a, b) => [a[0] + b[0], a[1] + b[1]];
const mul = (a, k) => [a[0] * k, a[1] * k];
const norm = (a) => {
  const l = Math.hypot(a[0], a[1]);
  return [a[0] / l, a[1] / l];
};
const perp = (a) => [-a[1], a[0]];
const pt = (p) => `${r(p[0])} ${r(p[1])}`;
/** p turned by deg degrees around c; positive turns clockwise on screen (y points down), i.e. to the right. */
const turn = (p, c, deg) => {
  const a = (deg * Math.PI) / 180;
  const v = sub(p, c);
  return add(c, [v[0] * Math.cos(a) - v[1] * Math.sin(a), v[0] * Math.sin(a) + v[1] * Math.cos(a)]);
};
/** x where the line through p with direction d crosses y. */
const xAt = (p, d, y) => p[0] + ((y - p[1]) / d[1]) * d[0];

/** The point of a cubic at t. */
const cubicAt = (p0, p1, p2, p3, t) => {
  const u = 1 - t;
  return [0, 1].map(
    (k) => u * u * u * p0[k] + 3 * u * u * t * p1[k] + 3 * u * t * t * p2[k] + t * t * t * p3[k],
  );
};
/** Splits a cubic at t (de Casteljau): the control points of both halves. */
function splitCubic(p0, p1, p2, p3, t) {
  const lerp = (a, b) => [a[0] + (b[0] - a[0]) * t, a[1] + (b[1] - a[1]) * t];
  const a = lerp(p0, p1),
    b = lerp(p1, p2),
    c = lerp(p2, p3);
  const d = lerp(a, b),
    e = lerp(b, c),
    m = lerp(d, e);
  return [
    [p0, a, d, m],
    [m, e, c, p3],
  ];
}

/**
 * The lanes and the band of one mark in a 256 × 256 box, centred. Parameters:
 * W width of the lane bundle where it leaves the band, band the width of the band itself, n lanes, gap between lanes,
 * fan (how far the lanes spread at the top, 1 = parallel), under, share and follow (a lane that starts beneath another),
 * bend (degrees per lane: from the middle of its visible height on, the lane curves this far to the right),
 * mode 'fan' (straight lanes) or 'swoosh' (lanes leave the band along its direction and bend up).
 */
function markGeometry(p) {
  const { W, n, gap, fan, mode, top } = p;
  const band = p.band ?? W;
  const L = [60, top];
  const B = [124, 206];
  const R = [188, top];
  const uL = norm(sub(B, L));
  const uR = norm(sub(R, B));
  const nL = perp(uL);
  const nR = perp(uR);
  const w = (W - (n - 1) * gap) / n;
  const ext = 80;

  // The lanes start inside the band's rounded end: their start offsets shrink with the band, so no start shows beside it.
  const spread = Math.min(0.6, (band / 2 - w / 2) / (W / 2 - w / 2));
  const lanes = [];
  const starts = [];
  let right = -Infinity;
  for (let i = 0; i < n; i++) {
    const o = -W / 2 + w / 2 + i * (w + gap);
    const bend = p.bend?.[i] ?? 0;
    let p0, p1, p2, p3;
    if (mode === 'swoosh') {
      p0 = add(B, mul(nR, o * spread));
      p1 = add(p0, mul(uL, 18 + i * 6));
      p2 = add(add(B, mul(uR, 70)), mul(nR, o * (1 + (fan - 1) * 0.4)));
      p3 = add(add(R, mul(uR, ext)), mul(nR, o * fan));
      // A lane that starts under another (under[i] = its index) leaves from that lane's start and along its first
      // stretch; `share` (0 to 1) is how far it runs hidden beneath before it turns out to its own place.
      const under = p.under?.[i];
      if (under !== undefined) {
        const [q0, q1, q2, q3] = starts[under];
        const k = p.share ?? 0;
        p0 = q0;
        p1 = q1;
        p2 = add(mul(q2, k), mul(p2, 1 - k));
        // follow: the lane takes the other lane's whole path, so it shows only where its bend leaves it.
        if (p.follow) [p2, p3] = [q2, q3];
      }
      starts[i] = [p0, p1, p2, p3];
    } else {
      p0 = add(B, mul(nR, o));
      p3 = add(add(R, mul(uR, ext)), mul(nR, o * fan));
      p1 = add(p0, mul(sub(p3, p0), 1 / 3));
      p2 = add(p0, mul(sub(p3, p0), 2 / 3));
    }
    let d;
    if (bend) {
      // The parameter at half the visible height, found by bisection (y falls along the lane).
      const yHalf = (p0[1] + top) / 2;
      let lo = 0,
        hi = 1;
      for (let k = 0; k < 40; k++) {
        const t = (lo + hi) / 2;
        if (cubicAt(p0, p1, p2, p3, t)[1] > yHalf) lo = t;
        else hi = t;
      }
      const [[, a, b, m], [, e]] = splitCubic(p0, p1, p2, p3, (lo + hi) / 2);
      // From the middle on: leave along the tangent there, end turned to the right around the middle.
      const end = turn(p3, m, bend);
      const q = add(m, mul(norm(sub(e, m)), Math.hypot(...sub(end, m)) * 0.5));
      d = `M${pt(p0)} C${pt(a)} ${pt(b)} ${pt(m)} Q${pt(q)} ${pt(end)}`;
      right = Math.max(right, xAt(q, sub(end, q), top) + w / 2);
    } else {
      d = `M${pt(p0)} C${pt(p1)} ${pt(p2)} ${pt(p3)}`;
      right = Math.max(right, xAt(p2, sub(p3, p2), top) + w / 2);
    }
    lanes.push({ d, w });
  }
  const left = Math.min(
    xAt(add(L, mul(nL, band / 2)), uL, top),
    xAt(add(L, mul(nL, -band / 2)), uL, top),
  );
  const bottom = B[1] + band / 2;
  // Centre the drawing in the box: horizontally by its extent, vertically between the top line and the band's end.
  const dx = (256 - (right - left)) / 2 - left;
  const dy = (256 - (bottom - top)) / 2 - top;
  const Lx = sub(L, mul(uL, ext));
  // Outermost lane first, so a lane that starts under its inner neighbour is drawn beneath it.
  const order = [...lanes.keys()].reverse();
  return {
    transform: `translate(${r(dx)} ${r(dy)})`,
    top,
    // The drawing's own box after the translation: its top edge is the cut line, so a viewport of this box cuts the
    // lanes and the band there without a clip path.
    box: [r(left + dx - 2), r(top + dy), r(right - left + 4), r(bottom - top + 1)],
    lanes: order.map((i) => ({ lane: i, d: lanes[i].d, width: r(lanes[i].w) })),
    band: { d: `M${pt(Lx)} L${pt(B)}`, width: band },
  };
}

/** The mark's shapes as SVG markup, the lanes with the classes o0, o1, ... and the band with the class in. */
function markShapes(p) {
  const g = markGeometry(p);
  let s = `<defs><clipPath id="clip"><rect x="-200" y="${g.top}" width="656" height="400"/></clipPath></defs>`;
  s += `<g transform="${g.transform}"><g clip-path="url(#clip)">`;
  for (const l of g.lanes) {
    s += `<path class="o${l.lane}" d="${l.d}" fill="none" stroke-width="${l.width}"/>`;
  }
  s += `<path class="in" d="${g.band.d}" fill="none" stroke-width="${g.band.width}" stroke-linecap="round"/>`;
  s += `</g></g>`;
  return s;
}

// ---- Colours -----------------------------------------------------------------------------------

// The hues of the mark, each on a dark and a light ground: saturated, bright neon tones without a halo (owner,
// 2026-11-06). On a light ground the same hues a step deeper, so a lane does not fade into white. The band and the middle
// lane are neutral (the wordmark's colour). Green was tried for the inner lane and turned back to blue; it stays here
// only for the comparison draft.
const hues = {
  // The band is the wordmark's colour, so the v sits in the lockup like a letter of the name.
  neutral: ['#e8eaec', '#1c2433'],
  blue: ['#1fb6ff', '#0a84ff'],
  green: ['#2ee87a', '#0a9c4a'],
  yellow: ['#ffe94d', '#ffb700'],
  red: ['#ff2d55', '#ff1f4b'],
  violet: ['#c26bff', '#9b3dff'],
  orange: ['#ff8a1f', '#ff6a00'],
};
const text = ['#e8eaec', '#1c2433'];

/** A palette is the input's hue and one hue per lane, inside to outside. */
function colours(palette, scheme) {
  const k = scheme === 'dark' ? 0 : 1;
  return {
    in: hues[palette.in][k],
    out: palette.out.map((h) => hues[h][k]),
    text: text[k],
  };
}

function rules(c) {
  return [
    `.in{stroke:${c.in}}`,
    ...c.out.map((v, i) => `.o${i}{stroke:${v}}`),
    `.word{fill:${c.text}}`,
  ].join('');
}

/** A style block: one scheme fixed, or both following prefers-color-scheme. */
function style(palette, scheme) {
  if (scheme === 'auto') {
    return `<style>${rules(colours(palette, 'light'))}@media (prefers-color-scheme: dark){${rules(colours(palette, 'dark'))}}</style>`;
  }
  return `<style>${rules(colours(palette, scheme))}</style>`;
}

// ---- The wordmark as paths ---------------------------------------------------------------------

// Inter Bold of @fontsource/inter, a static file of the same Inter release as the UI's variable font
// (@fontsource-variable/inter): opentype.js reads .woff but not .woff2, and fontkit cannot instance a variable
// .woff2 font's outlines.
const inter = (() => {
  const file = join(
    dirname(createRequire(import.meta.url).resolve('@fontsource/inter/package.json')),
    'files',
    'inter-latin-700-normal.woff',
  );
  const buf = readFileSync(file);
  return opentype.parse(buf.buffer.slice(buf.byteOffset, buf.byteOffset + buf.length));
})();

/**
 * The word as one path (class word), its left edge at x and its baseline at y, set at fontSize with the font's pair
 * kerning and `tracking` user units after each letter. Returns the path markup and the word's width.
 */
function wordPath(word, x, y, fontSize, tracking, anchor = 'start') {
  const glyphs = [...word].map((c) => inter.charToGlyph(c));
  const scale = fontSize / inter.unitsPerEm;
  let width = 0;
  const advances = glyphs.map((g, i) => {
    const kern = i + 1 < glyphs.length ? inter.getKerningValue(g, glyphs[i + 1]) : 0;
    const a = (g.advanceWidth + kern) * scale + (i + 1 < glyphs.length ? tracking : 0);
    width += a;
    return a;
  });
  let pen = anchor === 'middle' ? x - width / 2 : x;
  let d = '';
  glyphs.forEach((g, i) => {
    d += g.getPath(pen, y, fontSize).toPathData(2);
    pen += advances[i];
  });
  return { markup: `<path class="word" d="${d}"/>`, width };
}

// ---- The three forms ---------------------------------------------------------------------------

function markSvg(p, palette, scheme, title) {
  return (
    `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 256 256" role="img" aria-label="${title}">` +
    style(palette, scheme) +
    markShapes(p) +
    `</svg>\n`
  );
}

/** The app icon: the mark above the wordmark on a dark rounded square, the same in both schemes. */
function squareSvg(p, palette) {
  return (
    `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 512 512" role="img" aria-label="trove">` +
    style(palette, 'dark') +
    `<rect width="512" height="512" rx="96" fill="#121822"/>` +
    `<g transform="translate(112 40) scale(1.125)">${markShapes(p)}</g>` +
    wordPath('trove', 256, 436, 104, 4, 'middle').markup +
    `</svg>\n`
  );
}

/** The horizontal lockup: the mark, then the wordmark on its baseline. */
function horizontalSvg(p, palette, scheme) {
  return (
    `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 760 256" role="img" aria-label="trove">` +
    style(palette, scheme) +
    markShapes(p) +
    wordPath('trove', 252, 196, 168, 6).markup +
    `</svg>\n`
  );
}

// ---- Candidate and alternatives ----------------------------------------------------------------

// Owner, 2026-11-06: three lanes, the outermost red and curving to the right from its middle on; neon colours without a
// halo; a slim band (26, as slim as two lanes) in the wordmark's colour, so no hue is used twice; the red lane starts
// beneath the yellow one instead of beside it, where it looked stuck on.
const base = {
  W: 44,
  band: 26,
  n: 3,
  gap: 4,
  fan: 2.2,
  top: 40,
  mode: 'swoosh',
  bend: [0, 0, 18],
  under: [undefined, undefined, 1],
  share: 0,
};
// Owner, 2026-11-06: the red lane fully beneath the middle one until it curves (variant Z1); the middle lane in the
// band's neutral colour, the inner one blue.
const candidate = { ...base, share: 1 };
const palette = { in: 'neutral', out: ['blue', 'neutral', 'red'] };
// The keys and notes are repeated in features/login/logo-preview.ts, which shows them.
const alternatives = {
  Z1: [candidate, palette, 'the candidate: blue, neutral, red'],
  Z4: [
    { ...candidate, follow: true },
    palette,
    'red on the middle path up to half height, then curves out',
  ],
  G1: [
    candidate,
    { in: 'neutral', out: ['green', 'neutral', 'red'] },
    'before: green, neutral, red',
  ],
};

// ---- The favicon: a reduced form of the mark ---------------------------------------------------

// At 16 and 32 px three slim lanes run together. The reduced form keeps the idea (a band that splits and fans out,
// the outer lane curving right from beneath its neighbour) with two lanes instead of three and a much thicker band
// and lanes, in the candidate's blue and red, on the app icon's dark tile (the same in both schemes, so the tab's
// colour does not decide whether the mark can be read).
const reduced = {
  W: 76,
  band: 48,
  n: 2,
  gap: 8,
  fan: 1.8,
  top: 40,
  mode: 'swoosh',
  bend: [0, 22],
  under: [undefined, 0],
  share: 1,
};
const reducedPalette = { in: 'neutral', out: ['blue', 'red'] };
const TILE = '#121822';
const TILE_RADIUS = 56;
/** The mark's share of the tile: it is drawn at this scale around the tile's centre. */
const FAVICON_SCALE = 0.84;
const FAVICON_SIZES = [16, 32, 48];

function faviconSvg(p, palette) {
  const o = (1 - FAVICON_SCALE) * 128;
  return (
    `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 256 256">` +
    style(palette, 'dark') +
    `<rect width="256" height="256" rx="${TILE_RADIUS}" fill="${TILE}"/>` +
    `<g transform="translate(${r(o)} ${r(o)}) scale(${FAVICON_SCALE})">${markShapes(p)}</g>` +
    `</svg>\n`
  );
}

/** The points of a path of M, C and Q commands (as markGeometry writes them), flattened to a polyline. */
function flatten(d) {
  const n = d.match(/-?\d+(?:\.\d+)?/g).map(Number);
  let i = 0;
  const take = () => [n[i++], n[i++]];
  const out = [];
  let cur;
  for (const c of d.match(/[MCQL]/g)) {
    if (c === 'M' || c === 'L') {
      cur = take();
      out.push(cur);
    } else {
      const ctrl = c === 'C' ? [take(), take()] : [take()];
      const end = take();
      for (let k = 1; k <= 48; k++) {
        const t = k / 48;
        const u = 1 - t;
        out.push(
          [0, 1].map((a) =>
            ctrl.length === 2
              ? u * u * u * cur[a] +
                3 * u * u * t * ctrl[0][a] +
                3 * u * t * t * ctrl[1][a] +
                t * t * t * end[a]
              : u * u * cur[a] + 2 * u * t * ctrl[0][a] + t * t * end[a],
          ),
        );
      }
      cur = end;
    }
  }
  return out;
}

/** Distance of point q to the polyline. */
function distance(poly, q) {
  let best = Infinity;
  for (let k = 1; k < poly.length; k++) {
    const a = poly[k - 1];
    const ab = sub(poly[k], a);
    const len2 = ab[0] * ab[0] + ab[1] * ab[1];
    const t =
      len2 === 0
        ? 0
        : Math.max(0, Math.min(1, ((q[0] - a[0]) * ab[0] + (q[1] - a[1]) * ab[1]) / len2));
    best = Math.min(best, Math.hypot(q[0] - a[0] - t * ab[0], q[1] - a[1] - t * ab[1]));
  }
  return best;
}

const hex = (h) => [1, 3, 5].map((k) => parseInt(h.slice(k, k + 2), 16));

/** The favicon at size × size pixels as RGBA, by 8 × 8 samples per pixel: the same shapes and colours as the SVG. */
function faviconPixels(p, palette, size) {
  const g = markGeometry(p);
  const [dx, dy] = g.transform.match(/-?\d+(?:\.\d+)?/g).map(Number);
  const k = hex(TILE);
  const c = colours(palette, 'dark');
  // Painted in the SVG's order: the lanes (outermost first), then the band.
  const shapes = [
    ...g.lanes.map((l) => ({ poly: flatten(l.d), half: l.width / 2, rgb: hex(c.out[l.lane]) })),
    { poly: flatten(g.band.d), half: g.band.width / 2, rgb: hex(c.in) },
  ];
  const N = 8;
  const px = new Uint8Array(size * size * 4);
  for (let y = 0; y < size; y++) {
    for (let x = 0; x < size; x++) {
      let a = 0;
      const sum = [0, 0, 0];
      for (let j = 0; j < N; j++) {
        for (let i = 0; i < N; i++) {
          const q = [((x + (i + 0.5) / N) / size) * 256, ((y + (j + 0.5) / N) / size) * 256];
          // The tile: a square with rounded corners.
          const cx = Math.min(Math.max(q[0], TILE_RADIUS), 256 - TILE_RADIUS);
          const cy = Math.min(Math.max(q[1], TILE_RADIUS), 256 - TILE_RADIUS);
          if (Math.hypot(q[0] - cx, q[1] - cy) > TILE_RADIUS) continue;
          let rgb = k;
          // The same point in the mark's own coordinates (before the scale around the centre and the centring translate).
          const m = [
            (q[0] - 128) / FAVICON_SCALE + 128 - dx,
            (q[1] - 128) / FAVICON_SCALE + 128 - dy,
          ];
          if (m[1] >= g.top) {
            for (const s of shapes) if (distance(s.poly, m) <= s.half) rgb = s.rgb;
          }
          a++;
          for (let ch = 0; ch < 3; ch++) sum[ch] += rgb[ch];
        }
      }
      const o = (y * size + x) * 4;
      for (let ch = 0; ch < 3; ch++) px[o + ch] = a ? Math.round(sum[ch] / a) : 0;
      px[o + 3] = Math.round((a / (N * N)) * 255);
    }
  }
  return px;
}

function png(size, rgba) {
  const chunk = (type, data) => {
    const head = Buffer.alloc(8);
    head.writeUInt32BE(data.length, 0);
    head.write(type, 4, 'latin1');
    const crc = Buffer.alloc(4);
    crc.writeUInt32BE(crc32(Buffer.concat([head.subarray(4), data])), 0);
    return Buffer.concat([head, data, crc]);
  };
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(size, 0);
  ihdr.writeUInt32BE(size, 4);
  ihdr.set([8, 6, 0, 0, 0], 8); // 8 bits, RGBA, no interlace
  const rows = Buffer.alloc((size * 4 + 1) * size);
  for (let y = 0; y < size; y++) {
    rows[y * (size * 4 + 1)] = 0; // filter: none
    Buffer.from(rgba.buffer, y * size * 4, size * 4).copy(rows, y * (size * 4 + 1) + 1);
  }
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk('IHDR', ihdr),
    chunk('IDAT', deflateSync(rows, { level: 9 })),
    chunk('IEND', Buffer.alloc(0)),
  ]);
}

/** An ICO whose frames are PNGs (every browser that reads .ico reads those). */
function ico(frames) {
  const head = Buffer.alloc(6);
  head.writeUInt16LE(1, 2); // type: icon
  head.writeUInt16LE(frames.length, 4);
  let offset = 6 + 16 * frames.length;
  const dir = frames.map(([size, data]) => {
    const e = Buffer.alloc(16);
    e[0] = size;
    e[1] = size;
    e.writeUInt16LE(1, 4); // planes
    e.writeUInt16LE(32, 6); // bits per pixel
    e.writeUInt32LE(data.length, 8);
    e.writeUInt32LE(offset, 12);
    offset += data.length;
    return e;
  });
  return Buffer.concat([head, ...dir, ...frames.map(([, data]) => data)]);
}

writeFileSync(join(webDir, 'public', 'favicon.svg'), faviconSvg(reduced, reducedPalette));
writeFileSync(
  join(webDir, 'public', 'favicon.ico'),
  ico(FAVICON_SIZES.map((n) => [n, png(n, faviconPixels(reduced, reducedPalette, n))])),
);

rmSync(draftsDir, { recursive: true, force: true });
mkdirSync(draftsDir, { recursive: true });

writeFileSync(join(outDir, 'trove-mark.svg'), markSvg(candidate, palette, 'auto', 'trove'));
writeFileSync(join(outDir, 'trove-square.svg'), squareSvg(candidate, palette));
writeFileSync(join(outDir, 'trove-horizontal.svg'), horizontalSvg(candidate, palette, 'auto'));
for (const scheme of ['dark', 'light']) {
  writeFileSync(
    join(draftsDir, `mark-${scheme}.svg`),
    markSvg(candidate, palette, scheme, 'trove'),
  );
  writeFileSync(
    join(draftsDir, `horizontal-${scheme}.svg`),
    horizontalSvg(candidate, palette, scheme),
  );
}
for (const [key, [p, pal]] of Object.entries(alternatives)) {
  writeFileSync(join(draftsDir, `alt-${key}.svg`), markSvg(p, pal, 'dark', `draft ${key}`));
}
// The candidate's geometry for the app itself (shared/brand/trove-mark.ts draws it inline, so the neutral parts take the
// theme's text colour, which an <img> cannot).
const geometry = markGeometry(candidate);
const geometryFile = join(
  fileURLToPath(new URL('.', import.meta.url)),
  '..',
  'src',
  'app',
  'shared',
  'brand',
  'mark-geometry.ts',
);
mkdirSync(join(geometryFile, '..'), { recursive: true });
writeFileSync(
  geometryFile,
  `// Generated by web/scripts/brand-logo.mjs from the logo's candidate; do not edit, run the script.\n\n` +
    `/** A lane's hue: neutral is the text colour of the surface it sits on. */\n` +
    `export type MarkHue = ${[...new Set(palette.out)].map((h) => `'${h}'`).join(' | ')};\n\n` +
    `export const MARK_GEOMETRY = ${JSON.stringify(
      {
        viewBox: geometry.box.join(' '),
        transform: geometry.transform,
        lanes: geometry.lanes.map((l) => ({ ...l, hue: palette.out[l.lane] })),
        band: geometry.band,
      },
      null,
      2,
    )} as const;\n`,
);
console.log(`brand: wrote ${outDir} and ${geometryFile}`);
