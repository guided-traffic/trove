import { Component, inject } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { firstValueFrom } from 'rxjs';
import { seriousViolations } from '../../testing/axe';
import { DIALOG_DATA, DialogClose, DialogRef, DialogTitle, TroveDialog } from './dialog';

@Component({
  selector: 'trove-test-dialog',
  imports: [DialogTitle, DialogClose],
  template: `
    <h2 troveDialogTitle>Delete the thing</h2>
    <div class="dialog-body"><p>{{ data.what }}</p><input aria-label="Name" /></div>
    <div class="dialog-actions">
      <button type="button" class="btn btn-tertiary" troveDialogClose>Cancel</button>
      <button type="button" class="btn btn-confirm" (click)="ref.close('done')">Delete</button>
    </div>
  `,
})
class TestDialog {
  protected readonly data = inject<{ what: string }>(DIALOG_DATA);
  protected readonly ref = inject<DialogRef<string, TestDialog>>(DialogRef);
}

const dialogEl = () => document.querySelector<HTMLElement>('[role="dialog"]');
const turn = () => new Promise((resolve) => setTimeout(resolve));

describe('trove dialog (Q-UI-126)', () => {
  afterEach(() => TestBed.inject(TroveDialog)['dialog'].closeAll());

  it('R-UI-044 is a modal dialog named by its title, with the data injected, and passes axe', async () => {
    const ref = TestBed.inject(TroveDialog).open<TestDialog, { what: string }, string>(TestDialog, {
      data: { what: 'It cannot be undone.' },
    });
    await turn();
    const el = dialogEl()!;
    expect(el.getAttribute('aria-modal')).toBe('true');
    const title = el.querySelector('h2')!;
    expect(title.id).not.toBe('');
    expect(el.getAttribute('aria-labelledby')).toBe(title.id);
    expect(el.textContent).toContain('It cannot be undone.');
    expect(await seriousViolations(el)).toEqual([]);
    ref.close();
  });

  it('closes with the value of the confirming button, and without one on the cancel button', async () => {
    const dialog = TestBed.inject(TroveDialog);
    const confirmed = dialog.open<TestDialog, { what: string }, string>(TestDialog, { data: { what: 'x' } });
    await turn();
    const result = firstValueFrom(confirmed.closed);
    [...dialogEl()!.querySelectorAll('button')].find((b) => b.textContent === 'Delete')!.click();
    expect(await result).toBe('done');

    const cancelled = dialog.open<TestDialog, { what: string }, string>(TestDialog, { data: { what: 'x' } });
    await turn();
    const none = firstValueFrom(cancelled.closed);
    [...dialogEl()!.querySelectorAll('button')].find((b) => b.textContent === 'Cancel')!.click();
    expect(await none).toBeUndefined();
    expect(dialogEl()).toBeNull();
  });

  it('puts the focus inside, keeps it there, and returns it to the opener on close', async () => {
    const opener = document.createElement('button');
    document.body.appendChild(opener);
    opener.focus();
    const ref = TestBed.inject(TroveDialog).open<TestDialog, { what: string }, string>(TestDialog, {
      data: { what: 'x' },
    });
    await turn();
    expect(dialogEl()!.contains(document.activeElement)).toBe(true);
    ref.close();
    await turn();
    expect(document.activeElement).toBe(opener);
    opener.remove();
  });

  it('closes on Escape, and not when disableClose is set', async () => {
    const dialog = TestBed.inject(TroveDialog);
    const closable = dialog.open<TestDialog, { what: string }, string>(TestDialog, { data: { what: 'x' } });
    await turn();
    const gone = firstValueFrom(closable.closed);
    dialogEl()!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', code: 'Escape', keyCode: 27, bubbles: true }));
    await gone;
    expect(dialogEl()).toBeNull();

    dialog.open<TestDialog, { what: string }, string>(TestDialog, { data: { what: 'x' }, disableClose: true });
    await turn();
    dialogEl()!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', code: 'Escape', keyCode: 27, bubbles: true }));
    await turn();
    expect(dialogEl()).not.toBeNull();
  });
});
