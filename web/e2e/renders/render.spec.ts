import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { Page, expect, test } from '@playwright/test';
import { manifestDir, outDir } from './paths';
import { Kind } from './seed';
import { Screen, screens } from './screens';

/**
 * The renders of `make ui-renders` (rule 11 of docs/projektplan/promt.md; docs/developer/web-ui.md#renders-of-every-screen):
 * every screen, as each kind of user that reaches it, in both schemes and at three widths, every tab of the screen, the
 * whole length of the content. A screen fails when it shows an error notice, when a call of the app's own API answers 4xx
 * or 5xx, or when the console reports an error; the pictures are taken first, so a failure has its picture.
 */

/** The widths: wide, the narrowest window with the pinned sidebar, a phone. UI_RENDERS_WIDTHS=1440,1280,1024,390 adds one. */
export const widths: readonly number[] = (process.env['UI_RENDERS_WIDTHS'] ?? '1440,1024,390').split(',').map(Number);
export const schemes = ['light', 'dark'] as const;
const heightOf = (width: number): number => (width < 600 ? 844 : width < 1200 ? 768 : 900);

/** What one picture shows, beside the picture: the contact sheet and the audit read it. */
export interface Shot {
  readonly kind: Kind;
  readonly screen: string;
  readonly tab: string | null;
  readonly scheme: (typeof schemes)[number];
  readonly width: number;
  readonly file: string;
  readonly url: string;
  /** How far the content panel (or the page) scrolls sideways, in CSS px. */
  readonly overflowX: number;
  /** The regions inside it that scroll sideways (a table's scroll region, a code block), with how far. */
  readonly regions: readonly { readonly what: string; readonly overflowX: number }[];
  /** The widths of the address fields of each table, rounded to the pixel: one width per list is the rule (R-UI-065). */
  readonly addressWidths: readonly (readonly number[])[];
}

for (const screen of screens) {
  for (const kind of screen.kinds) {
    test(`${screen.id} as ${kind}`, { tag: `@${kind}` }, async ({ page }) => {
      forget(kind, screen);
      const failures = watch(page, screen);
      trackRequests(page);
      const shots: Shot[] = [];
      for (const width of widths) {
        await page.setViewportSize({ width, height: heightOf(width) });
        await page.emulateMedia({ colorScheme: 'light' });
        const t0 = Date.now();
        await page.goto(screen.path);
        if (process.env['UI_RENDERS_DEBUG']) console.log(`goto: ${Date.now() - t0} ms`);
        await settle(page);
        if (screen.walk) {
          // A flow: one picture of each state it names.
          for await (const state of screen.walk(page)) {
            await settle(page);
            for (const scheme of schemes) {
              await page.emulateMedia({ colorScheme: scheme });
              shots.push(await shoot(page, kind, screen, state, scheme, width));
            }
            await page.emulateMedia({ colorScheme: 'light' });
            failures.notices.push(...(await errorNotices(page, width, state)));
          }
          continue;
        }
        // The screen as it opens is the picture without a tab in its name; then every other tab.
        // allTextContents does not wait: most screens have no tab.
        const opened = (await page.locator('main [role=tab][aria-selected=true]').allTextContents())[0]?.replace(/\s+/g, ' ').trim();
        const tabs = (await tabNames(page)).filter((t) => t !== opened);
        for (const tab of [null, ...tabs]) {
          if (tab !== null) {
            await page.locator('main [role=tab]').filter({ hasText: tab }).first().click();
            await settle(page);
          }
          for (const scheme of schemes) {
            await page.emulateMedia({ colorScheme: scheme });
            shots.push(await shoot(page, kind, screen, tab, scheme, width));
          }
          await page.emulateMedia({ colorScheme: 'light' });
          failures.notices.push(...(await errorNotices(page, width, tab)));
        }
      }
      record(kind, screen, shots, failures.list());
      expect(failures.list(), `${screen.id} as ${kind}: ${screen.path}`).toEqual([]);
    });
  }
}

