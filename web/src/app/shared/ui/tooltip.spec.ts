import { Component } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { settle } from '../../testing/screen';
import { Tooltip } from './tooltip';

@Component({
  imports: [Tooltip],
  template: `<button type="button" aria-label="About the column" troveTooltip="Counts the files">?</button>`,
})
class Host {}

describe('trove tooltip (Q-UI-126)', () => {
  async function render() {
    await TestBed.configureTestingModule({ imports: [Host] }).compileComponents();
    const fixture = TestBed.createComponent(Host);
    document.body.appendChild(fixture.nativeElement);
    await settle(fixture);
    const button = fixture.nativeElement.querySelector('button') as HTMLButtonElement;
    return { fixture, button };
  }
  const bubble = () => document.querySelector('trove-tooltip');

  it('R-UI-044 describes its element always, shows on focus and pointer, and hides on Escape and blur', async () => {
    const { fixture, button } = await render();
    const describedBy = button.getAttribute('aria-describedby');
    expect(describedBy).toBeTruthy();
    expect(document.getElementById(describedBy!)?.textContent).toBe('Counts the files');
    expect(bubble()).toBeNull();

    button.dispatchEvent(new Event('focus'));
    await settle(fixture);
    expect(bubble()?.textContent).toBe('Counts the files');

    button.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    await settle(fixture);
    expect(bubble()).toBeNull();

    button.dispatchEvent(new Event('mouseenter'));
    await settle(fixture);
    expect(bubble()).not.toBeNull();
    button.dispatchEvent(new Event('blur'));
    await settle(fixture);
    expect(bubble()).toBeNull();
  });
});
