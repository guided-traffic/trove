import { parseBannerText, safeHref } from './banner-text';

describe('banner text', () => {
  it('R-OPS-025 splits a message into text and https links', () => {
    expect(parseBannerText('Read [the status page](https://status.example.com/now) first, then [docs](https://docs.example.com).')).toEqual([
      { kind: 'text', text: 'Read ' },
      { kind: 'link', text: 'the status page', href: 'https://status.example.com/now' },
      { kind: 'text', text: ' first, then ' },
      { kind: 'link', text: 'docs', href: 'https://docs.example.com/' },
      { kind: 'text', text: '.' },
    ]);
  });

  it('R-OPS-025 a message without markup is one text part, and an empty one has none', () => {
    expect(parseBannerText('Plain <b>text</b> & more')).toEqual([{ kind: 'text', text: 'Plain <b>text</b> & more' }]);
    expect(parseBannerText('')).toEqual([]);
  });

  it('R-OPS-025 a link to anything but https stays in the text as written, so nothing runs', () => {
    for (const bad of [
      'javascript:alert(1)',
      'JavaScript:alert(1)',
      'data:text/html,<script>1</script>',
      'http://plain.example.com',
      '//protocol-relative.example.com',
      '/relative/path',
      'https://user:secret@example.com/',
      'vbscript:x',
    ]) {
      const message = `see [status](${bad})`;
      expect(parseBannerText(message), bad).toEqual([{ kind: 'text', text: message }]);
    }
  });

  it('R-OPS-025 the message javascript link of the acceptance criterion is rendered as plain text', () => {
    const parts = parseBannerText('see [status](javascript:alert(1))');
    expect(parts.every((p) => p.kind === 'text')).toBe(true);
    expect(parts.map((p) => p.text).join('')).toContain('[status](javascript:alert(1)');
  });

  it('R-OPS-025 only https passes safeHref, in any letter case', () => {
    expect(safeHref('HTTPS://Example.com/a')).toBe('https://example.com/a');
    expect(safeHref('http://example.com')).toBeNull();
    expect(safeHref('not a url')).toBeNull();
  });

  it('R-OPS-025 markup that is not a link is left alone', () => {
    const message = '[no target] and (no label) and [a](b c) and [broken(https://example.com)';
    expect(parseBannerText(message)).toEqual([{ kind: 'text', text: message }]);
  });
});
