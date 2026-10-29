import {
  HttpClient,
  HttpErrorResponse,
  HttpEventType,
  provideHttpClient,
  withXhr,
  withXsrfConfiguration,
} from '@angular/common/http';
import { EnvironmentInjector, Injectable, createEnvironmentInjector, inject } from '@angular/core';
import {
  Observable,
  catchError,
  filter,
  fromEvent,
  lastValueFrom,
  map,
  takeUntil,
  tap,
  throwError,
} from 'rxjs';
import { SessionStore } from '../core/session-store';
import { ApiClient } from './client';
import { CSRF_COOKIE, CSRF_HEADER } from './csrf';
import { PageFetcher } from './pagination';
import { ApiError, isProblem } from './problem';
import { DeleteOutcome, JobReference, repositoryUrl } from './repositories-api';
import { Schema } from './types';

export type Item = Schema<'Item'>;
export type Child = Schema<'Child'>;
export type ItemStats = Schema<'ItemStats'>;

/** A file larger than this is not hashed in the browser; the server's own digest is used (R-UI-012). */
export const HASH_LIMIT_BYTES = 256 * 1024 * 1024;

/**
 * `ui.deploy.max_upload_bytes` (R-UI-012, default 100 MiB). The UI cannot read the configuration
 * (no API member carries it), so the default is a constant; the server's own limit still decides.
 */
export const UPLOAD_LIMIT_BYTES = 100 * 1024 * 1024;

/** One upload: the target, the file and the way to follow and stop it. */
export interface UploadRequest {
  readonly repository: string;
  readonly path: string;
  readonly file: Blob;
  /** `sha-256=:<base64>:` when the file was hashed in the browser. */
  readonly reprDigest?: string;
  /** False sends `If-None-Match: *`, so an existing path is refused instead of replaced. */
  readonly overwrite: boolean;
  readonly onProgress?: (loaded: number, total: number) => void;
  readonly signal?: AbortSignal;
}

/** True for a client address template of the raw files family (`protocolBasePath` of a format). */
export function isFilesAddress(template: string): boolean {
  return template.startsWith('/files/');
}

/** Percent-encodes every segment of a repository path and keeps the slashes. */
export function encodePath(path: string): string {
  return path.split('/').map(encodeURIComponent).join('/');
}

/** SHA-256 of the file as the `Repr-Digest` value of RFC 9530, or undefined above the limit. */
export async function reprDigestOf(file: Blob): Promise<string | undefined> {
  if (file.size > HASH_LIMIT_BYTES || !crypto.subtle) return undefined;
  try {
    const digest = new Uint8Array(await crypto.subtle.digest('SHA-256', await bytesOf(file)));
    let binary = '';
    for (const byte of digest) binary += String.fromCharCode(byte);
    return `sha-256=:${btoa(binary)}:`;
  } catch {
    // A file that cannot be read for hashing is still sent; the server computes its own digest.
    return undefined;
  }
}

function bytesOf(blob: Blob): Promise<ArrayBuffer> {
  if (typeof blob.arrayBuffer === 'function') return blob.arrayBuffer();
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(reader.result as ArrayBuffer);
    reader.onerror = () => reject(reader.error);
    reader.readAsArrayBuffer(blob);
  });
}

/** The hex SHA-256 behind a `Repr-Digest` value, for the comparison after a retried upload. */
export function hexOfReprDigest(value: string): string | undefined {
  const m = /^sha-256=:([A-Za-z0-9+/=]+):/.exec(value);
  if (!m?.[1]) return undefined;
  return [...atob(m[1])].map((c) => c.charCodeAt(0).toString(16).padStart(2, '0')).join('');
}

function itemsUrl(repository: string, suffix: 'items' | 'children' | 'stats'): string {
  return `${repositoryUrl(repository)}/${suffix}`;
}

/** Files and folders of a repository: the browser (R-UI-010, R-UI-011) and the deploy dialog (R-UI-012). */
export abstract class FilesApi {
  abstract item(repository: string, path: string): Promise<Item>;
  /** The direct children of a folder; `path` '' is the root of the repository. */
  abstract children(repository: string, path: string, limit: number): PageFetcher<Child>;
  abstract stats(repository: string, path: string): Promise<ItemStats>;
  /**
   * Counts the items below a folder, stopping once the count exceeds `cap`; the second member is
   * true when the walk stopped early (the real number is larger).
   */
  abstract countBelow(
    repository: string,
    path: string,
    cap: number,
  ): Promise<{ count: number; capped: boolean }>;
  abstract deleteItem(
    repository: string,
    path: string,
    idempotencyKey: string,
  ): Promise<DeleteOutcome>;
  abstract upload(request: UploadRequest): Promise<Item>;
  /** `PUT …/folder/` with an empty body: an explicit folder and its missing parents. */
  abstract createFolder(repository: string, path: string): Promise<Item>;
}

