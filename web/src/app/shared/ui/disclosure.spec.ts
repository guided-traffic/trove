import { Component } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { expectAccessible, settle } from '../../testing/screen';
import { Disclosure } from './disclosure';

@Component({
  imports: [Disclosure],
  template: `
    <trove-disclosure heading="Details" hint="Where this repository fetches from.">
      <p class="inside">Caches https://registry.npmjs.org/</p>
    </trove-disclosure>
  `,
})
class Host {}

describe('Disclosure', () => {
  it('R-UI-067 the details are one quiet disclosure: a heading at text size with a chevron, one hint line, closed until opened', async () => {
    await TestBed.configureTestingModule({ imports: [Host] }).compileComponents();
    const fixture = TestBed.createComponent(Host);
    await settle(fixture);
    const root = fixture.nativeElement as HTMLElement;
    const toggle = root.querySelector<HTMLButtonElement>('h2 button')!;
    expect(toggle.textContent).toContain('Details');
    expect(root.querySelector('.hint')?.textContent).toBe('Where this repository fetches from.');
    const region = root.querySelector('.collapse-region')!;
    expect(toggle.getAttribute('aria-controls')).toBe(region.id);
    // Closed at first (Q-UI-319): the region is not open, so nothing in it can be focused.
    expect(toggle.getAttribute('aria-expanded')).toBe('false');
    expect(region.classList).not.toContain('open');
    toggle.click();
    await settle(fixture);
    expect(toggle.getAttribute('aria-expanded')).toBe('true');
    expect(region.classList).toContain('open');
    expect(region.querySelector('.inside')?.textContent).toContain('registry.npmjs.org');
    await expectAccessible(root);
  });
});
