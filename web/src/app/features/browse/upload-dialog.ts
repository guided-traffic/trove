import { Component, inject, signal } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { MAT_DIALOG_DATA, MatDialogModule, MatDialogRef } from '@angular/material/dialog';
import { UPLOAD_LIMIT_BYTES, FilesApi, hexOfReprDigest, reprDigestOf } from '../../api/files-api';
import { ApiError } from '../../api/problem';
import { describeProblem } from '../../core/http/problem-messages';

export interface UploadDialogData {
  readonly repository: string;
  /** The selected folder; the prefill of the target path. */
  readonly folder: string;
  /** The caller holds `overwrite` on the target (R-UI-012): the "Replace" switch is offered. */
  readonly canOverwrite: boolean;
}

type EntryState = 'queued' | 'hashing' | 'uploading' | 'done' | 'failed' | 'cancelled';

interface Entry {
  readonly file: File;
  readonly state: EntryState;
  readonly loaded: number;
  readonly message: string;
}

/**
 * Deploy dialog (R-UI-012): target folder, files chosen with a button or dropped, one after
 * another with progress and per-file status; "Cancel" aborts the current one. Files up to 256 MiB
 * are hashed in the browser and sent with `Repr-Digest`; larger ones rely on the server's digest.
 * The size limit is checked before anything is sent. An upload cut without an answer stored
 * nothing; it is sent once more from the start from the `File` in hand, and a retry that is told
 * the path exists counts as done when the stored SHA-256 equals the one computed here.
 */
@Component({
  selector: 'trove-upload-dialog',
  imports: [MatButtonModule, MatDialogModule],
  template: `
    <h2 mat-dialog-title i18n="@@upload.title">Upload files</h2>
    <mat-dialog-content>
      <div class="field">
        <label class="field-label" for="upload-folder" i18n="@@upload.folder">Target folder</label>
        <input
          id="upload-folder"
          class="text-input"
          autocomplete="off"
          spellcheck="false"
          [value]="folder()"
          [disabled]="running()"
          (input)="folder.set($any($event.target).value)"
        />
        <p class="hint" i18n="@@upload.folder.hint">
          Relative to the root of the repository; missing folders are created. Empty is the root.
        </p>
      </div>

      <div
        class="drop"
        [class.over]="over()"
        (dragover)="$event.preventDefault(); over.set(true)"
        (dragleave)="over.set(false)"
        (drop)="drop($event)"
      >
        <span i18n="@@upload.drop">Drop files here, or</span>
        <button
          mat-stroked-button
          type="button"
          [disabled]="running()"
          (click)="chooser.click()"
          i18n="@@upload.choose"
        >
          Choose files
        </button>
        <input
          #chooser
          type="file"
          multiple
          class="visually-hidden"
          tabindex="-1"
          aria-hidden="true"
          (change)="choose($any($event.target).files)"
        />
      </div>

      @if (data.canOverwrite) {
        <label class="check-row">
          <input
            type="checkbox"
            [checked]="overwrite()"
            [disabled]="running()"
            (change)="overwrite.set($any($event.target).checked)"
          />
          <span i18n="@@upload.overwrite">Replace files that already exist</span>
        </label>
      }

      @if (entries().length > 0) {
        <table class="data">
          <caption class="visually-hidden" i18n="@@upload.caption">
            Files to upload
          </caption>
          <thead>
            <tr>
              <th scope="col" i18n="@@upload.col.file">File</th>
              <th scope="col" i18n="@@upload.col.status">Status</th>
            </tr>
          </thead>
          <tbody>
            @for (e of entries(); track $index) {
              <tr>
                <th scope="row" translate="no">{{ e.file.name }}</th>
                <td>
                  @switch (e.state) {
                    @case ('queued') {
                      <span i18n="@@upload.state.queued">waiting</span>
                    }
                    @case ('hashing') {
                      <span i18n="@@upload.state.hashing">computing checksum</span>
                    }
                    @case ('uploading') {
                      <progress
                        [attr.aria-label]="progressLabel"
                        [value]="e.loaded"
                        [max]="e.file.size"
                      ></progress>
                    }
                    @case ('done') {
                      <span class="material-icons inline ok" aria-hidden="true">check_circle</span>
                      <span i18n="@@upload.state.done">uploaded</span>
                    }
                    @case ('cancelled') {
                      <span i18n="@@upload.state.cancelled">cancelled</span>
                    }
                    @case ('failed') {
                      <span class="material-icons inline bad" aria-hidden="true">error</span>
                      <span>{{ e.message }}</span>
                    }
                  }
                </td>
              </tr>
            }
          </tbody>
        </table>
      }
      <span class="visually-hidden" role="status">{{ announcement() }}</span>
    </mat-dialog-content>
    <mat-dialog-actions align="end">
      @if (running()) {
        <button mat-button type="button" (click)="cancel()" i18n="@@upload.cancel">
          Cancel upload
        </button>
      } @else {
        <button mat-button type="button" (click)="close()" i18n="@@upload.close">Close</button>
        <button
          mat-flat-button
          type="button"
          [disabled]="pending() === 0"
          (click)="start()"
          i18n="@@upload.start"
        >
          Upload
        </button>
      }
    </mat-dialog-actions>
  `,
  styles: `
    .field {
      display: flex;
      flex-direction: column;
      gap: var(--trove-space-1);
      margin-block-end: var(--trove-space-3);
    }
    .drop {
      display: flex;
      align-items: center;
      gap: var(--trove-space-3);
      padding: var(--trove-space-4);
      border: 2px dashed var(--mat-sys-outline);
      border-radius: var(--mat-sys-corner-medium);
      margin-block-end: var(--trove-space-3);
    }
    .drop.over {
      border-color: var(--mat-sys-primary);
    }
    .check-row {
      display: flex;
      gap: var(--trove-space-2);
      margin-block-end: var(--trove-space-3);
    }
  `,
})
export class UploadDialog {
  protected readonly data = inject<UploadDialogData>(MAT_DIALOG_DATA);
  private readonly ref = inject<MatDialogRef<UploadDialog, number>>(MatDialogRef);
  private readonly files = inject(FilesApi);

