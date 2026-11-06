import { Component, signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { seriousViolations } from '../../testing/axe';
import { settle } from '../../testing/screen';
import { Checkbox } from './checkbox';

@Component({
  imports: [Checkbox],
  template: `
    <trove-checkbox [checked]="on()" (checkedChange)="on.set($event)" describedBy="note">Allow it</trove-checkbox>
    <p id="note">Only for admins.</p>
    <trove-checkbox [checked]="false" [disabled]="true">Locked</trove-checkbox>
    <trove-checkbox [checked]="false" ariaLabel="Select row 1" />
  `,
})
class Host {
  readonly on = signal(false);
}

describe('trove checkbox (Q-UI-126)', () => {
  async function render() {
    await TestBed.configureTestingModule({ imports: [Host] }).compileComponents();
    const fixture = TestBed.createComponent(Host);
    document.body.appendChild(fixture.nativeElement);
    await settle(fixture);
    const root = fixture.nativeElement as HTMLElement;
    return { fixture, root, boxes: [...root.querySelectorAll<HTMLInputElement>('input[type="checkbox"]')] };
  }

  it('R-UI-044 is a native checkbox named by its label or its aria-label, linked to its note, and passes axe', async () => {
    const { root, boxes } = await render();
    expect(boxes[0]!.labels?.[0]?.textContent?.trim()).toBe('Allow it');
    expect(boxes[0]!.getAttribute('aria-describedby')).toBe('note');
    expect(boxes[1]!.disabled).toBe(true);
    expect(boxes[2]!.getAttribute('aria-label')).toBe('Select row 1');
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('a click toggles it and reports the new state', async () => {
    const { fixture, boxes } = await render();
    boxes[0]!.click();
    await settle(fixture);
    expect(fixture.componentInstance.on()).toBe(true);
    expect(boxes[0]!.checked).toBe(true);
  });
});
