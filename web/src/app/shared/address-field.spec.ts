import { Component, signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { vi } from 'vitest';
import { Notifier } from '../core/notifier';
import { expectAccessible, settle } from '../testing/screen';
import { AddressField } from './address-field';

@Component({
  imports: [AddressField],
  template: `<trove-address-field [value]="value()" [label]="label()" [copyLabel]="copyLabel()" />`,
})
class Host {
  readonly value = signal('');
  readonly label = signal('Client address');
  readonly copyLabel = signal('Copy client address');
}

async function render(value: string) {
  TestBed.resetTestingModule();
  await TestBed.configureTestingModule({ imports: [Host] }).compileComponents();
  const fixture = TestBed.createComponent(Host);
  fixture.componentInstance.value.set(value);
  await settle(fixture);
  const root = fixture.nativeElement as HTMLElement;
  return { fixture, root, notifier: TestBed.inject(Notifier), input: root.querySelector<HTMLInputElement>('input') };
}

const LONG = 'https://trove.example.net/' + 'a'.repeat(274);

describe('Address field', () => {
  it.each([
    'https://trove.k8s-main.wds18.de/api/npm/npm-internal/',
    'trove.k8s-main.wds18.de/docker-hub-remote',
    LONG,
  ])('R-UI-065 shows the full address %#, read-only, with an accessible name and no ellipsis', async (address) => {
    const { root, input } = await render(address);
    expect(input!.value).toBe(address);
    expect(input!.readOnly).toBe(true);
    expect(input!.getAttribute('aria-label')).toBe('Client address');
    expect(input!.getAttribute('translate')).toBe('no');
    for (const el of [root, ...root.querySelectorAll<HTMLElement>('*')]) {
      expect(getComputedStyle(el).textOverflow).not.toBe('ellipsis');
    }
    expect(root.querySelector('trove-copy-button button')?.getAttribute('aria-label')).toBe('Copy client address');
  });

  it('R-UI-065 the labels name the row in a list', async () => {
    const { fixture, root } = await render('trove.example.net/npm-internal');
    fixture.componentInstance.label.set('Client address of npm-internal');
    fixture.componentInstance.copyLabel.set('Copy client address of npm-internal');
    await settle(fixture);
    expect(root.querySelector('input')?.getAttribute('aria-label')).toBe('Client address of npm-internal');
    const button = root.querySelector('trove-copy-button button')!;
    expect(button.getAttribute('aria-label')).toBe('Copy client address of npm-internal');
    expect(button.getAttribute('title')).toBe('Copy client address of npm-internal');
  });

  it('R-UI-065 a click on the button writes exactly the address to the clipboard and sets the status message', async () => {
    const { fixture, root, notifier } = await render('https://trove.example.net/api/npm/npm-internal/');
    const write = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { value: { writeText: write }, configurable: true });
    root.querySelector<HTMLButtonElement>('trove-copy-button button')!.click();
    await settle(fixture);
    expect(write).toHaveBeenCalledExactlyOnceWith('https://trove.example.net/api/npm/npm-internal/');
    expect(notifier.notices().map((n) => [n.kind, n.text])).toEqual([['status', 'Copied to the clipboard.']]);
  });

  it('R-UI-065 a failed clipboard write sets the alert', async () => {
    const { fixture, root, notifier } = await render('trove.example.net/docker-hub-remote');
    const write = vi.fn().mockRejectedValue(new Error('denied'));
    Object.defineProperty(navigator, 'clipboard', { value: { writeText: write }, configurable: true });
    root.querySelector<HTMLButtonElement>('trove-copy-button button')!.click();
    await settle(fixture);
    expect(notifier.notices().map((n) => n.kind)).toEqual(['alert']);
  });

  it('R-UI-065 focus and click in the field select the whole address', async () => {
    const { root, input } = await render('https://trove.example.net/api/npm/npm-internal/');
    document.body.append(root);
    try {
      input!.focus();
      expect([input!.selectionStart, input!.selectionEnd]).toEqual([0, input!.value.length]);
      input!.setSelectionRange(3, 5);
      input!.click();
      expect([input!.selectionStart, input!.selectionEnd]).toEqual([0, input!.value.length]);
    } finally {
      root.remove();
    }
  });

  it('R-UI-065 an empty address renders nothing', async () => {
    const { root } = await render('');
    expect(root.querySelector('input')).toBeNull();
    expect(root.querySelector('trove-copy-button')).toBeNull();
  });

  it('R-UI-065 has no serious accessibility violation', async () => {
    const { root } = await render('https://trove.example.net/api/npm/npm-internal/');
    await expectAccessible(root);
  });
});
