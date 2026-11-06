import { ComponentType } from '@angular/cdk/overlay';
import { CdkDialogContainer, Dialog, DialogConfig, DialogRef } from '@angular/cdk/dialog';
import { Directive, Injectable, OnDestroy, inject, input } from '@angular/core';

export { DIALOG_DATA, DialogRef } from '@angular/cdk/dialog';

/** What a screen may set when it opens a dialog; everything else is trove's one dialog (Q-UI-126). */
export interface DialogOptions<D = unknown> {
  readonly data?: D;
  readonly width?: string;
  readonly maxWidth?: string;
  /** Escape and a click on the backdrop do not close it. */
  readonly disableClose?: boolean;
}

/**
 * Opens a component in trove's dialog: the CDK dialog with a modal `role="dialog"` named by its
 * `troveDialogTitle`, the focus trapped inside and returned to the opener on close, the rest of the page hidden from
 * assistive technology, and trove's panel styling (`_dialog.scss`). The type parameters keep the order the screens
 * were written in: the component, its data and its result.
 */
@Injectable({ providedIn: 'root' })
export class TroveDialog {
  private readonly dialog = inject(Dialog);

  open<T, D = unknown, R = unknown>(
    component: ComponentType<T>,
    options: DialogOptions<D> = {},
  ): DialogRef<R, T> {
    const config = new DialogConfig<D, DialogRef<R, T>>();
    config.data = options.data ?? null;
    config.role = 'dialog';
    config.ariaModal = true;
    config.autoFocus = 'first-tabbable';
    config.restoreFocus = true;
    config.panelClass = 'trove-dialog';
    config.backdropClass = 'trove-dialog-backdrop';
    config.disableClose = options.disableClose ?? false;
    // A width never exceeds the window, whatever the screen asks for.
    config.maxWidth = `min(${options.maxWidth ?? '40rem'}, calc(100vw - 2rem))`;
    if (options.width) config.width = `min(${options.width}, calc(100vw - 2rem))`;
    return this.dialog.open<R, D, T>(component, config);
  }
}

let nextTitleId = 1;

/** The title of a dialog: names it (`aria-labelledby` of the dialog container) and takes the title's look. */
@Directive({
  selector: '[troveDialogTitle]',
  host: { class: 'dialog-title', '[id]': 'id' },
})
export class DialogTitle implements OnDestroy {
  protected readonly id = `trove-dialog-title-${nextTitleId++}`;
  private readonly container = inject(DialogRef, { optional: true })?.containerInstance as
    | CdkDialogContainer
    | undefined;

  constructor() {
    // After the container's own change detection: the name is a binding of the container, which is checked first.
    void Promise.resolve().then(() => this.container?._addAriaLabelledBy(this.id));
  }

  ngOnDestroy(): void {
    this.container?._removeAriaLabelledBy(this.id);
  }
}

/** Closes the dialog it sits in, with the bound value as the result: `<button troveDialogClose>`. */
@Directive({
  selector: '[troveDialogClose]',
  host: { '(click)': 'close()' },
})
export class DialogClose {
  private readonly ref = inject(DialogRef);
  /** The result the dialog closes with; the bare attribute closes without one. */
  readonly troveDialogClose = input<unknown>(undefined);

  protected close(): void {
    const value = this.troveDialogClose();
    this.ref.close(value === '' ? undefined : value);
  }
}
