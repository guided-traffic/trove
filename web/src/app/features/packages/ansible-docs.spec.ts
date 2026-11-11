import { TestBed } from '@angular/core/testing';
import { Router } from '@angular/router';
import { AnsibleApi } from '../../api/ansible-api';
import { PackagesApi } from '../../api/packages-api';
import { ApiError } from '../../api/problem';
import { bareEntry, commonFixture, DocsFixture } from '../../api/testing/fakes-ansible-docs';
import { FakeAnsibleApi, importTask } from '../../api/testing/fakes-ansible';
import { FakePackagesApi, ansibleDocsFixture, ansibleFixture } from '../../api/testing/fakes-packages';
import { expectAccessible, inProject, renderScreen, settle, text, type } from '../../testing/screen';
import { PackageDetail } from './package-detail';
import { CONTENTS_PAGE } from './ansible-contents';
import { SEARCH_DEBOUNCE_MS } from './ansible-docs';

type Opened = Awaited<ReturnType<typeof open>>;

async function open(inputs: Record<string, unknown> = {}, setup?: (fake: FakePackagesApi) => void) {
  const r = await renderScreen(
    PackageDetail,
    inProject({
      inputs: { format: 'ansible', name: 'acme.tools', ...inputs },
      setup: (get) => {
        const fake = get(PackagesApi) as FakePackagesApi;
        ansibleFixture(fake);
        ansibleDocsFixture(fake);
        setup?.(fake);
      },
    }),
  );
  return { ...r, fake: r.get(PackagesApi) as FakePackagesApi, tasks: r.get(AnsibleApi) as FakeAnsibleApi };
}

async function set(r: Opened, inputs: Record<string, unknown>) {
  for (const [k, v] of Object.entries(inputs)) r.fixture.componentRef.setInput(k, v);
  await settle(r.fixture);
}

const tabLabels = (r: Opened) => [...r.root.querySelectorAll('[role="tab"]')].map((t) => text(t));
const tree = (r: Opened) => r.root.querySelector('ul[role="tree"]');
const items = (r: Opened) => [...r.root.querySelectorAll<HTMLElement>('[role="treeitem"]')];
const label = (el: Element) => text(el.querySelector('.node-name')!);
/** A row as it reads: the name and the count, without the glyph of the chevron. */
const rowText = (el: Element) => [...el.querySelectorAll('.node-name, .badge')].map((e) => text(e)).join(' ');
const current = (r: Opened) => r.root.querySelector<HTMLElement>('[role="treeitem"][aria-current="page"]');
const searchBox = (r: Opened) => r.root.querySelector<HTMLInputElement>('input[type="search"]')!;
const live = (r: Opened) => text(r.root.querySelector('.match-count')!);
/** The group rows with their counts: `Modules 8`. */
const groups = (r: Opened) => items(r).filter((i) => i.hasAttribute('aria-expanded') && i.getAttribute('aria-level') === '1').map(rowText);