/** The labels of the tabs of the screen (`role=tab` inside `main`), the selected one first left out by the caller. */
async function tabNames(page: Page): Promise<string[]> {
  const names = await page.locator('main [role=tab]').allTextContents();
  return names.map((n) => n.replace(/\s+/g, ' ').trim()).filter((n, i, a) => n !== '' && a.indexOf(n) === i);
}

/** The calls of the app's API that are open, per page: `settle` waits until there are none. */
const open = new WeakMap<Page, Set<unknown>>();

function trackRequests(page: Page): void {
  const pending = new Set<unknown>();
  open.set(page, pending);
  const api = (url: string): boolean => /^\/(api|auth)\//.test(new URL(url).pathname);
  page.on('request', (r) => {
    if (api(r.url())) pending.add(r);
  });
  page.on('requestfinished', (r) => pending.delete(r));
  page.on('requestfailed', (r) => pending.delete(r));
  // A redirected request gets its answer and no "finished" of its own.
  page.on('response', (r) => {
    if (r.status() >= 300 && r.status() < 400) pending.delete(r.request());
  });
}

/**
 * Waits until the screen stands still: the document loaded, no call of the app's API open for 400 ms (at most 15 s: a
 * screen that polls never gets there and is taken as it is), the fonts loaded and every finite animation finished.
 */
async function settle(page: Page): Promise<void> {
  await page.waitForLoadState('load');
  const pending = open.get(page);
  const deadline = Date.now() + 15_000;
  let quietSince = Date.now();
  while (pending && Date.now() < deadline) {
    await page.waitForTimeout(100);
    if (pending.size > 0) quietSince = Date.now();
    else if (Date.now() - quietSince >= 400) break;
  }
  if (pending && pending.size > 0 && Date.now() >= deadline) {
    console.log(`settle: still open after 15 s: ${[...pending].map((r) => (r as { url(): string }).url()).join(', ')}`);
  }
  const t0 = Date.now();
  await page.evaluate(async () => {
    await document.fonts.ready;
    await Promise.all(
      document
        .getAnimations()
        .filter((a) => a.effect?.getComputedTiming().iterations !== Infinity)
        .map((a) => a.finished.catch(() => undefined)),
    );
  });
  if (process.env['UI_RENDERS_DEBUG']) console.log(`settle: network ${t0 - deadline + 15_000} ms, animations ${Date.now() - t0} ms`);
  await page.mouse.move(0, 0);
}

/**
 * The whole content: the panel `main` scrolls inside the shell and a list's region scrolls inside the panel, each with a
 * height that follows the window, so the window grows by what they hide (a few rounds, at most 16,000 px), then shrinks back.
 */
