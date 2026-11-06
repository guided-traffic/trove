import { Component, signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { seriousViolations } from '../../testing/axe';
import { settle } from '../../testing/screen';
import { TabContent, TabGroup, TabPage } from './tabs';

@Component({
  imports: [TabGroup, TabPage, TabContent],
  template: `
    <trove-tab-group [selectedIndex]="index()" (selectedIndexChange)="index.set($event)">
      <trove-tab label="General"><p>general body</p></trove-tab>
      <trove-tab label="Access"><ng-template troveTabContent><p>access body</p></ng-template></trove-tab>
      @if (extra()) {
        <trove-tab label="Extra"><p>extra body</p></trove-tab>
      }
    </trove-tab-group>
  `,
})
class Host {
  readonly index = signal(0);
  readonly extra = signal(true);
}

async function render() {
  await TestBed.configureTestingModule({ imports: [Host] }).compileComponents();
  const fixture = TestBed.createComponent(Host);
  document.body.appendChild(fixture.nativeElement);
  await settle(fixture);
  const root = fixture.nativeElement as HTMLElement;
  const tabs = () => [...root.querySelectorAll<HTMLElement>('[role="tab"]')];
  return { fixture, root, tabs, host: fixture.componentInstance };
}

describe('trove tabs (Q-UI-126)', () => {
  it('R-UI-044 is a tablist with the selected tab named for its panel, and passes axe', async () => {
    const { root, tabs } = await render();
    expect(root.querySelector('[role="tablist"]')).toBeTruthy();
    expect(tabs().map((t) => t.textContent?.trim())).toEqual(['General', 'Access', 'Extra']);
    expect(tabs().map((t) => t.getAttribute('aria-selected'))).toEqual(['true', 'false', 'false']);
    const panel = root.querySelector('[role="tabpanel"]')!;
    expect(panel.textContent).toContain('general body');
    expect(panel.getAttribute('aria-labelledby')).toBe(tabs()[0]!.id);
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('a click selects a tab and only its content is in the page; the lazy content appears with it', async () => {
    const { fixture, root, tabs, host } = await render();
    tabs()[1]!.click();
    await settle(fixture);
    expect(host.index()).toBe(1);
    expect(root.textContent).toContain('access body');
    expect(root.textContent).not.toContain('general body');
    tabs()[0]!.click();
    await settle(fixture);
    expect(root.textContent).not.toContain('access body');
  });

  it('the arrow keys move between the tabs, with a single tab stop', async () => {
    const { fixture, tabs, host } = await render();
    expect(tabs().map((t) => t.tabIndex)).toEqual([0, -1, -1]);
    tabs()[0]!.focus();
    tabs()[0]!.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true }));
    await settle(fixture);
    expect(host.index()).toBe(1);
    expect(document.activeElement).toBe(tabs()[1]);
  });

  it('a selected index past the last tab shows the last one', async () => {
    const { fixture, tabs, host } = await render();
    host.index.set(9);
    await settle(fixture);
    expect(tabs().map((t) => t.getAttribute('aria-selected'))).toEqual(['false', 'false', 'true']);
  });
});