describe('The Documentation tab of an Ansible collection', () => {
  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  describe('the tab list', () => {
    it('R-UI-060 opens on Documentation, then Contents with the count of its entries, and has no README tab', async () => {
      const r = await open();
      expect(tabLabels(r)).toEqual(['Documentation', 'Contents 32', 'Metadata', 'Files', 'Repositories', 'All versions', 'Import log']);
      expect(r.root.querySelector('[role="tab"][aria-selected="true"]')?.textContent).toContain('Documentation');
      // The README is the first page of the tab, so the README operation is not used.
      expect(r.fake.readmeCalls).toEqual([]);
      expect(r.fake.docCalls).toEqual(['tree', 'file:README.md']);
    });

    it('R-UI-060 R-UI-064 a caller who may not read the wiki gets no metadata.docs, so no Documentation and no Contents tab and no request', async () => {
      const r = await open({}, (fake) => {
        for (const v of fake.versionList) if (v.format === 'ansible') v.metadata = { requiresAnsible: '>=2.15.0' };
        for (const v of fake.versionList) v.hasReadme = true;
      });
      expect(tabLabels(r)).toEqual(['Metadata', 'Files', 'Repositories', 'All versions', 'Import log']);
      expect(r.fake.docCalls).toEqual([]);
      expect(r.fake.readmeCalls).toEqual([]);
    });

    it('R-UI-060 a version without a README and with the state none has no Documentation tab and asks for none of the operations', async () => {
      const r = await open({}, (fake) => {
        ansibleDocsFixture(fake, commonFixture({ state: 'none' }));
        for (const v of fake.versionList) v.hasReadme = false;
      });
      expect(tabLabels(r)).toEqual(['Metadata', 'Files', 'Repositories', 'All versions', 'Import log']);
      expect(r.fake.docCalls).toEqual([]);
    });

    it('R-UI-060 a version with a README and the state none shows the README alone in the Documentation tab, without a tree', async () => {
      const r = await open({}, (fake) => {
        ansibleDocsFixture(fake, commonFixture({ state: 'none' }));
        for (const v of fake.versionList) v.hasReadme = true;
      });
      expect(tabLabels(r)).toEqual(['Documentation', 'Metadata', 'Files', 'Repositories', 'All versions', 'Import log']);
      expect(tree(r)).toBeNull();
      expect(r.fake.docCalls).toEqual(['file:README.md']);
    });

    it('R-UI-060 R-UI-063 a tab that reads from the server on its own asks for nothing while another tab is open', async () => {
      const r = await open({ tab: 'metadata' }, (fake) => {
        ansibleDocsFixture(fake, commonFixture({ state: 'none' }));
        for (const v of fake.versionList) v.hasReadme = true;
      });
      expect(tabLabels(r)).toContain('Documentation');
      expect(tabLabels(r)).toContain('Import log');
      expect(r.fake.docCalls).toEqual([]);
      expect(r.tasks.calls).toEqual([]);
      await set(r, { tab: 'import-log' });
      expect(r.tasks.calls).toEqual(['importTasks']);
    });

    it('R-UI-060 R-UI-064 a 404 of the tree removes the Documentation and Contents tabs without a message', async () => {
      const r = await open({}, (fake) => fake.docFixtures.clear());
      expect(tabLabels(r)).toEqual(['Metadata', 'Files', 'Repositories', 'All versions', 'Import log']);
      expect(r.root.querySelector('[role="alert"]')).toBeNull();
    });

    it('R-UI-060 the install panel stays above the tabs while Documentation or Contents is open, and beside them for the others', async () => {
      const r = await open();
      const layout = () => r.root.querySelector('.pkg-layout')!;
      expect(layout().classList.contains('stacked')).toBe(true);
      await set(r, { tab: 'contents' });
      expect(layout().classList.contains('stacked')).toBe(true);
      await set(r, { tab: 'metadata' });
      expect(layout().classList.contains('stacked')).toBe(false);
    });

    it('R-UI-060 the tree is requested when its tab is open, once for the version, and not again when the user comes back', async () => {
      const r = await open({ tab: 'metadata' });
      expect(r.fake.docCalls).toEqual([]);
      await set(r, { tab: 'documentation' });
      await set(r, { tab: 'metadata' });
      await set(r, { tab: 'contents' });
      expect(r.fake.docCalls.filter((c) => c.startsWith('tree'))).toEqual(['tree']);
    });
  });

  describe('the tree', () => {
    it('R-UI-060 shows Documentation, Modules (8) and Plugins (24) with their counts, the README first and the entries folded away', async () => {
      const r = await open();
      expect(groups(r)).toEqual(['Documentation 2', 'Modules 8', 'Plugins 24']);
      expect(label(current(r)!)).toBe('hans_fischer.common');
      expect(items(r).filter((i) => i.getAttribute('aria-level') === '2').map((i) => label(i))).toEqual(['hans_fischer.common', 'install']);
      expect(text(r.root.querySelector('.pane-details')!)).toContain('Common roles and modules.');
      expect(r.root.querySelector('.pane-details h1')?.textContent).toBe('hans_fischer.common');
    });

    it('R-UI-060 R-UI-061 a page of the URL opens with the group that holds it, marked aria-current, and is read from the repository the tree names', async () => {
      const r = await open({ doc: 'module/check_host_reachable' });
      const open_ = current(r)!;
      expect(label(open_)).toBe('check_host_reachable');
      expect(open_.tagName).toBe('A');
      expect(open_.getAttribute('href')).toContain('doc=module%2Fcheck_host_reachable');
      expect(items(r).find((i) => i.getAttribute('aria-level') === '1' && rowText(i).startsWith('Modules'))!.getAttribute('aria-expanded')).toBe('true');
      expect(items(r).find((i) => rowText(i).startsWith('Plugins'))!.getAttribute('aria-expanded')).toBe('false');
      expect(text(r.root.querySelector('[data-testid="doc-entry"] .doc-title')!)).toContain('hans_fischer.common.check_host_reachable');
      expect(r.fake.docCalls).toContain('entry:module/check_host_reachable');
      expect(r.fake.docRepositories.at(-1)).toBe('web/ansible-local');
    });

    it('R-UI-060 a type of the plugins opens with its count, and an entry without documentation is muted and labelled', async () => {
      const r = await open({ doc: 'filter/is_cidr' });
      const type_ = items(r).find((i) => rowText(i).startsWith('filter'))!;
      expect(rowText(type_)).toBe('filter 8');
      expect(type_.getAttribute('aria-expanded')).toBe('true');
      expect(current(r)!.querySelector('.tag')?.textContent).toBe('no documentation');
      expect(text(r.root.querySelector('[data-testid="doc-no-docs"]')!)).toBe('This plugin has no documentation in its source.');
    });

    it('R-UI-060 a group opens and closes by a click on its row and keeps what the user chose', async () => {
      const r = await open();
      const modules = items(r).find((i) => rowText(i).startsWith('Modules'))!;
      expect(modules.getAttribute('aria-expanded')).toBe('false');
      modules.click();
      await settle(r.fixture);
      expect(items(r).find((i) => rowText(i).startsWith('Modules'))!.getAttribute('aria-expanded')).toBe('true');
      expect(items(r).filter((i) => i.getAttribute('aria-level') === '2' && i.tagName === 'A').length).toBeGreaterThan(2);
    });

    it('R-UI-060 an unknown doc shows the first page and no error', async () => {
      const r = await open({ doc: 'module/does_not_exist' });
      expect(label(current(r)!)).toBe('hans_fischer.common');
      expect(r.root.querySelector('[role="alert"]')).toBeNull();
      expect(r.fake.docCalls).not.toContain('entry:module/does_not_exist');
    });

    it('R-UI-060 another Markdown file is a page of its own', async () => {
      const r = await open({ doc: 'file/install.md' });
      expect(label(current(r)!)).toBe('install');
      expect(r.root.querySelector('.pane-details h1')?.textContent).toBe('Install');
    });

    it('R-UI-060 the tree is a role="tree" of links: the open entry is the one tab stop and the arrow keys move between rows', async () => {
      const r = await open({ doc: 'module/check_host_reachable' });
      expect(tree(r)?.getAttribute('aria-label')).toBe('Documentation');
      const first = items(r).find((i) => i.getAttribute('tabindex') === '0')!;
      expect(first).toBeDefined();
      first.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }));
      await settle(r.fixture);
      const after = items(r).find((i) => i.getAttribute('tabindex') === '0')!;
      expect(after).not.toBe(first);
      expect(items(r).filter((i) => i.tagName === 'A').every((i) => i.getAttribute('href')?.includes('doc='))).toBe(true);
    });

    it('R-UI-060 a long tree notes that it is cut', async () => {
      const r = await open({}, (fake) => ansibleDocsFixture(fake, commonFixture({ truncated: true })));
      expect(text(r.root.querySelector('[data-testid="docs-truncated"]')!)).toContain('Not every entry is listed');
    });
  });

  describe('the search', () => {
    it('R-UI-060 R-UI-064 sends the text 300 ms after the last key, only from two characters, and keeps the counts while the tree narrows', async () => {
      const r = await open();
      vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
      const navigate = vi.spyOn(TestBed.inject(Router), 'navigate').mockResolvedValue(true);
      const box = searchBox(r);
      expect(box.getAttribute('aria-label')).toBe('Search the documentation');
      type(box, 'h');
      vi.advanceTimersByTime(SEARCH_DEBOUNCE_MS + 1);
      expect(navigate).toHaveBeenCalledTimes(0);
      expect(live(r)).toBe('Enter at least 2 characters to search.');
      type(box, 'ho');
      vi.advanceTimersByTime(SEARCH_DEBOUNCE_MS - 50);
      type(box, 'hos');
      vi.advanceTimersByTime(SEARCH_DEBOUNCE_MS - 50);
      expect(navigate).toHaveBeenCalledTimes(0);
      type(box, 'host');
      vi.advanceTimersByTime(SEARCH_DEBOUNCE_MS);
      expect(navigate).toHaveBeenCalledTimes(1);
      expect(navigate).toHaveBeenCalledWith([], { queryParams: { q: 'host' }, queryParamsHandling: 'merge', replaceUrl: true });
      // The URL carries the text now.
      await set(r, { q: 'host' });
      expect(r.fake.docCalls.filter((c) => c.startsWith('tree'))).toEqual(['tree', 'tree?q=host']);
      // No Markdown file matches "host", so the group of files is gone; the counts of the others stay.
      expect(groups(r)).toEqual(['Modules 8', 'Plugins 24']);
      expect(live(r)).toBe('5 of 32 entries');
      // Every group with a match is open, and the rows left are the matching entries.
      expect(items(r).filter((i) => i.tagName === 'A').map((i) => label(i))).toEqual([
        'check_host_reachable',
        'collect_facts',
        'cidr_hosts',
        'first_host',
        'last_host',
      ]);
      expect(r.root.querySelector('[data-testid="docs-no-match"]')).toBeNull();
    });

    it('R-UI-060 a search with no match says so in the pane instead of showing an empty tree', async () => {
      const r = await open({ q: 'zzzz' });
      expect(text(r.root.querySelector('[data-testid="docs-no-match"]')!)).toContain('No entries match');
      expect(tree(r)).toBeNull();
      expect(live(r)).toBe('0 of 32 entries');
    });

    it('R-UI-060 the page that is open stays open while one searches', async () => {
      const r = await open({ doc: 'module/check_host_reachable', q: 'cidr' });
      expect(text(r.root.querySelector('[data-testid="doc-entry"] .doc-title')!)).toContain('check_host_reachable');
      expect(r.fake.docCalls.filter((c) => c.startsWith('entry'))).toEqual(['entry:module/check_host_reachable']);
    });

    it('R-UI-060 a reload of ?doc=module/check_host_reachable&q=host shows the same page and the same narrowed tree', async () => {
      const r = await open({ doc: 'module/check_host_reachable', q: 'host' });
      expect(searchBox(r).value).toBe('host');
      expect(live(r)).toBe('5 of 32 entries');
      expect(label(current(r)!)).toBe('check_host_reachable');
      expect(groups(r)).toEqual(['Modules 8', 'Plugins 24']);
    });

    it('R-UI-060 Escape clears the search field and the URL follows at once', async () => {
      const r = await open({ q: 'host' });
      const navigate = vi.spyOn(TestBed.inject(Router), 'navigate').mockResolvedValue(true);
      searchBox(r).dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
      await settle(r.fixture);
      expect(searchBox(r).value).toBe('');
      expect(navigate).toHaveBeenCalledWith([], { queryParams: { q: null }, queryParamsHandling: 'merge', replaceUrl: true });
    });

    it('R-UI-060 the field follows the URL when the back button changes it', async () => {
      const r = await open({ q: 'host' });
      expect(searchBox(r).value).toBe('host');
      await set(r, { q: undefined });
      expect(searchBox(r).value).toBe('');
      expect(r.fake.docCalls.filter((c) => c.startsWith('tree?'))).toEqual(['tree?q=host']);
    });

    it('R-UI-060 a text of one character in the URL is not sent', async () => {
      const r = await open({ q: 'h' });
      expect(r.fake.docCalls.filter((c) => c.startsWith('tree?'))).toEqual([]);
    });
  });

  describe('the states', () => {
    it('R-UI-060 pending says the documentation is being fetched and offers a refresh that reads the tree again', async () => {
      const r = await open({}, (fake) => ansibleDocsFixture(fake, commonFixture({ state: 'pending' })));
      expect(text(r.root.querySelector('[data-testid="docs-pending"]')!)).toContain('The documentation of this version is being fetched.');
      expect(tree(r)).toBeNull();
      const fx = r.fake.docFixtures.values().next().value as DocsFixture;
      fx.state = 'available';
      [...r.root.querySelectorAll('button')].find((b) => text(b) === 'Refresh')!.click();
      await settle(r.fixture);
      expect(r.fake.docCalls.filter((c) => c === 'tree')).toHaveLength(2);
      expect(groups(r)).toEqual(['Documentation 2', 'Modules 8', 'Plugins 24']);
    });

    it('R-UI-060 unavailable gives the reason in words', async () => {
      const reasons = {
        upstream_error: 'The upstream did not deliver the documentation of this version.',
        too_large: 'The documentation of this version is larger than trove keeps.',
        invalid_document: 'The upstream sent a documentation document that trove cannot read.',
      } as const;
      for (const [reason, sentence] of Object.entries(reasons)) {
        const r = await open({}, (fake) =>
          ansibleDocsFixture(fake, commonFixture({ state: 'unavailable', reason: reason as keyof typeof reasons })),
        );
        expect(text(r.root.querySelector('[data-testid="docs-unavailable"]')!), reason).toBe(sentence);
      }
    });

    it('R-UI-060 a failed read of the tree is an error with a retry, not a missing tab', async () => {
      const r = await open({}, (fake) => {
        fake.docFailure = new ApiError(503, { code: 'unavailable', detail: 'try later' }, 'rid');
      });
      expect(tabLabels(r)[0]).toBe('Documentation');
      expect(r.root.querySelector('trove-error-notice')).not.toBeNull();
    });

    it('R-UI-061 names where the documentation comes from: generated with its time, or taken from the upstream, and the repository when there are several', async () => {
      const r = await open();
      // The time is the viewer's: the same instant, written the way the page writes it.
      const at = new Date('2026-11-11T09:41:02Z');
      const pad = (n: number) => String(n).padStart(2, '0');
      const local = `${at.getFullYear()}-${pad(at.getMonth() + 1)}-${pad(at.getDate())} ${pad(at.getHours())}:${pad(at.getMinutes())}`;
      expect(text(r.root.querySelector('[data-testid="docs-source"]')!)).toBe(`Generated by trove from the archive, ${local}`);
      const several = await open({}, (fake) => {
        for (const v of fake.versionList) {
          if (v.format === 'ansible') v.repositories.push({ key: 'web/ansible-all', class: 'virtual', publishedAt: v.publishedAt });
        }
        ansibleDocsFixture(fake, commonFixture({ source: 'upstream' }));
      });
      expect(text(several.root.querySelector('[data-testid="docs-source"]')!)).toBe('Taken from the upstream · Repository: web/ansible-local');
    });

    it('R-UI-060 one pane at a time on a narrow content area: the page shows with a back action once a page is chosen', async () => {
      const r = await open();
      const box = r.root.querySelector('.browse.docs')!;
      expect(box.classList.contains('has-selection')).toBe(false);
      await set(r, { doc: 'module/check_host_reachable' });
      expect(box.classList.contains('has-selection')).toBe(true);
      const navigate = vi.spyOn(TestBed.inject(Router), 'navigate').mockResolvedValue(true);
      [...r.root.querySelectorAll('button')].find((b) => /Back to the tree/.test(text(b)))!.click();
      expect(navigate).toHaveBeenCalledWith([], { queryParams: { doc: null }, queryParamsHandling: 'merge' });
    });

    it('R-UI-060 R-UI-064 the tab passes the accessibility checks with a page open and with a search', async () => {
      const r = await open({ doc: 'module/check_host_reachable', q: 'host' });
      await expectAccessible(r.root);
    });

    it('R-UI-060 changing the version drops doc and q', async () => {
      const r = await open({ doc: 'module/check_host_reachable', q: 'host' });
      const navigate = vi.spyOn(TestBed.inject(Router), 'navigate').mockResolvedValue(true);
      const input = r.root.querySelector<HTMLInputElement>('input[list="pkg-versions"]')!;
      input.value = '1.0.0';
      input.dispatchEvent(new Event('change', { bubbles: true }));
      expect(navigate).toHaveBeenCalledWith([], { queryParams: { version: '1.0.0', doc: null, q: null }, queryParamsHandling: 'merge' });
    });
  });

  describe('the Contents tab', () => {
    it('R-UI-062 lists the 32 entries with their types, and a type filter narrows the table while the badge stays 32', async () => {
      const r = await open({ tab: 'contents' });
      const rows = () => [...r.root.querySelectorAll('[data-testid="contents-table"] tbody tr')];
      expect(rows()).toHaveLength(32);
      // Sorted by type, then name.
      const types = rows().map((tr) => text(tr.querySelectorAll('td')[0]!));
      expect(types).toEqual([...types].sort());
      const select = r.root.querySelector<HTMLSelectElement>('[data-testid="contents-type"]')!;
      expect([...select.options].map((o) => text(o))).toEqual(['All types (32)', 'filter (8)', 'module (8)', 'module_utils (16)']);
      type(select, 'module');
      await settle(r.fixture);
      expect(rows()).toHaveLength(8);
      expect(tabLabels(r)[1]).toBe('Contents 32');
      // The tree and the table are one request.
      expect(r.fake.docCalls.filter((c) => c.startsWith('tree'))).toEqual(['tree']);
    });

    it('R-UI-062 a row with documentation links to its page in the Documentation tab, one without is plain text', async () => {
      const r = await open({ tab: 'contents' });
      const row = (name: string) => [...r.root.querySelectorAll('[data-testid="contents-table"] tbody tr')].find((tr) => text(tr.querySelector('th')!) === name)!;
      const link = row('check_host_reachable').querySelector('a')!;
      expect(link.getAttribute('href')).toContain('tab=documentation');
      expect(link.getAttribute('href')).toContain('doc=module%2Fcheck_host_reachable');
      expect(text(row('check_host_reachable'))).toContain('Checks whether a host answers');
      expect(row('is_cidr').querySelector('a')).toBeNull();
    });

    it('R-UI-062 shows 5,000 entries in pages of 100 with Load more', async () => {
      const r = await open({ tab: 'contents' }, (fake) =>
        ansibleDocsFixture(
          fake,
          commonFixture({ entries: Array.from({ length: 250 }, (_, i) => bareEntry('module', `m${String(i).padStart(4, '0')}`)) }),
        ),
      );
      const rows = () => r.root.querySelectorAll('[data-testid="contents-table"] tbody tr').length;
      expect(rows()).toBe(CONTENTS_PAGE);
      const more = () => [...r.root.querySelectorAll('button')].find((b) => text(b) === 'Load more')!;
      more().click();
      await settle(r.fixture);
      expect(rows()).toBe(2 * CONTENTS_PAGE);
      more().click();
      await settle(r.fixture);
      expect(rows()).toBe(250);
      expect([...r.root.querySelectorAll('button')].some((b) => text(b) === 'Load more')).toBe(false);
    });

    it('R-UI-062 says what the state is when the documentation is pending or unavailable', async () => {
      const pending = await open({ tab: 'contents' }, (fake) => ansibleDocsFixture(fake, commonFixture({ state: 'pending' })));
      expect(text(pending.root.querySelector('[data-testid="contents-pending"]')!)).toContain('being fetched');
      const gone = await open({ tab: 'contents' }, (fake) =>
        ansibleDocsFixture(fake, commonFixture({ state: 'unavailable', reason: 'too_large' })),
      );
      expect(text(gone.root.querySelector('[data-testid="contents-unavailable"]')!)).toContain('larger than trove keeps');
    });

    it('R-UI-062 the tab passes the accessibility checks', async () => {
      const r = await open({ tab: 'contents' });
      await expectAccessible(r.root);
    });
  });

  describe('the Import log tab', () => {
    it('R-UI-063 asks for the tasks of this collection and this version of the local repository that holds it', async () => {
      const r = await renderTasks([importTask({ version: '1.2.0' })]);
      const api = r.get(AnsibleApi) as FakeAnsibleApi;
      expect(api.filters.at(-1)).toEqual({ package: 'acme.tools', version: '1.2.0' });
      expect(tabLabels(r as unknown as Opened).at(-1)).toBe('Import log');
    });

    it('R-UI-063 lists the tasks of the version with the warnings of the generator among the messages', async () => {
      const r = await renderTasks([
        importTask({
          version: '1.2.0',
          messages: [
            { level: 'INFO', message: 'Collection acme.tools 1.2.0 stored', time: '2026-11-09T09:00:01Z' },
            { level: 'WARNING', message: 'plugins/modules/x.py: documentation literal not terminated <b>x</b>', time: '2026-11-09T09:00:01Z' },
          ],
        }),
        importTask({ id: '6f1c0000-0000-4000-8000-000000000009', version: '1.0.0' }),
      ]);
      const sections = [...r.root.querySelectorAll('section.task')];
      expect(sections).toHaveLength(1);
      expect(sections[0]?.getAttribute('data-state')).toBe('completed');
      expect(text(sections[0]!.querySelector('.badge')!)).toContain('Completed');
      expect([...sections[0]!.querySelectorAll('.messages li')].map((li) => text(li))).toEqual([
        'INFO Collection acme.tools 1.2.0 stored',
        'WARNING plugins/modules/x.py: documentation literal not terminated <b>x</b>',
      ]);
      expect(sections[0]!.querySelector('.messages b')).toBeNull();
      await expectAccessible(r.root);
    });

    it('R-UI-063 says so when no task of the version is kept', async () => {
      const r = await renderTasks([]);
      expect(text(r.root.querySelector('[data-testid="import-log-empty"]')!)).toContain('No import task of this version is kept');
    });

    it('R-UI-063 a failed task shows its reason', async () => {
      const r = await renderTasks([
        importTask({ version: '1.2.0', state: 'failed', error: { code: 'invalid_archive', description: 'MANIFEST.json is missing' } }),
      ]);
      expect(text(r.root.querySelector('[data-testid="import-log-error"]')!)).toBe('MANIFEST.json is missing');
      expect(r.root.querySelector('section.task .badge')?.classList.contains('bad')).toBe(true);
    });

    it('R-UI-063 a version that no local repository holds has no Import log tab', async () => {
      const r = await open({}, (fake) => {
        for (const v of fake.versionList) {
          if (v.format === 'ansible') v.repositories = [{ key: 'web/ansible-remote', class: 'remote', publishedAt: v.publishedAt }];
        }
      });
      expect(tabLabels(r)).not.toContain('Import log');
    });

    async function renderTasks(tasks: ReturnType<typeof importTask>[]) {
      const r = await renderScreen(
        PackageDetail,
        inProject({
          inputs: { format: 'ansible', name: 'acme.tools', tab: 'import-log' },
          setup: (get) => {
            const fake = get(PackagesApi) as FakePackagesApi;
            ansibleFixture(fake);
            ansibleDocsFixture(fake);
            (get(AnsibleApi) as FakeAnsibleApi).tasks = tasks;
          },
        }),
      );
      return r;
    }
  });

  describe('the Metadata tab', () => {
    it('R-UI-063 lists the dependencies with a link for a collection the installation holds and muted text for another', async () => {
      const r = await open({ tab: 'metadata', version: '1.2.0' }, (fake) => {
        fake.packages.push({ ...fake.packages[0]!, id: 'pkg_01JBQ9GENERAL', format: 'ansible', name: 'community.general' });
        for (const v of fake.versionList) {
          if (v.format === 'ansible' && v.version === '1.2.0') {
            v.metadata = { ...v.metadata, dependencies: { 'community.general': '>=8.0.0', 'other.missing': '*' } };
          }
        }
      });
      const dts = [...r.root.querySelectorAll('.pkg-tabs dl.facts dt')];
      const held = dts.find((dt) => text(dt) === 'community.general')!;
      expect(held.querySelector('a')?.getAttribute('href')).toBe('/projects/web/packages/ansible/community.general');
      const missing = dts.find((dt) => text(dt) === 'other.missing')!;
      expect(missing.querySelector('a')).toBeNull();
      expect(missing.querySelector('.secondary-inline')).not.toBeNull();
      expect(text(held.nextElementSibling!)).toBe('>=8.0.0');
    });

    it('R-UI-063 the documentation link is labelled Docs site, and a link with another scheme than http or https is text', async () => {
      const r = await open({ tab: 'metadata', version: '1.2.0' });
      const links = [...r.root.querySelectorAll('.pkg-tabs dl.facts')].find((dl) => text(dl).includes('Docs site'))!;
      expect([...links.querySelectorAll('dt')].map((dt) => text(dt))).toContain('Docs site');
      expect(text(r.root)).not.toMatch(/\bDocumentation\s+https/);
      const bad = await open({ tab: 'metadata', version: '1.2.0' }, (fake) => {
        for (const v of fake.versionList) {
          if (v.format === 'ansible') v.metadata = { ...v.metadata, documentationUrl: 'javascript:alert(1)' };
        }
      });
      expect(text(bad.root)).toContain('javascript:alert(1)');
      expect(bad.root.querySelector('a[href*="javascript"]')).toBeNull();
    });
  });
});
