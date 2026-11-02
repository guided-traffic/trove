import { Router } from '@angular/router';
import { ManualApi } from '../../api/manual-api';
import { ApiError } from '../../api/problem';
import { FakeManualApi } from '../../api/testing/fakes-manual';
import { expectAccessible, renderScreen, settle } from '../../testing/screen';
import { HelpOverview } from './help-overview';
import { HelpPage } from './help-page';

async function page(section: string, id: string, setup?: (api: FakeManualApi) => void) {
  const r = await renderScreen(HelpPage, {
    inputs: { section, page: id },
    setup: (get) => setup?.(get(ManualApi) as FakeManualApi),
  });
  return { ...r, api: r.get(ManualApi) as FakeManualApi };
}

describe('Help', () => {
  it('R-UI-050 the overview lists both guides with every page title and summary, linked to the page', async () => {
    const r = await renderScreen(HelpOverview);
    expect(r.root.querySelector('h1')?.textContent?.trim()).toBe('Help');
    const sections = [...r.root.querySelectorAll('section.help-section')];
    expect(sections.map((s) => s.querySelector('h2')?.textContent?.trim())).toEqual(['User guide', 'Administrator guide']);
    const cards = [...r.root.querySelectorAll<HTMLAnchorElement>('a.help-card')];
    expect(cards.map((a) => a.getAttribute('href'))).toEqual([
      '/help/user/getting-started',
      '/help/user/access-tokens',
      '/help/admin/overview',
      '/help/admin/endpoints',
    ]);
    expect(cards[0]?.textContent).toContain('How to sign in and find your way around.');
    // The manual is English whatever the language of the frame.
    expect(r.root.querySelector('ul.help-cards')?.getAttribute('lang')).toBe('en');
    await expectAccessible(r.root);
  });

  it('R-UI-050 a page shows its title, its headings as "On this page", its text, the contents and its neighbours', async () => {
    const r = await page('user', 'access-tokens');
    expect(r.root.querySelector('h1')?.textContent?.trim()).toBe('Personal access tokens');
    expect(r.root.querySelector('h1')?.getAttribute('lang')).toBe('en');
    expect([...r.root.querySelectorAll('.help-toc li')].map((li) => li.textContent?.trim())).toEqual(['Create a token']);
    expect(r.root.querySelector('.readme h2')?.id).toBe('user-content-create-a-token');
    const contents = [...r.root.querySelectorAll<HTMLAnchorElement>('.help-contents ul a')];
    expect(contents.map((a) => a.textContent?.trim())).toEqual([
      'Getting started',
      'Personal access tokens',
      'How trove runs',
      'Endpoints and exposure rules',
    ]);
    const pager = [...r.root.querySelectorAll<HTMLAnchorElement>('.help-pager a')];
    expect(pager.map((a) => [a.getAttribute('rel'), a.getAttribute('href')])).toEqual([
      ['prev', '/help/user/getting-started'],
      ['next', '/help/admin/overview'],
    ]);
    await expectAccessible(r.root);
  });

  it('R-UI-050 a link to another page stays a link and opens that page in the application; a link inside the page scrolls', async () => {
    const r = await page('user', 'getting-started');
    const links = [...r.root.querySelectorAll<HTMLAnchorElement>('.readme a')];
    const other = links.find((a) => a.textContent === 'tokens')!;
    expect(other.getAttribute('href')).toBe('help/user/access-tokens#user-content-create-a-token');
    expect(other.hasAttribute('rel')).toBe(false);
    other.click();
    await settle(r.fixture);
    expect(r.get(Router).url).toBe('/help/user/access-tokens#user-content-create-a-token');

    const inside = links.find((a) => a.textContent === 'below')!;
    const target = r.root.querySelector<HTMLElement>('#user-content-the-screen')!;
    let scrolled = false;
    target.scrollIntoView = () => (scrolled = true);
    inside.click();
    expect(scrolled).toBe(true);
  });

  it('R-UI-050 the article is not capped in width; the prose carries the measure class, a table its own scrolling container', async () => {
    const r = await page('admin', 'overview');
    expect(r.root.querySelector('.help-article')).not.toBeNull();
    expect(r.root.querySelector('trove-package-readme')?.classList.contains('help-readme')).toBe(true);
    const wrap = r.root.querySelector('.help-article .readme-table');
    expect(wrap?.querySelector('table th')?.textContent).toBe('Role');
    expect(wrap?.querySelector('td code')?.textContent).toBe('all');
    await expectAccessible(r.root);
  });

  it('R-UI-050 the first and the last page have one neighbour', async () => {
    const first = await page('user', 'getting-started');
    expect([...first.root.querySelectorAll('.help-pager a')].map((a) => a.getAttribute('rel'))).toEqual(['next']);
    const last = await page('admin', 'endpoints');
    expect([...last.root.querySelectorAll('.help-pager a')].map((a) => a.getAttribute('rel'))).toEqual(['prev']);
  });

  it('R-UI-050 an unknown page and a failed read show the error with a retry', async () => {
    const missing = await page('user', 'nothing');
    expect(missing.root.querySelector('trove-error-notice')).toBeTruthy();
    expect(missing.root.querySelector('article')).toBeNull();

    const failed = await page('user', 'getting-started', (api) => {
      api.failure = new ApiError(503, { code: 'unavailable', detail: 'down' }, null);
    });
    expect(failed.root.querySelector('trove-error-notice')).toBeTruthy();
    failed.api.failure = null;
    failed.root.querySelector<HTMLButtonElement>('trove-error-notice button')?.click();
    await settle(failed.fixture);
    expect(failed.root.querySelector('h1')?.textContent?.trim()).toBe('Getting started');
  });
});