  protected readonly folder = signal(this.data.folder);
  protected readonly overwrite = signal(false);
  protected readonly entries = signal<readonly Entry[]>([]);
  protected readonly over = signal(false);
  protected readonly running = signal(false);
  protected readonly announcement = signal('');
  protected readonly progressLabel = $localize`:@@upload.progress:Upload progress`;
  private abort: AbortController | null = null;
  private uploaded = 0;

  protected pending(): number {
    return this.entries().filter((e) => e.state === 'queued').length;
  }

  protected choose(list: FileList | null): void {
    if (list) this.add([...list]);
  }

  protected drop(event: DragEvent): void {
    event.preventDefault();
    this.over.set(false);
    if (event.dataTransfer?.files && !this.running()) this.add([...event.dataTransfer.files]);
  }

  private add(files: File[]): void {
    this.entries.update((list) => [
      ...list,
      ...files.map((file): Entry => {
        if (file.size > UPLOAD_LIMIT_BYTES) {
          return {
            file,
            state: 'failed',
            loaded: 0,
            message: $localize`:@@upload.too-large:Larger than ${Math.floor(UPLOAD_LIMIT_BYTES / 1048576)}:limit: MiB. Use a native client or curl for files this large.`,
          };
        }
        return { file, state: 'queued', loaded: 0, message: '' };
      }),
    ]);
  }

  protected close(): void {
    this.ref.close(this.uploaded);
  }

  protected cancel(): void {
    this.abort?.abort();
  }

  protected async start(): Promise<void> {
    this.running.set(true);
    try {
      for (let i = 0; i < this.entries().length; i++) {
        if (this.entries()[i]?.state !== 'queued') continue;
        this.abort = new AbortController();
        await this.uploadOne(i, this.abort.signal);
      }
    } finally {
      this.running.set(false);
      this.abort = null;
    }
  }

  private patch(index: number, change: Partial<Entry>): void {
    this.entries.update((list) => list.map((e, i) => (i === index ? { ...e, ...change } : e)));
  }

  private target(file: File): string {
    const folder = this.folder().replace(/^\/+|\/+$/g, '');
    return folder === '' ? file.name : `${folder}/${file.name}`;
  }

  private async uploadOne(index: number, signal: AbortSignal): Promise<void> {
    const file = this.entries()[index]?.file;
    if (!file) return;
    this.patch(index, { state: 'hashing' });
    const digest = await reprDigestOf(file);
    this.patch(index, { state: 'uploading', loaded: 0 });
    const path = this.target(file);
    const request = {
      repository: this.data.repository,
      path,
      file,
      reprDigest: digest,
      overwrite: this.overwrite(),
      signal,
      onProgress: (loaded: number) => this.patch(index, { loaded }),
    };
    try {
      try {
        await this.files.upload(request);
      } catch (e) {
        if (!(e instanceof ApiError) || e.status !== 0 || signal.aborted) throw e;
        // No answer (R-API-051): nothing was stored; send it once more from the start.
        try {
          await this.files.upload(request);
        } catch (retry) {
          if (!(await this.alreadyStored(retry, path, digest))) throw retry;
        }
      }
      this.uploaded++;
      this.patch(index, { state: 'done' });
      this.announcement.set($localize`:@@upload.announce.done:${file.name}:name: uploaded.`);
    } catch (e) {
      if (e instanceof DOMException && e.name === 'AbortError') {
        this.patch(index, { state: 'cancelled' });
        return;
      }
      this.patch(index, { state: 'failed', message: this.describe(e) });
      this.announcement.set($localize`:@@upload.announce.failed:${file.name}:name: failed.`);
    }
  }

  /** A retried create-only upload that answers "exists" is a success when the bytes are ours. */
  private async alreadyStored(
    error: unknown,
    path: string,
    digest: string | undefined,
  ): Promise<boolean> {
    if (!(error instanceof ApiError) || error.status !== 412 || !digest) return false;
    const mine = hexOfReprDigest(digest);
    try {
      const item = await this.files.item(this.data.repository, path);
      return mine !== undefined && item.digests?.sha256 === mine;
    } catch {
      return false;
    }
  }

  private describe(e: unknown): string {
    if (e instanceof ApiError) {
      if (e.status === 412) {
        return $localize`:@@upload.exists:A file already exists at this path. Tick "Replace files that already exist" to overwrite it.`;
      }
      return describeProblem(e).text;
    }
    return $localize`:@@upload.failed:The upload failed.`;
  }
}
