import { TestBed } from '@angular/core/testing';
import { Title } from '@angular/platform-browser';
import { Router, TitleStrategy, provideRouter } from '@angular/router';
import { Notifier } from './notifier';
import { BrowserStorage } from './storage';
import { TroveTitleStrategy } from './title-strategy';
import { diffFields } from '../shared/conflict-dialog';
import { browseMatcher } from '../features/browse/browse.routes';
import { UrlSegment } from '@angular/router';

describe('BrowserStorage', () => {
  it('R-UI-006 stores and reads through the wrapper', () => {
    const s = TestBed.inject(BrowserStorage);
    expect(s.available('local')).toBe(true);
    expect(s.set('local', 'trove.k', 'v')).toBe(true);
    expect(s.get('local', 'trove.k')).toBe('v');
    s.remove('local', 'trove.k');
    expect(s.get('local', 'trove.k')).toBeNull();
  });

  it('R-UI-006 blocked storage only makes the features disappear: reads answer null, writes false', () => {
    const s = TestBed.inject(BrowserStorage);
    const spy = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new DOMException('blocked', 'SecurityError');
    });
    expect(s.available('session')).toBe(false);
    expect(s.set('session', 'k', 'v')).toBe(false);
    spy.mockRestore();
    const get = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new DOMException('blocked', 'SecurityError');
    });
    expect(s.get('local', 'k')).toBeNull();
    get.mockRestore();
  });
});

describe('Notifier', () => {
  it('R-UI-042 keeps errors and notices with an action until dismissed, and lets a plain confirmation expire', () => {
    vi.useFakeTimers();
    const n = TestBed.inject(Notifier);
    n.status('Saved');
    n.status('Deleted', { label: 'Undo', run: () => undefined });
    n.alert('Failed');
    expect(n.notices().map((x) => x.kind)).toEqual(['status', 'status', 'alert']);
    vi.advanceTimersByTime(9000);
    expect(n.notices().map((x) => x.text)).toEqual(['Deleted', 'Failed']);
    vi.useRealTimers();
  });
});

describe('TroveTitleStrategy', () => {
  it('R-UI-003 sets "Title – trove" from the route title', async () => {
    TestBed.configureTestingModule({
      providers: [
        provideRouter([{ path: 'x', title: 'Packages', children: [] }]),
        { provide: TitleStrategy, useExisting: TroveTitleStrategy },
      ],
    });
    await TestBed.inject(Router).navigateByUrl('/x');
    expect(TestBed.inject(Title).getTitle()).toBe('Packages – trove');
  });
});

describe('browseMatcher', () => {
  const seg = (...p: string[]) => p.map((x) => new UrlSegment(x, {}));

  it('R-UI-003 takes the first segment as repository and joins the rest as the path', () => {
    const m = browseMatcher(seg('npm-local', '@team-a', 'ui'), null as never, null as never);
    expect(m?.posParams?.['repo']?.path).toBe('npm-local');
    expect(m?.posParams?.['path']?.path).toBe('@team-a/ui');
  });

  it('R-UI-003 matches a repository root with an empty path and nothing without segments', () => {
    expect(browseMatcher(seg('npm-local'), null as never, null as never)?.posParams?.['path']?.path).toBe('');
    expect(browseMatcher([], null as never, null as never)).toBeNull();
  });
});

describe('conflict diff', () => {
  it('R-UI-042 lists only the fields that differ, theirs against yours', () => {
    const rows = diffFields({
      mine: { description: 'mine', enabled: true, extra: 1 },
      theirs: { description: 'theirs', enabled: true },
    });
    expect(rows).toEqual([
      { field: 'description', mine: '"mine"', theirs: '"theirs"' },
      { field: 'extra', mine: '1', theirs: '' },
    ]);
  });
});
