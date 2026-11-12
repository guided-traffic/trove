import { Component, inject, signal } from '@angular/core';
import { DIALOG_DATA, DialogClose, DialogRef, DialogTitle } from '../../shared/ui/dialog';
import { FilesApi } from '../../api/files-api';
import { ApiError } from '../../api/problem';
import { ErrorNotice } from '../../shared/error-notice';
import { asApiError } from '../../shared/paged-resource';

export interface FolderDialogData {
  readonly repository: string;
  /** The folder the new one is created in; empty is the root of the repository. */
  readonly parent: string;
}

/** "New folder" (R-UI-011): an explicit folder below the selected one, created with `PUT …/folder/`. */
@Component({
  selector: 'trove-folder-dialog',
  imports: [DialogTitle, DialogClose, ErrorNotice],
  template: `
    <h2 troveDialogTitle i18n="@@folder.title">New folder</h2>
    <div class="dialog-body">
      <div class="field">
        <label class="field-label" for="folder-name" i18n="@@folder.name">Folder name</label>
        <input
          id="folder-name"
          class="text-input field-w-name"
          autocomplete="off"
          spellcheck="false"
          [value]="name()"
          (input)="name.set($any($event.target).value)"
          (keydown.enter)="create()"
        />
        <p class="hint">
          <span i18n="@@folder.parent">Created in</span>
          <code translate="no"> {{ parentLabel }}</code
          >.
          <span i18n="@@folder.hint">A name with slashes creates the folders in between.</span>
        </p>
      </div>
      @if (failure(); as f) {
        <trove-error-notice [error]="f" />
      }
    </div>
    <div class="dialog-actions">
      <button class="btn btn-tertiary" type="button" troveDialogClose i18n="@@folder.cancel">Cancel</button>
      <button class="btn btn-confirm"
        type="button"
        [disabled]="busy() || name().trim() === ''"
        (click)="create()"
        i18n="@@folder.create"
      >
        Create folder
      </button>
    </div>
  `,
  styles: `
    .field {
      display: flex;
      flex-direction: column;
      gap: var(--trove-space-1);
    }
  `,
})
export class FolderDialog {
  private readonly data = inject<FolderDialogData>(DIALOG_DATA);
  private readonly ref = inject<DialogRef<string, FolderDialog>>(DialogRef);
  private readonly files = inject(FilesApi);

  protected readonly name = signal('');
  protected readonly busy = signal(false);
  protected readonly failure = signal<ApiError | null>(null);
  protected readonly parentLabel = this.data.parent === '' ? '/' : `/${this.data.parent}/`;

  protected async create(): Promise<void> {
    const name = this.name()
      .trim()
      .replace(/^\/+|\/+$/g, '');
    if (name === '' || this.busy()) return;
    const path = this.data.parent === '' ? name : `${this.data.parent}/${name}`;
    this.busy.set(true);
    this.failure.set(null);
    try {
      await this.files.createFolder(this.data.repository, path);
      this.ref.close(path);
    } catch (e) {
      this.failure.set(asApiError(e));
    } finally {
      this.busy.set(false);
    }
  }
}
