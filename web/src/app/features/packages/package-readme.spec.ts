import { PackageReadme } from '../../api/packages-api';
import { expectAccessible, renderScreen, settle, text } from '../../testing/screen';
import { PackageReadmeView, headingSlug } from './package-readme';

async function show(html: string, over: Partial<PackageReadme> = {}) {
  const readme: PackageReadme = {
    contentType: 'text/markdown',
    html,
    truncated: false,
    externalImagesBlocked: 0,
    ...over,
  };
  const r = await renderScreen(PackageReadmeView, { inputs: { readme } });
  return { ...r, body: r.root.querySelector('.readme') as HTMLElement };
}

describe('README rendering', () => {
  beforeEach(() => {
    // Angular reports what its sanitizer removed; the specs below expect that.
    vi.spyOn(console, 'warn').mockImplementation(() => undefined);
    (window as unknown as Record<string, unknown>)['__readme'] = undefined;
  });
  afterEach(() => vi.restoreAllMocks());

  it('R-UI-008 renders the headings, lists, tables and code of the server HTML', async () => {
    const { body, root } = await show(
      '<h1 id="user-content-t">Title</h1><p>text <code>x</code></p><ul><li>a</li></ul>' +
        '<table><tr><td colspan="2" align="center">c</td></tr></table><pre><code class="language-ts">1</code></pre>',
    );
    expect(body.querySelector('h1')?.textContent).toBe('Title');
    expect(body.querySelector('td')?.getAttribute('colspan')).toBe('2');
    expect(body.querySelector('code.language-ts')).not.toBeNull();
    await expectAccessible(root);
  });

  it('R-UI-008 a script element and an event-handler attribute are not executable in the DOM (the second layer after the server)', async () => {
    const { body } = await show(
      '<p>ok</p><script>window.__readme = 1</script><img src="x" onerror="window.__readme = 2">' +
        '<a href="#" onclick="window.__readme = 3">c</a><p onmouseover="window.__readme = 4">m</p>',
    );
    expect(body.querySelector('script')).toBeNull();
    for (const el of body.querySelectorAll('*')) {
      expect(el.getAttributeNames().filter((n) => n.startsWith('on')), el.outerHTML).toEqual([]);
    }
    expect((window as unknown as Record<string, unknown>)['__readme']).toBeUndefined();
  });

  it('R-UI-008 javascript:, vbscript: and data: link targets never reach an href', async () => {
    const { body } = await show(
      '<a href="javascript:window.__readme=1">j</a><a href="vbscript:msgbox(1)">v</a>' +
        '<a href="data:text/html,<script>1</script>">d</a><a href="  JaVaScRiPt:1">s</a>',
    );
    for (const a of body.querySelectorAll('a')) {
      const href = (a.getAttribute('href') ?? '').trim().toLowerCase();
      expect(href.startsWith('javascript:'), href).toBe(false);
      expect(href.startsWith('vbscript:'), href).toBe(false);
      expect(href.startsWith('data:'), href).toBe(false);
    }
  });

  it('R-UI-008 iframe, object, embed, form, input, svg, math, base, meta, link and style are removed', async () => {
    const { body } = await show(
      '<p>x</p><iframe src="https://evil.example"></iframe><object data="x"></object><embed src="x">' +
        '<form action="https://evil.example"><input name="q"><button>go</button></form>' +
        '<svg onload="1"><script>1</script></svg><math><mi>x</mi></math>' +
        '<base href="https://evil.example/"><meta http-equiv="refresh" content="0;url=https://evil.example">' +
        '<link rel="stylesheet" href="https://evil.example/x.css"><style>*{display:none}</style>',
    );
    for (const tag of ['iframe', 'object', 'embed', 'form', 'input', 'svg', 'math', 'base', 'meta', 'link', 'style']) {
      expect(body.querySelector(tag), tag).toBeNull();
    }
  });

  it('R-UI-008 style attributes are removed', async () => {
    const { body } = await show('<p style="position:fixed;inset:0">x</p><div style="background:url(https://evil.example/a.png)">y</div>');
    expect(body.querySelector('[style]')).toBeNull();
  });

  it('R-UI-008 ids and names of the HTML never reach the DOM (no DOM clobbering); only the headings carry an id the component derives', async () => {
    const { body } = await show(
      '<a id="__proto__" name="cookie">a</a><img name="getElementById" src="https://img.example/x.png"><p id="forms">p</p><h2 id="evil">Real Heading</h2>',
    );
    expect(body.querySelector('[id="__proto__"]')).toBeNull();
    expect(body.querySelector('[name]')).toBeNull();
    expect(body.querySelector('#forms')).toBeNull();
    expect(body.querySelector('#evil')).toBeNull();
    expect([...body.querySelectorAll('[id]')].map((e) => e.id)).toEqual(['user-content-real-heading']);
    expect((document as unknown as Record<string, unknown>)['cookie']).not.toBe(body.querySelector('a'));
  });

  it('R-UI-008 headings get user-content-<slug> anchors like a table of contents expects: punctuation dropped, repeated names numbered', async () => {
    const { body } = await show('<h1>Getting started!</h1><h2>Usage</h2><h2>Usage</h2><h3>API &amp; Options (v2)</h3>');
    expect([...body.querySelectorAll('h1, h2, h3')].map((h) => h.id)).toEqual([
      'user-content-getting-started',
      'user-content-usage',
      'user-content-usage-1',
      'user-content-api--options-v2',
    ]);
  });

  /**
   * The corpus of the heading rule. The same table is in internal/packages/readme/render_test.go
   * (slugCorpus), which checks the server's renderer against it; the expected anchors are what this
   * function yields, so a link of a table of contents the server wrote resolves here (Q-UI-76).
   */
  const SLUG_CORPUS: readonly (readonly [string, string])[] = [
  ["Usage", "usage"],
  ["  Getting Started  ", "getting-started"],
  ["What's new? (v2)", "whats-new-v2"],
  ["snake_case and-dash", "snake_case-and-dash"],
  ["Über uns & mehr!", "über-uns--mehr"],
  ["日本語 の 見出し", "日本語-の-見出し"],
  ["A\u00a0B", "a-b"],
  ["A\u2003B", "a-b"],
  ["A\u0085B", "ab"],
  ["A\ufeffB", "a-b"],
  ["\ufeffX\ufeff", "x"],
  ["Tab\tTab", "tab-tab"],
  ["x\u2028y", "x-y"],
  ["İstanbul", "istanbul"],
  ["ΟΔΟΣ", "οδος"],
  ["ΟΔΟΣ ΣΑΣ", "οδος-σας"],
  ["Σ", "σ"],
  ["ΑΣ.", "ας"],
  ["Aσς", "aσς"],
  ["ǅ Title", "ǆ-title"],
  ["Ⅻ roman", "ⅻ-roman"],
  ["x²", "x²"],
  ["٣ arabic", "٣-arabic"],
  ["???", ""],
  ["!!!", ""],
  ["a\u200bb", "ab"],
  ["\u180eX", "x"],
  ["ﬃ ligature", "ﬃ-ligature"],
  ["Ǆ", "ǆ"],
  ["ᾈ", "ᾀ"],
  ["e\u0301", "e"],
  ["😀 emoji", "-emoji"],
  ["ΑΣ\u0301", "ας"],
  ["Σ'Α", "σα"],
  ["ΑΣ\u2019", "ας"],
  ];

  it('R-UI-008 the heading rule gives the anchors the server derives for the same headings', () => {
    for (const [heading, anchor] of SLUG_CORPUS) {
      expect(headingSlug(heading, new Map()), JSON.stringify(heading)).toBe(anchor);
    }
    const seen = new Map<string, number>();
    expect(['Usage', 'Usage', 'Usage', 'Other', 'Usage'].map((h) => headingSlug(h, seen))).toEqual([
      'usage',
      'usage-1',
      'usage-2',
      'other',
      'usage-3',
    ]);
    const empty = new Map<string, number>();
    expect([headingSlug('???', empty), headingSlug('!!!', empty)]).toEqual(['', '-1']);
  });

  it('R-UI-008 links to relative paths, vbscript: and other schemes lose their target and keep their text', async () => {
    const { body } = await show('<a href="docs/guide.md">guide</a><a href="vbscript:msgbox(1)">v</a><a href="ftp://example.com/x">f</a>');
    for (const a of body.querySelectorAll('a')) expect(a.hasAttribute('href')).toBe(false);
    expect(body.textContent).toContain('guide');
  });

  it('R-UI-008 an image is kept only with an https source or a path of this origin; data:, http: and protocol-relative sources go', async () => {
    const { body } = await show(
      '<img src="https://img.example/a.png" alt="a"><img src="/ui/assets/b.png" alt="b"><img src="data:image/png;base64,AAAA" alt="c">' +
        '<img src="http://img.example/d.png" alt="d"><img src="//img.example/e.png" alt="e">',
    );
    expect([...body.querySelectorAll('img')].map((i) => i.alt)).toEqual(['a', 'b']);
  });

  it('R-UI-008 links that leave trove carry rel nofollow noopener noreferrer and no target', async () => {
    const { body } = await show(
      '<a href="https://example.com/a" target="_blank">a</a><a href="mailto:team@example.com" rel="author">m</a><a href="#user-content-x">in page</a>',
    );
    const [external, mail, inPage] = [...body.querySelectorAll('a')];
    expect(external?.getAttribute('rel')).toBe('nofollow noopener noreferrer');
    expect(external?.hasAttribute('target')).toBe(false);
    expect(mail?.getAttribute('rel')).toBe('nofollow noopener noreferrer');
    expect(inPage?.getAttribute('rel')).toBeNull();
  });

  it('R-UI-008 a link to #user-content-… scrolls inside the README and does not navigate', async () => {
    const { body } = await show('<a href="#user-content-usage">Usage</a><h2>Usage</h2>');
    const target = body.querySelector<HTMLElement>('#user-content-usage')!;
    const scroll = vi.fn();
    target.scrollIntoView = scroll;
    const click = new MouseEvent('click', { bubbles: true, cancelable: true });
    body.querySelector('a')!.dispatchEvent(click);
    expect(click.defaultPrevented).toBe(true);
    expect(scroll).toHaveBeenCalledOnce();
  });

  it('R-UI-008 a fragment link to an id that is not prefixed scrolls nowhere and does not navigate either', async () => {
    const { body } = await show('<a href="#top">x</a><h2>T</h2>');
    const click = new MouseEvent('click', { bubbles: true, cancelable: true });
    body.querySelector('a')!.dispatchEvent(click);
    expect(click.defaultPrevented).toBe(true);
  });

  it('R-UI-008 code blocks take focus so that a keyboard user can scroll them', async () => {
    const { body } = await show('<pre><code>a very long line</code></pre>');
    expect(body.querySelector('pre')?.getAttribute('tabindex')).toBe('0');
  });

  it('R-UI-008 a truncated README and blocked external images are announced as notes', async () => {
    const { root, fixture } = await show('<p>x</p>', { truncated: true, externalImagesBlocked: 2 });
    await settle(fixture);
    const t = text(root);
    expect(t).toContain('This README is shortened');
    expect(t).toContain('2 external images are shown as links.');
    const one = await show('<p>x</p>', { externalImagesBlocked: 1 });
    expect(text(one.root)).toContain('One external image is shown as a link.');
  });

  it('R-UI-008 a version without a README renders nothing', async () => {
    const r = await renderScreen(PackageReadmeView, { inputs: { readme: null } });
    expect(r.root.querySelector('.readme')).toBeNull();
  });
});
