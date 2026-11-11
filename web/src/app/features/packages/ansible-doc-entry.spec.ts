import { PackageDocEntry } from '../../api/packages-api';
import { CHECK_HOST_REACHABLE, bareEntry } from '../../api/testing/fakes-ansible-docs';
import { expectAccessible, renderScreen, settle, text } from '../../testing/screen';
import { AnsibleDocEntry } from './ansible-doc-entry';

async function show(entry: PackageDocEntry = CHECK_HOST_REACHABLE) {
  const r = await renderScreen(AnsibleDocEntry, { inputs: { entry } });
  return { ...r, rows: () => [...r.root.querySelectorAll<HTMLTableRowElement>('tbody tr')] };
}

const headings = (root: Element) => [...root.querySelectorAll('section h3')].map((h) => text(h));

describe('An entry page of the documentation', () => {
  beforeEach(() => vi.spyOn(console, 'warn').mockImplementation(() => undefined));
  afterEach(() => vi.restoreAllMocks());

  it('R-UI-061 shows the head and the sections in the order ansible-doc prints them, each only with content', async () => {
    const r = await show();
    expect(text(r.root.querySelector('.doc-title')!)).toContain('hans_fischer.common.check_host_reachable');
    expect(r.root.querySelector('.doc-title trove-copy-button button')?.getAttribute('aria-label')).toBe('Copy the full name');
    expect(text(r.root.querySelector('.doc-short')!)).toBe('Checks whether a host answers');
    expect(text(r.root.querySelector('.doc-facts')!)).toContain('added in 1.1.0');
    expect(headings(r.root)).toEqual([
      'Synopsis',
      'Parameters',
      'Notes',
      'Requirements',
      'Attributes',
      'See also',
      'Examples',
      'Return values',
      'Authors',
    ]);
    // The prose is the server's HTML: the code element and the link are there.
    expect(r.root.querySelector('#doc-synopsis ~ trove-doc-prose code')?.textContent).toBe('host');
    await expectAccessible(r.root);
  });

  it('R-UI-061 a section without content is not drawn', async () => {
    const r = await show({ type: 'module', name: 'm', fullName: 'a.b.m', hasDocs: true, source: 'generated', description: ['Does it.'] });
    expect(headings(r.root)).toEqual(['Synopsis']);
  });

  it('R-UI-061 the parameters are a table with the columns of the page, a text badge for required and the aliases under the name', async () => {
    const r = await show();
    expect([...r.root.querySelectorAll('#doc-parameters ~ trove-data-table thead th')].map((t) => text(t))).toEqual([
      'Name',
      'Type',
      'Required',
      'Default',
      'Choices',
      'Description',
    ]);
    const host = r.rows()[0]!;
    expect(text(host)).toContain('host');
    expect(text(host)).toContain('aliases: hostname');
    expect(host.querySelector('.badge')?.textContent?.trim()).toBe('required');
    // A default of 1000 is shown as 1000, the choices one by one.
    const port = r.rows()[1]!;
    expect(text(port.cells[3]!)).toBe('22');
    expect([...port.querySelectorAll('.choices code')].map((c) => c.textContent)).toEqual(['22', '443']);
  });

  it('R-UI-061 parameters nest three levels deep, each level 16 px further in, with a label for the screen reader', async () => {
    const r = await show();
    const names = r.rows().map((tr) => [tr.getAttribute('data-depth'), tr.querySelector('code')?.textContent]);
    expect(names.slice(2, 6)).toEqual([
      ['0', 'retry'],
      ['1', 'count'],
      ['1', 'backoff'],
      ['2', 'seconds'],
    ]);
    const deepest = r.rows().find((tr) => tr.getAttribute('data-depth') === '2')!;
    expect(deepest.querySelector('th')!.style.paddingInlineStart).toContain('32px');
    expect(text(deepest.querySelector('th .visually-hidden')!)).toBe('suboptions of backoff:');
  });

  it('R-UI-061 a parameter with suboptions folds and unfolds, and a new page starts open', async () => {
    const r = await show();
    const count = () => r.rows().length;
    const all = count();
    const retry = r.rows().find((tr) => tr.querySelector('code')?.textContent === 'retry')!;
    const fold = retry.querySelector<HTMLButtonElement>('button.fold')!;
    expect(fold.getAttribute('aria-expanded')).toBe('true');
    expect(fold.getAttribute('aria-label')).toBe('Hide the suboptions of retry');
    fold.click();
    await settle(r.fixture);
    expect(count()).toBe(all - 4);
    const again = r.rows().find((tr) => tr.querySelector('code')?.textContent === 'retry')!.querySelector('button.fold')!;
    expect(again.getAttribute('aria-expanded')).toBe('false');
    expect(again.getAttribute('aria-label')).toBe('Show the suboptions of retry');
    r.fixture.componentRef.setInput('entry', { ...CHECK_HOST_REACHABLE });
    await settle(r.fixture);
    expect(count()).toBe(all);
  });

  it('R-UI-061 a parameter named <img src=x onerror=1> and a default of <script> appear as that text, never as markup', async () => {
    const r = await show();
    const hostile = r.rows().find((tr) => tr.querySelector('code')?.textContent === '<img src=x onerror=1>')!;
    expect(hostile).toBeDefined();
    expect(text(hostile.cells[3]!)).toBe('<script>alert(1)</script>');
    expect(r.root.querySelector('img')).toBeNull();
    expect(r.root.querySelector('script')).toBeNull();
    expect(r.root.querySelectorAll('[onerror]')).toHaveLength(0);
  });

  it('R-UI-061 prose from the server is bound as sanitized HTML: a script element and an event handler do not survive', async () => {
    const r = await show({
      ...CHECK_HOST_REACHABLE,
      description: ['<p>ok <code>x</code></p><script>window.__doc = 1</script><img src="https://x/y.png" onerror="window.__doc = 2">'],
    });
    const prose = r.root.querySelector('#doc-synopsis ~ trove-doc-prose')!;
    expect(prose.querySelector('script')).toBeNull();
    expect(prose.querySelector('[onerror]')).toBeNull();
    expect(prose.querySelector('code')?.textContent).toBe('x');
    expect((window as unknown as Record<string, unknown>)['__doc']).toBeUndefined();
  });

  it('R-UI-061 the examples are a code block with a copy button, the return values a table with a sample as a block when it is long', async () => {
    const r = await show();
    const code = r.root.querySelector('#doc-examples ~ .code-wrap pre.code-block')!;
    expect(code.textContent).toContain('hans_fischer.common.check_host_reachable:');
    expect(code.getAttribute('tabindex')).toBe('0');
    expect(r.root.querySelector('#doc-examples ~ .code-wrap trove-copy-button button')?.getAttribute('aria-label')).toBe('Copy the examples');
    const returns = [...r.root.querySelectorAll<HTMLTableRowElement>('#doc-returns ~ trove-data-table tbody tr')];
    expect(returns.map((tr) => tr.querySelector('code')?.textContent)).toEqual(['reachable', 'details', 'rtt_ms']);
    // `true` stays inline, an object is a block with the copy button.
    expect((returns[0]!.cells[3]!.querySelector('code') as HTMLElement).textContent).toBe('true');
    expect(returns[1]!.cells[3]!.querySelector('pre.code-block')?.textContent).toContain('"rtt_ms": 12');
  });

  it('R-UI-061 a see-also link is a link only for http and https', async () => {
    const r = await show({
      ...CHECK_HOST_REACHABLE,
      seealso: [
        { description: 'ok', link: 'https://example.com/reference' },
        { description: 'bad', link: 'javascript:alert(1)' },
      ],
    });
    const links = [...r.root.querySelectorAll<HTMLAnchorElement>('#doc-seealso ~ ul a')];
    expect(links.map((a) => a.getAttribute('href'))).toEqual(['https://example.com/reference']);
    expect(links[0]?.getAttribute('rel')).toBe('nofollow noopener noreferrer');
    expect(r.root.innerHTML).not.toContain('href="javascript:');
  });

  it('R-UI-061 a deprecated plugin carries a warn badge and its notice', async () => {
    const r = await show({
      ...CHECK_HOST_REACHABLE,
      deprecated: { why: 'Replaced by <code>other</code>.', removedIn: '3.0.0', alternative: 'Use other.' },
    });
    const badge = r.root.querySelector('.doc-facts .badge');
    expect(badge?.classList.contains('warn')).toBe(true);
    expect(text(badge!)).toContain('Deprecated');
    const note = text(r.root.querySelector('.doc-deprecation')!);
    expect(note).toContain('Replaced by other.');
    expect(note).toContain('Removed in 3.0.0');
  });

  it('R-UI-061 an entry without documentation shows the head and one line, generated or taken from the upstream, and no empty section', async () => {
    const generated = await show(bareEntry('filter', 'is_cidr'));
    expect(text(generated.root.querySelector('[data-testid="doc-no-docs"]')!)).toBe('This plugin has no documentation in its source.');
    expect(generated.root.querySelectorAll('section')).toHaveLength(0);
    expect(text(generated.root)).toContain('hans_fischer.common.is_cidr');
    const upstream = await show(bareEntry('filter', 'is_cidr', 'upstream'));
    expect(text(upstream.root.querySelector('[data-testid="doc-no-docs"]')!)).toBe('The upstream sent no documentation for this plugin.');
  });

  it('R-UI-061 a role shows its README and no Parameters section', async () => {
    const r = await show({
      type: 'role',
      name: 'web',
      fullName: 'hans_fischer.common.web',
      hasDocs: true,
      source: 'generated',
      readmeHtml: '<h1>web</h1><p>Installs the web server.</p>',
    });
    expect(r.root.querySelector('trove-package-readme .readme h1')?.textContent).toBe('web');
    expect(headings(r.root)).toEqual([]);
    expect(r.root.querySelector('#doc-parameters')).toBeNull();
  });

  it('R-UI-061 a role of an upstream that sends a README and no doc_strings (hasDocs false) still shows its README', async () => {
    const r = await show({
      type: 'role',
      name: 'web',
      fullName: 'hans_fischer.common.web',
      hasDocs: false,
      source: 'upstream',
      readmeHtml: '<h1>web</h1><p>Installs the web server.</p>',
    });
    expect(r.root.querySelector('trove-package-readme .readme h1')?.textContent).toBe('web');
    expect(r.root.querySelector('[data-testid="doc-no-docs"]')).toBeNull();
  });

  it('R-UI-061 names the documentation fragments that were not merged', async () => {
    const r = await show({ ...CHECK_HOST_REACHABLE, extendsFragments: ['community.general.attributes'] });
    expect(text(r.root.querySelector('[data-testid="doc-fragments"]')!)).toContain('community.general.attributes');
  });
});
