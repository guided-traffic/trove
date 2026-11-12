import { Component } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { expectAccessible, settle } from '../../testing/screen';
import { EmptyState } from './empty-state';
import { PageHeader } from './page-header';

@Component({
  imports: [PageHeader, EmptyState],
  template: `
    <trove-page-header heading="npm-internal" [name]="true" description="Packages built by the web team." [addresses]="addresses">
      <span meta class="badge">npm</span>
      <a actions class="btn btn-confirm" href="#setup">Set up client</a>
    </trove-page-header>
    <trove-empty-state text="This repository holds no packages yet."><a class="btn btn-confirm" href="#setup">Set up client</a></trove-empty-state>
  `,
})
class Host {
  readonly addresses = ['https://trove.example.net/api/npm/web/npm-internal/'];
}

describe('Page header and empty state', () => {
  it('R-UI-067 the header names the object in the h1, says what it is, shows its address and puts its actions at the right end', async () => {
    await TestBed.configureTestingModule({ imports: [Host] }).compileComponents();
    const fixture = TestBed.createComponent(Host);
    await settle(fixture);
    const root = fixture.nativeElement as HTMLElement;
    const h1 = root.querySelector('h1')!;
    expect(h1.textContent?.trim()).toBe('npm-internal');
    expect(h1.getAttribute('translate')).toBe('no');
    expect(h1.getAttribute('tabindex')).toBe('-1');
    expect(root.querySelector('.page-header-description')?.textContent).toBe('Packages built by the web team.');
    expect(root.querySelector('.page-header-main .badge')?.textContent).toBe('npm');
    expect(root.querySelector<HTMLInputElement>('.page-header-main trove-address-field input')?.value).toBe(
      'https://trove.example.net/api/npm/web/npm-internal/',
    );
    expect(root.querySelector('.page-header-actions a')?.textContent).toBe('Set up client');
    expect(root.querySelector('trove-empty-state .empty-state-text')?.textContent).toBe('This repository holds no packages yet.');
    expect(root.querySelector('trove-empty-state a.btn')).toBeTruthy();
    await expectAccessible(root);
  });
});