@Injectable()
export class HttpFilesApi extends FilesApi {
  private readonly client = inject(ApiClient);
  private readonly session = inject(SessionStore);
  private readonly injector = inject(EnvironmentInjector);
  private xhr: HttpClient | null = null;

  async item(repository: string, path: string): Promise<Item> {
    return (await this.client.getJson<Item>(itemsUrl(repository, 'items'), { params: { path } }))
      .body;
  }

  children(repository: string, path: string, limit: number): PageFetcher<Child> {
    const fetcher = this.client.page<Child>(itemsUrl(repository, 'children'), { path });
    return (cursor) => fetcher(cursor, limit);
  }

  async stats(repository: string, path: string): Promise<ItemStats> {
    return (
      await this.client.getJson<ItemStats>(itemsUrl(repository, 'stats'), { params: { path } })
    ).body;
  }

  async countBelow(
    repository: string,
    path: string,
    cap: number,
  ): Promise<{ count: number; capped: boolean }> {
    const fetcher = this.client.page<Child>(itemsUrl(repository, 'children'), {
      path,
      recursive: true,
    });
    let count = 0;
    let cursor: string | null = null;
    do {
      const page: { items: readonly Child[]; nextCursor: string | null } = await fetcher(
        cursor,
        cap + 1,
      );
      count += page.items.length;
      cursor = page.nextCursor;
      if (count > cap) return { count, capped: true };
    } while (cursor !== null);
    return { count, capped: false };
  }

  async deleteItem(
    repository: string,
    path: string,
    idempotencyKey: string,
  ): Promise<DeleteOutcome> {
    const r = await this.client.delete<{ job: JobReference } | null>(
      itemsUrl(repository, 'items'),
      {
        params: { path },
        idempotencyKey,
      },
    );
    return r.status === 202 && r.body ? { done: false, job: r.body.job } : { done: true };
  }

  upload(request: UploadRequest): Promise<Item> {
    const headers: Record<string, string> = {
      'Content-Type': request.file.type || 'application/octet-stream',
    };
    if (request.reprDigest) headers['Repr-Digest'] = request.reprDigest;
    if (!request.overwrite) headers['If-None-Match'] = '*';
    return this.put(
      request,
      `/files/${encodeURIComponent(request.repository)}/${encodePath(request.path)}`,
      request.file,
      headers,
    );
  }

  createFolder(repository: string, path: string): Promise<Item> {
    const url = `/files/${encodeURIComponent(repository)}/${encodePath(path)}/`;
    return this.put({}, url, new Blob([]), {});
  }

  /**
   * Uploads go through an XHR-based client, because the fetch backend reports no upload progress
   * (R-UI-012). The client has no trove interceptors (they cover `/api/v1/` only); it keeps the
   * XSRF protection, maps a failure to `ApiError` and ends the session on a 401.
   */
  private put(
    options: {
      readonly onProgress?: (loaded: number, total: number) => void;
      readonly signal?: AbortSignal;
    },
    url: string,
    body: Blob,
    headers: Record<string, string>,
  ): Promise<Item> {
    this.xhr ??= createEnvironmentInjector(
      [
        provideHttpClient(
          withXhr(),
          withXsrfConfiguration({ cookieName: CSRF_COOKIE, headerName: CSRF_HEADER }),
        ),
      ],
      this.injector,
    ).get(HttpClient);
    const abort$: Observable<unknown> = options.signal
      ? fromEvent(options.signal, 'abort')
      : new Observable<never>();
    const events$ = this.xhr
      .put<Item>(url, body, {
        headers,
        observe: 'events',
        reportProgress: true,
        responseType: 'json',
      })
      .pipe(
        tap((event) => {
          if (event.type === HttpEventType.UploadProgress && event.total) {
            options.onProgress?.(event.loaded, event.total);
          }
        }),
        filter((event) => event.type === HttpEventType.Response),
        map((event) => (event as { body: Item | null }).body as Item),
        takeUntil(abort$),
        catchError((err: unknown) => throwError(() => this.toApiError(err))),
      );
    return lastValueFrom(events$, { defaultValue: null }).then((item) => {
      if (item === null) throw new DOMException('The upload was cancelled.', 'AbortError');
      return item;
    });
  }

  private toApiError(err: unknown): unknown {
    if (!(err instanceof HttpErrorResponse)) return err;
    const problem = isProblem(err.error) ? err.error : null;
    if (err.status === 401 && problem?.code !== 'reauthentication_required')
      this.session.markEnded();
    return new ApiError(
      err.status,
      problem,
      problem?.requestId ?? err.headers?.get('X-Request-Id') ?? null,
    );
  }
}
