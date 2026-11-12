import { Component, signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { describe, expect, it } from 'vitest';
import { text } from '../../testing/screen';
import { FieldWidth, SelectField, SelectOption, TextField } from './fields';

@Component({
  imports: [SelectField],
  template: `<trove-select-field label="Project" name="project" [options]="options" [loading]="loading()" [width]="width()" [(value)]="value" />`,
})
class Host {
  readonly options: readonly SelectOption[] = [
    { value: '', label: 'Choose a project' },
    { value: 'web', label: 'web' },
  ];
  readonly loading = signal<string | null>(null);
  readonly width = signal<'name' | 'full'>('name');
  readonly value = signal('');
}

async function render() {
  TestBed.resetTestingModule();
  await TestBed.configureTestingModule({ imports: [Host] }).compileComponents();
  const fixture = TestBed.createComponent(Host);
  fixture.detectChanges();
  await fixture.whenStable();
  const root = fixture.nativeElement as HTMLElement;
  return { fixture, host: fixture.componentInstance, root, select: () => root.querySelector('select') as HTMLSelectElement };
}

describe('The select field (Q-UI-334)', () => {
  it('Q-UI-334 a select takes the field width of a choice, half the column, unless its place fills a line', async () => {
    const { fixture, host, root, select } = await render();
    expect([...select().classList]).toEqual(['select-input', 'field-w-name']);
    expect(root.querySelector('trove-select-field')!.classList.contains('full')).toBe(false);
    host.width.set('full');
    fixture.detectChanges();
    expect(select().classList).toContain('field-w-full');
    expect(root.querySelector('trove-select-field')!.classList.contains('full')).toBe(true);
  });

  it('Q-UI-334 while its options load it is the same select, held and busy, its one option saying so', async () => {
    const { fixture, host, select } = await render();
    host.loading.set('Loading projects…');
    fixture.detectChanges();
    const held = select();
    expect(held.disabled).toBe(true);
    expect(held.getAttribute('aria-busy')).toBe('true');
    expect([...held.options].map((o) => text(o))).toEqual(['Loading projects…']);
    host.loading.set(null);
    fixture.detectChanges();
    expect(select()).toBe(held);
    expect(held.disabled).toBe(false);
    expect(held.getAttribute('aria-busy')).toBeNull();
    expect([...held.options].map((o) => text(o))).toEqual(['Choose a project', 'web']);
  });
});

@Component({
  imports: [TextField],
  template: `
    <trove-text-field label="Priority" name="priority" type="number" [width]="width()" [(value)]="value" />
    <trove-text-field width="full" label="Description" name="description" type="textarea" [(value)]="value" />
  `,
})
class TextHost {
  readonly width = signal<FieldWidth>('number');
  readonly value = signal('');
}

describe('The text field (Q-UI-335)', () => {
  it('Q-UI-335 a text field takes the width its place names, a number field the width of its number, an area the column', async () => {
    TestBed.resetTestingModule();
    await TestBed.configureTestingModule({ imports: [TextHost] }).compileComponents();
    const fixture = TestBed.createComponent(TextHost);
    fixture.detectChanges();
    await fixture.whenStable();
    const root = fixture.nativeElement as HTMLElement;
    const input = root.querySelector('input') as HTMLInputElement;
    expect([...input.classList]).toEqual(['text-input', 'field-w-number']);
    fixture.componentInstance.width.set('duration');
    fixture.detectChanges();
    expect([...input.classList]).toEqual(['text-input', 'field-w-duration']);
    expect([...root.querySelector('textarea')!.classList]).toEqual(['area-input', 'field-w-full']);
  });
});
