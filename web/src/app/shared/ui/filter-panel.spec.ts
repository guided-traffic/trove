import { Component, signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { MATERIAL_ANIMATIONS } from '@angular/material/core';
import { seriousViolations } from '../../testing/axe';
import { FilterCategory, FilterPanel, FilterToggle } from './filter-panel';

@Component({
  imports: [FilterPanel, FilterCategory, FilterToggle],
  template: `
    <trove-filter-toggle [(open)]="open" [activeCount]="count()" [controls]="panel.panelId" />
    <trove-filter-panel
      #panel
      [(open)]="open"
      [activeCount]="count()"
      applyLabel="Search"
      (apply)="applied = applied + 1"
      (clearAll)="cleared = cleared + 1"
    >
      <trove-filter-category label="Class" [active]="count() > 0" [expanded]="count() > 0">
        <input aria-label="Class value" />
      </trove-filter-category>
      <trove-filter-category label="Format">
        <input aria-label="Format value" />
      </trove-filter-category>
    </trove-filter-panel>
  `,
})
class Host {
  readonly open = signal(false);
  readonly count = signal(0);
  applied = 0;
  cleared = 0;
}

async function render() {
  await TestBed.configureTestingModule({
    imports: [Host],
    providers: [{ provide: MATERIAL_ANIMATIONS, useValue: { animationsDisabled: true } }],
  }).compileComponents();
  const fixture = TestBed.createComponent(Host);
  document.body.appendChild(fixture.nativeElement);
  await fixture.whenStable();
  const root = fixture.nativeElement as HTMLElement;
  const settle = async () => {
    for (let i = 0; i < 3; i++) {
      fixture.detectChanges();
      await fixture.whenStable();
    }
  };
  return { fixture, root, host: fixture.componentInstance, settle };
}

const toggle = (root: HTMLElement) => root.querySelector<HTMLButtonElement>('button.filter-toggle')!;

describe('Filter panel', () => {
  afterEach(() => document.body.replaceChildren());

  it('R-UI-020 is closed until the toggle opens it, and the toggle reports the state', async () => {
    const { root, settle } = await render();
    expect(root.querySelector('form')).toBeNull();
    expect(toggle(root).getAttribute('aria-expanded')).toBe('false');
    expect(toggle(root).hasAttribute('aria-controls')).toBe(false);

    toggle(root).click();
    await settle();
    const form = root.querySelector('form')!;
    expect(form.getAttribute('role')).toBe('search');
    expect(toggle(root).getAttribute('aria-expanded')).toBe('true');
    expect(toggle(root).getAttribute('aria-controls')).toBe(form.id);
    expect(form.querySelector('h2')?.textContent).toContain('Filters');
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('R-UI-044 moves focus into the panel on opening and back to the toggle on closing', async () => {
    const { root, settle } = await render();
    toggle(root).focus();
    toggle(root).click();
    await settle();
    expect(document.activeElement).toBe(root.querySelector('form h2'));

    root.querySelector<HTMLButtonElement>('button.close')!.click();
    await settle();
    expect(root.querySelector('form')).toBeNull();
    expect(document.activeElement).toBe(toggle(root));
  });

  it('R-UI-044 Escape closes the panel', async () => {
    const { root, host, settle } = await render();
    host.open.set(true);
    await settle();
    root.querySelector('form')!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    await settle();
    expect(host.open()).toBe(false);
  });

  it('R-UI-020 a category folds with a chevron button; a category with an active filter starts open', async () => {
    const { root, host, settle } = await render();
    host.count.set(1);
    host.open.set(true);
    await settle();
    const [first, second] = [...root.querySelectorAll<HTMLButtonElement>('button.category-toggle')];
    expect(first!.getAttribute('aria-expanded')).toBe('true');
    expect(second!.getAttribute('aria-expanded')).toBe('false');
    expect(first!.textContent).toContain('(active)');

    second!.click();
    await settle();
    expect(second!.getAttribute('aria-expanded')).toBe('true');
    expect(root.querySelector(`#${second!.getAttribute('aria-controls')}`)!.hasAttribute('hidden')).toBe(false);
    first!.click();
    await settle();
    expect(root.querySelector(`#${first!.getAttribute('aria-controls')}`)!.hasAttribute('hidden')).toBe(true);
  });

  it('R-UI-020 "Clear all" is disabled without active filters and asks the screen to clear them', async () => {
    const { root, host, settle } = await render();
    host.open.set(true);
    await settle();
    const clear = root.querySelector<HTMLButtonElement>('button.clear-all')!;
    expect(clear.disabled).toBe(true);
    host.count.set(2);
    await settle();
    expect(clear.disabled).toBe(false);
    clear.click();
    expect(host.cleared).toBe(1);
    expect(toggle(root).textContent).toContain('2');
  });

  it('R-UI-020 the apply button submits the form once and does not reload the page', async () => {
    const { root, host, settle } = await render();
    host.open.set(true);
    await settle();
    const apply = [...root.querySelectorAll<HTMLButtonElement>('button[type="submit"]')].find(
      (b) => b.textContent?.trim() === 'Search',
    )!;
    apply.click();
    expect(host.applied).toBe(1);
  });
});
