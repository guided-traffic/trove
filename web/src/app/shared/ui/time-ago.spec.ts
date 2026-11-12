import { Component, signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { expectAccessible, settle, text } from '../../testing/screen';
import { TimeAgo, timeAgo } from './time-ago';

const NOW = Date.parse('2026-11-12T14:00:00Z');
const before = (ms: number) => new Date(NOW - ms).toISOString();

describe('Time ago', () => {
  it.each([
    [0, 'just now'],
    [59_000, 'just now'],
    [60_000, '1 min ago'],
    [59 * 60_000, '59 min ago'],
    [60 * 60_000, '1 h ago'],
    [23 * 3_600_000 + 59 * 60_000, '23 h ago'],
    [24 * 3_600_000, '1 d ago'],
    [45 * 86_400_000, '45 d ago'],
    [-5 * 60_000, 'just now'],
  ] as const)('R-UI-093 Q-UI-378 a time %d ms ago reads "%s"', (ms, words) => {
    expect(timeAgo(before(ms), NOW)).toBe(words);
  });

  it('R-UI-093 Q-UI-378 shows the relative time and the absolute time in brackets, with the machine-readable time', async () => {
    @Component({ imports: [TimeAgo], template: `<p><trove-time-ago [at]="at()" [now]="now" /></p>` })
    class Host {
      readonly at = signal(before(3 * 60_000));
      readonly now = NOW;
    }
    await TestBed.configureTestingModule({ imports: [Host] }).compileComponents();
    const fixture = TestBed.createComponent(Host);
    await settle(fixture);
    const root = fixture.nativeElement as HTMLElement;
    expect(text(root)).toMatch(/^3 min ago \(Nov 12, 2026, \d+:57:00 [AP]M\)$/);
    expect(root.querySelector('time')?.getAttribute('datetime')).toBe(before(3 * 60_000));
    await expectAccessible(root);
  });
});
