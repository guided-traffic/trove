import { HttpClient, HttpContext, HttpContextToken, HttpParams } from '@angular/common/http';
import { Injectable, inject } from '@angular/core';
import { firstValueFrom } from 'rxjs';
import { Page, PageFetcher } from './pagination';
import { ApiResponse, GetBody, GetPath } from './types';

/** Carries the `Idempotency-Key` of a request from the client to the interceptor (R-API-014). */
export const IDEMPOTENCY_KEY = new HttpContextToken<string | null>(() => null);

/** Query parameter values; arrays repeat the parameter (R-API-011), undefined is omitted. */
export type QueryParams = Readonly<
  Record<string, string | number | boolean | readonly string[] | undefined>
>;

export interface ReadOptions {
  readonly params?: QueryParams;
  /** Sends `If-None-Match`; a 304 answer is not mapped. Optional on every GET. */
  readonly ifNoneMatch?: string;
}

export interface CreateOptions {
  readonly params?: QueryParams;
  /**
   * The key of the form or dialog that issues the request; generated when the form opens and kept
   * until it closes (R-UI-042). Without a key the request is not retried after a network error.
   */
  readonly idempotencyKey?: string;
}

/** A new `Idempotency-Key`: a UUID, as R-API-014 recommends. */
export function newIdempotencyKey(): string {
  return crypto.randomUUID();
}

function toParams(params: QueryParams | undefined): HttpParams {
  let out = new HttpParams();
  for (const [name, value] of Object.entries(params ?? {})) {
    if (value === undefined) continue;
    if (typeof value === 'object') {
      for (const v of value) out = out.append(name, v);
    } else {
      out = out.append(name, String(value));
    }
  }
  return out;
}

/**
 * The one HTTP client of the management API (R-UI-046). It speaks JSON, exposes the `ETag` and
 * `Location` next to the body and sends the conventions of R-API-010 to R-API-014; CSRF, the
 * problem mapping, retries and the 401 handling are interceptors (core/http) and apply to every
 * call. Failures reject with an `ApiError`. No component calls `/api/v1` with `HttpClient`.
 *
 * Typed overload: `get` with a path of the generated OpenAPI document returns that operation's
 * body. Endpoints the document does not describe yet go through `getJson<T>` with a type the
 * calling service declares.
 */
@Injectable({ providedIn: 'root' })
export class ApiClient {
  private readonly http = inject(HttpClient);

  get<P extends GetPath>(path: P, options?: ReadOptions): Promise<ApiResponse<GetBody<P>>> {
    return this.getJson<GetBody<P>>(path, options);
  }

  getJson<T>(url: string, options: ReadOptions = {}): Promise<ApiResponse<T>> {
    const headers: Record<string, string> = {};
    if (options.ifNoneMatch) headers['If-None-Match'] = options.ifNoneMatch;
    return this.send<T>('GET', url, undefined, { params: options.params, headers });
  }

  /** A list page (R-API-010): `limit` and the opaque `cursor`, plus the caller's filters. */
  page<T>(url: string, params: QueryParams = {}): PageFetcher<T> {
    return async (cursor, limit): Promise<Page<T>> => {
      const query: Record<string, QueryParams[string]> = { ...params, limit };
      if (cursor !== null) query['cursor'] = cursor;
      return (await this.getJson<Page<T>>(url, { params: query })).body;
    };
  }

  /** Create or action: POST with an optional idempotency key. */
  post<T>(url: string, body?: unknown, options: CreateOptions = {}): Promise<ApiResponse<T>> {
    return this.send<T>('POST', url, body, {
      params: options.params,
      idempotencyKey: options.idempotencyKey,
    });
  }

  /**
   * Replaces a resource. `etag` is the tag the caller loaded (R-API-012): the UI never sends
   * `If-Match: *` on its own, so the parameter has no wildcard overload.
   */
  put<T>(url: string, body: unknown, etag: string): Promise<ApiResponse<T>> {
    return this.send<T>('PUT', url, body, { headers: { 'If-Match': etag } });
  }

  /** JSON Merge Patch of a resource (R-API-013) with the loaded `ETag`. */
  patch<T>(url: string, patch: unknown, etag: string): Promise<ApiResponse<T>> {
    return this.send<T>('PATCH', url, patch, {
      headers: { 'If-Match': etag, 'Content-Type': 'application/merge-patch+json' },
    });
  }

  // >>> M1 ui-b
  /**
   * JSON Merge Patch of a resource that carries no `ETag` (users, groups, service accounts,
   * R-API-013): the members present in the patch are the only ones that change, so a concurrent
   * edit of another member survives. Without `If-Match` the request is never retried after a
   * network error (R-UI-042).
   */
  patchMerge<T>(url: string, patch: unknown): Promise<ApiResponse<T>> {
    return this.send<T>('PATCH', url, patch, {
      headers: { 'Content-Type': 'application/merge-patch+json' },
    });
  }

  /** PATCH with a plain JSON body: the membership changes (`add`, `remove`) are not merge patches. */
  patchJson<T>(url: string, body: unknown): Promise<ApiResponse<T>> {
    return this.send<T>('PATCH', url, body, {});
  }

  /** PUT of a resource that is its own representation and has no body or `ETag` (role bindings). */
  putBinding<T>(url: string): Promise<ApiResponse<T>> {
    return this.send<T>('PUT', url, undefined, {});
  }
  // <<< M1 ui-b

  /** Delete; `etag` is optional because DELETE honours but does not require `If-Match`. */
  delete<T = void>(
    url: string,
    options: CreateOptions & { readonly etag?: string } = {},
  ): Promise<ApiResponse<T>> {
    const headers: Record<string, string> = {};
    if (options.etag) headers['If-Match'] = options.etag;
    return this.send<T>('DELETE', url, undefined, {
      params: options.params,
      headers,
      idempotencyKey: options.idempotencyKey,
    });
  }

  private async send<T>(
    method: string,
    url: string,
    body: unknown,
    extra: {
      readonly params?: QueryParams;
      readonly headers?: Record<string, string>;
      readonly idempotencyKey?: string;
    },
  ): Promise<ApiResponse<T>> {
    let context = new HttpContext();
    if (extra.idempotencyKey) context = context.set(IDEMPOTENCY_KEY, extra.idempotencyKey);
    const response = await firstValueFrom(
      this.http.request<T>(method, url, {
        body,
        params: toParams(extra.params),
        headers: extra.headers,
        context,
        observe: 'response',
        responseType: 'json',
      }),
    );
    return {
      body: response.body as T,
      status: response.status,
      etag: response.headers.get('ETag'),
      location: response.headers.get('Location'),
    };
  }
}
