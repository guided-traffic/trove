import { Component, inject, signal } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { MAT_DIALOG_DATA, MatDialogModule, MatDialogRef } from '@angular/material/dialog';
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
  imports: [MatButtonModule, MatDialogModule, ErrorNotice],
  template: `
    <h2 mat-dialog-title i18n="@@folder.title">New folder</h2>
    <mat-dialog-content>
      <div class="field">
        <label class="field-label" for="folder-name" i18n="@@folder.name">Folder name</label>
        <input
          id="folder-name"
          class="text-input"
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
    </mat-dialog-content>
    <mat-dialog-actions align="end">
      <button mat-button type="button" mat-dialog-close i18n="@@folder.cancel">Cancel</button>
      <button
        mat-flat-button
        type="button"
        [disabled]="busy() || name().trim() === ''"
        (click)="create()"
        i18n="@@folder.create"
      >
        Create folder
      </button>
    </mat-dialog-actions>
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
  private readonly data = inject<FolderDialogData>(MAT_DIALOG_DATA);
  private readonly ref = inject<MatDialogRef<FolderDialog, string>>(MatDialogRef);
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
