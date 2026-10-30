import { formatSize } from './format-size';
import { relativeTime } from './format-time';
import { packageCommands, safeHttpUrl } from './package-links';

describe('package links', () => {
  it('R-UI-007 only http and https values become links; every other scheme stays text', () => {
    expect(safeHttpUrl('https://example.com/team-a/ui')).toBe('https://example.com/team-a/ui');
    expect(safeHttpUrl('http://example.com')).toBe('http://example.com/');
    expect(safeHttpUrl('  https://example.com/x  ')).toBe('https://example.com/x');
    for (const bad of [
      'javascript:alert(1)',
      ' javascript:alert(1)',
      'JaVaScRiPt:alert(1)',
      'java\tscript:alert(1)',
      'vbscript:msgbox(1)',
      'data:text/html,<script>alert(1)</script>',
      'git+https://example.com/x.git',
      'ftp://example.com/x',
      'file:///etc/passwd',
      '//example.com/x',
      '/relative/path',
      'example.com',
      '',
      '   ',
    ]) {
      expect(safeHttpUrl(bad), bad).toBeNull();
    }
    expect(safeHttpUrl(undefined)).toBeNull();
    expect(safeHttpUrl({ url: 'https://example.com' })).toBeNull();
  });

  it('R-UI-003 a package page keeps the slash of a scoped name as a path separator', () => {
    expect(packageCommands('npm', '@team-a/ui')).toEqual(['/packages', 'npm', '@team-a', 'ui']);
    expect(packageCommands('nuget', 'Contoso.Logging')).toEqual(['/packages', 'nuget', 'Contoso.Logging']);
  });

  it('R-UI-006 the updated time is relative in the language of the build; a time within a minute reads "now"', () => {
    const now = Date.parse('2026-10-30T12:00:00Z');
    expect(relativeTime('2026-10-27T12:00:00Z', 'en', now)).toBe('3 days ago');
    expect(relativeTime('2026-10-30T09:00:00Z', 'en', now)).toBe('3 hours ago');
    expect(relativeTime('2026-08-30T12:00:00Z', 'en', now)).toBe('2 months ago');
    expect(relativeTime('2026-10-30T11:59:40Z', 'en', now)).toBe('this minute');
    expect(relativeTime('2026-10-27T12:00:00Z', 'de', now)).toBe('vor 3 Tagen');
  });

  it('R-UI-007 sizes are shown in binary units', () => {
    expect(formatSize(0)).toBe('0 B');
    expect(formatSize(1023)).toBe('1023 B');
    expect(formatSize(48213)).toBe('47.1 KiB');
    expect(formatSize(5 * 1024 * 1024)).toBe('5.0 MiB');
  });
});