async function shoot(page: Page, kind: Kind, screen: Screen, tab: string | null, scheme: Shot['scheme'], width: number): Promise<Shot> {
  const view = page.viewportSize() ?? { width, height: 900 };
  let height = view.height;
  for (let round = 0; round < 4; round++) {
    const hidden = await page.evaluate(() => {
      const vertical = (el: Element | null): number => (el ? Math.max(0, el.scrollHeight - el.clientHeight) : 0);
      const main = document.querySelector('main');
      let inner = 0;
      for (const el of Array.from(document.querySelectorAll<HTMLElement>('main *'))) {
        if (['auto', 'scroll'].includes(getComputedStyle(el).overflowY)) inner = Math.max(inner, vertical(el));
      }
      return Math.max(vertical(main), vertical(document.scrollingElement)) + inner;
    });
    if (hidden <= 1 || height >= 16_000) break;
    height = Math.min(height + hidden, 16_000);
    await page.setViewportSize({ width: view.width, height });
    await settle(page);
  }
  const measured = await page.evaluate(() => {
    const main = document.querySelector('main');
    const doc = document.scrollingElement;
    const overflowX = Math.max(main ? main.scrollWidth - main.clientWidth : 0, doc ? doc.scrollWidth - doc.clientWidth : 0);
    const regions: { what: string; overflowX: number }[] = [];
    for (const el of Array.from(document.querySelectorAll<HTMLElement>('main *'))) {
      const style = getComputedStyle(el);
      if (!['auto', 'scroll'].includes(style.overflowX) || el.clientWidth === 0) continue;
      const over = el.scrollWidth - el.clientWidth;
      if (over > 1) {
        const label = el.getAttribute('aria-label') ?? el.getAttribute('aria-labelledby') ?? '';
        regions.push({ what: `${el.tagName.toLowerCase()}.${Array.from(el.classList).join('.')}${label ? ` [${label}]` : ''}`, overflowX: over });
      }
    }
    const addressWidths = Array.from(document.querySelectorAll('main table'))
      .map((t) => Array.from(t.querySelectorAll('trove-address-field input')).map((i) => Math.round(i.getBoundingClientRect().width)))
      .filter((w) => w.length > 0);
    return { overflowX, regions, addressWidths };
  });
  const name = `${screen.id}${tab ? `-${slug(tab)}` : ''}-${scheme}-${width}.png`;
  const file = join(kind, name);
  mkdirSync(join(outDir, kind), { recursive: true });
  const t0 = Date.now();
  await page.screenshot({ path: join(outDir, file), animations: 'disabled', caret: 'hide' });
  if (process.env['UI_RENDERS_DEBUG']) console.log(`shoot: ${Date.now() - t0} ms`);
  if (height !== view.height) {
    await page.setViewportSize(view);
    await settle(page);
  }
  return { kind, screen: screen.id, tab, scheme, width, file, url: new URL(page.url()).pathname + new URL(page.url()).search, ...measured };
}

function slug(text: string): string {
  return text
    .toLowerCase()
    .replace(/\(\d+\)|\d+$/g, '')
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-|-$/g, '');
}

/** The failures of a screen: the console's errors, uncaught errors, and the app's API calls answered 4xx or 5xx. */
function watch(page: Page, screen: Screen): { notices: string[]; list(): string[] } {
  const seen: string[] = [];
  const notices: string[] = [];
  page.on('console', (m) => {
    if (m.type() !== 'error') return;
    // The browser's own line for a failed fetch repeats the response below.
    if (/^Failed to load resource: the server responded with a status of/.test(m.text())) return;
    seen.push(`console error: ${m.text().slice(0, 300)}`);
  });
  page.on('pageerror', (e) => seen.push(`uncaught: ${e.message.slice(0, 300)}`));
  page.on('response', (r) => {
    const url = new URL(r.url());
    if (!/^\/(api|auth)\//.test(url.pathname) || r.status() < 400) return;
    const line = `${r.request().method()} ${url.pathname}${url.search} answered ${r.status()}`;
    if (screen.expected?.some((e) => e.match.test(line))) return;
    seen.push(line);
  });
  return { notices, list: () => [...new Set([...seen, ...notices])] };
}

/** An error notice on the screen is a failure, whatever caused it. */
async function errorNotices(page: Page, width: number, tab: string | null): Promise<string[]> {
  const texts = await page.locator('main trove-error-notice').allTextContents();
  const where = `${tab ? `tab ${tab}, ` : ''}${width} px`;
  return texts.map((t) => `error notice (${where}): ${t.replace(/\s+/g, ' ').trim().slice(0, 200)}`);
}

function record(kind: Kind, screen: Screen, shots: readonly Shot[], failures: readonly string[]): void {
  mkdirSync(manifestDir, { recursive: true });
  writeFileSync(join(manifestDir, `${kind}--${screen.id}.json`), JSON.stringify({ kind, screen: screen.id, path: screen.path, shots, failures }, null, 1));
}

/** Removes the pictures of the screen's last run as this kind of user: its tabs may have changed since. */
function forget(kind: Kind, screen: Screen): void {
  const manifest = join(manifestDir, `${kind}--${screen.id}.json`);
  if (!existsSync(manifest)) return;
  const last = JSON.parse(readFileSync(manifest, 'utf8')) as { shots: readonly Shot[] };
  for (const shot of last.shots) rmSync(join(outDir, shot.file), { force: true });
  rmSync(manifest, { force: true });
}
