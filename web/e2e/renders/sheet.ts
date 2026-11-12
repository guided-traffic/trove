import { existsSync, readFileSync, readdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { manifestDir, outDir } from './paths';
import type { Shot } from './render.spec';
import { screens } from './screens';

interface Entry {
  readonly kind: string;
  readonly screen: string;
  readonly path: string;
  readonly shots: readonly Shot[];
  readonly failures: readonly string[];
}

const kindOrder = ['anonymous', 'plain', 'power', 'admin'];

/**
 * The global teardown of `make ui-renders`: the contact sheet `.out/ui-renders/index.html`, grouped by screen, and
 * `report.json` with what each picture measured. At the top: the failures, the screens that scroll sideways at 1024 px and
 * wider, and the lists whose address fields differ in width. It runs after failed renders too.
 */
export default function sheet(): void {
  if (!existsSync(manifestDir)) return;
  const entries: Entry[] = readdirSync(manifestDir)
    .filter((f) => f.endsWith('.json'))
    .map((f) => JSON.parse(readFileSync(join(manifestDir, f), 'utf8')) as Entry);
  const order = new Map(screens.map((s, i) => [s.id, i]));
  entries.sort((a, b) => (order.get(a.screen) ?? 0) - (order.get(b.screen) ?? 0) || kindOrder.indexOf(a.kind) - kindOrder.indexOf(b.kind));

  const failed = entries.filter((e) => e.failures.length > 0);
  // Sideways: the panel at any width (a phone included), and from 1024 px also a region that scrolls inside the page.
  const sideways = entries.flatMap((e) =>
    e.shots.filter((s) => s.scheme === 'light' && (s.overflowX > 1 || (s.width >= 1024 && s.regions.length > 0))),
  );
  const uneven = entries.flatMap((e) => e.shots.filter((s) => s.scheme === 'light' && s.addressWidths.some((w) => new Set(w).size > 1)));
  writeFileSync(join(outDir, 'report.json'), JSON.stringify({ entries, failed: failed.length, sideways: sideways.length, uneven: uneven.length }, null, 1));
  writeFileSync(join(outDir, 'failures.txt'), failed.map((e) => `${e.kind} ${e.screen} ${e.path}\n${e.failures.map((f) => `  ${f}`).join('\n')}\n`).join(''));

  const esc = (s: string): string => s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
  const shotLine = (s: Shot): string =>
    `<li><a href="${esc(s.file)}">${esc(s.file)}</a>${s.overflowX > 1 ? ` content ${s.overflowX} px wider` : ''}${
      s.offender ? ` (widest: ${esc(s.offender.what)}, ${s.offender.overflowX} px)` : ''
    }${s.regions
      .map((r) => ` ${esc(r.what)} ${r.overflowX} px`)
      .join(';')}${s.addressWidths.length ? ` address widths ${esc(JSON.stringify(s.addressWidths))}` : ''}</li>`;
  const groups = new Map<string, Entry[]>();
  for (const e of entries) groups.set(e.screen, [...(groups.get(e.screen) ?? []), e]);

  const sections = [...groups.entries()].map(([screen, list]) => {
    const rows = list.flatMap((e) => {
      const tabs = [...new Set(e.shots.map((s) => s.tab))];
      return tabs.map((tab) => {
        const shots = e.shots.filter((s) => s.tab === tab).sort((a, b) => a.scheme.localeCompare(b.scheme) * -1 || b.width - a.width);
        const figures = shots
          .map((s) => `<figure><a href="${esc(s.file)}"><img loading="lazy" src="${esc(s.file)}" alt="${esc(`${s.screen} ${s.scheme} ${s.width}`)}"></a><figcaption>${s.scheme} ${s.width}</figcaption></figure>`)
          .join('');
        const bad = e.failures.length ? ` class="failed" title="${esc(e.failures.join('\n'))}"` : '';
        return `<div class="row"${bad}><h3>${esc(e.kind)}${tab ? ` · ${esc(tab)}` : ''}</h3><div class="shots">${figures}</div></div>`;
      });
    });
    return `<section id="${esc(screen)}"><h2>${esc(screen)} <code>${esc(list[0]?.path ?? '')}</code></h2>${rows.join('')}</section>`;
  });

  const html = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>trove UI renders</title>
<style>
body { font: 14px/1.5 system-ui, sans-serif; margin: 1.5rem; color: #222; }
nav { columns: 4 14rem; margin-block-end: 1.5rem; }
nav a { display: block; }
section { border-block-start: 1px solid #ccc; padding-block: .5rem 1rem; }
h2 { font-size: 1.1rem; margin: .5rem 0; } h2 code { font-weight: 400; color: #555; }
h3 { font-size: .9rem; margin: .25rem 0; }
.row.failed h3 { color: #b00; } .row.failed h3::after { content: " (failed: hover for why)"; }
.shots { display: flex; gap: .5rem; align-items: flex-start; overflow-x: auto; }
figure { margin: 0; } figure img { width: 220px; border: 1px solid #ccc; display: block; } figcaption { font-size: 12px; color: #555; }
.findings li { margin-block-end: .25rem; }
</style></head><body>
<h1>trove UI renders</h1>
<p>${entries.reduce((n, e) => n + e.shots.length, 0)} pictures of ${groups.size} screens, generated ${new Date().toISOString()} by <code>make ui-renders</code>. Each row: light, then dark, at 1440, 1024 and 390 px. Judge them against the <a href="../../docs/developer/ui-style-guide.md#checklist">checklist</a>.</p>
<h2>Failures (${failed.length})</h2>
<ul class="findings">${failed.map((e) => `<li><a href="#${esc(e.screen)}">${esc(e.screen)}</a> as ${esc(e.kind)}: ${e.failures.map(esc).join('; ')}</li>`).join('') || '<li>None.</li>'}</ul>
<h2>Sideways scroll at 1024 px and wider (${sideways.length})</h2>
<ul class="findings">${sideways.map(shotLine).join('') || '<li>None.</li>'}</ul>
<h2>Address fields of one list in different widths (${uneven.length})</h2>
<ul class="findings">${uneven.map(shotLine).join('') || '<li>None.</li>'}</ul>
<h2>Screens</h2>
<nav>${[...groups.keys()].map((s) => `<a href="#${esc(s)}">${esc(s)}</a>`).join('')}</nav>
${sections.join('\n')}
</body></html>
`;
  writeFileSync(join(outDir, 'index.html'), html);
  console.log(`ui-renders: ${join(outDir, 'index.html')}; ${failed.length} failed, ${sideways.length} sideways, ${uneven.length} uneven address columns`);
}
