import {
  HttpContextToken,
  HttpErrorResponse,
  HttpEvent,
  HttpInterceptorFn,
  HttpRequest,
  HttpResponse,
} from '@angular/common/http';
import { Injector, inject } from '@angular/core';
import { Observable, catchError, of, switchMap, tap, throwError, timer } from 'rxjs';
import { IDEMPOTENCY_KEY } from '../../api/client';
import { ApiError, isProblem, Problem } from '../../api/problem';
import { AuthzEpoch } from '../authz-epoch';
import { SessionStore } from '../session-store';

/** Requests to these prefixes carry the trove conventions; everything else passes untouched. */
const API_PREFIXES = ['/api/v1/', '/auth/'];

/** Calls whose 401 means "not signed in yet", not "the session ended". */
const SESSION_PROBES = ['/api/v1/auth/whoami', '/api/v1/auth/login-options'];

/** Longest `Retry-After` the UI waits for before an automatic retry. */
export const MAX_AUTO_RETRY_WAIT_S = 30;

export function isApiUrl(url: string): boolean {
  return API_PREFIXES.some((p) => url.startsWith(p));
}

/** Retry bookkeeping of one request; shared by reference through the request context. */
export interface RetryState {
  retried: boolean;
  /** The retry followed a failure without an answer (status 0). */
  afterNetworkError: boolean;
}

export const RETRY_STATE = new HttpContextToken<RetryState>(() => ({
  retried: false,
  afterNetworkError: false,
}));

function problemOf(err: HttpErrorResponse): Problem | null {
  return isProblem(err.error) ? err.error : null;
}

function retryAfterSeconds(err: HttpErrorResponse): number | null {
  const raw = err.headers?.get('Retry-After');
  if (!raw) return null;
  const n = Number.parseInt(raw, 10);
  return Number.isFinite(n) && n >= 0 ? n : null;
}

/**
 * Innermost: the headers of the conventions. `Trove-Authz-Min-Epoch` from the tab's epoch
 * (R-UI-042), `Idempotency-Key` from the request context (R-API-014), JSON accept; and the
 * `Trove-Authz-Epoch` of a successful answer goes back into the epoch holder. CSRF is not here:
 * Angular's XSRF support adds `X-CSRF-Token` to unsafe same-origin requests
 * (`withXsrfConfiguration`, R-AUTH-012).
 */
export const conventionsInterceptor: HttpInterceptorFn = (req, next) => {
  if (!isApiUrl(req.url)) return next(req);
  const epoch = inject(AuthzEpoch);
  let headers = req.headers;
  if (!headers.has('Accept')) headers = headers.set('Accept', 'application/json, application/problem+json');
  const min = epoch.value();
  if (min !== null) headers = headers.set('Trove-Authz-Min-Epoch', String(min));
  const key = req.context.get(IDEMPOTENCY_KEY);
  if (key) headers = headers.set('Idempotency-Key', key);
  return next(req.clone({ headers })).pipe(
    tap((event) => {
      if (event instanceof HttpResponse) {
        const raw = event.headers.get('Trove-Authz-Epoch');
        if (raw) epoch.note(Number.parseInt(raw, 10));
      }
    }),
  );
};

/** A request is safe to send twice when repeating it cannot create or start something twice. */
function repeatable(req: HttpRequest<unknown>): boolean {
  switch (req.method) {
    case 'GET':
    case 'HEAD':
    case 'DELETE':
      return true;
    case 'PUT':
    case 'PATCH':
      return req.headers.has('If-Match');
    default:
      return req.context.get(IDEMPOTENCY_KEY) !== null;
  }
}

/** Seconds to wait before the one automatic retry, or null for no retry (R-UI-042). */
function retryDelaySeconds(
  req: HttpRequest<unknown>,
  err: HttpErrorResponse,
  epoch: AuthzEpoch,
): number | null {
  const code = problemOf(err)?.code;
  const wait = Math.min(retryAfterSeconds(err) ?? 1, MAX_AUTO_RETRY_WAIT_S);
  const read = req.method === 'GET' || req.method === 'HEAD';
  if (err.status === 0) return repeatable(req) ? 0 : null;
  if (err.status === 429) return read ? wait : null;
  if (err.status === 503 && code === 'authz_stale') return read ? wait : null;
  if (err.status === 409 && code === 'idempotency_in_progress') {
    return req.context.get(IDEMPOTENCY_KEY) !== null ? wait : null;
  }
  if (err.status === 412 && code === 'authz_epoch_unknown') {
    // Without this every later request would fail the same way.
    epoch.clear();
    return read ? 0 : null;
  }
  return null;
}

/**
 * Middle: the one automatic retry of R-UI-042. A GET after a network error, 429 or
 * `authz_stale`; a request with an idempotency key after a network error or
 * `idempotency_in_progress`; a PUT or PATCH with its `If-Match` after a network error; a DELETE
 * after a network error, where a 404 on the retry counts as done; a GET without the epoch
 * header after `authz_epoch_unknown`. Any other unsafe request is never repeated.
 */
export const resilienceInterceptor: HttpInterceptorFn = (req, next) => {
  if (!isApiUrl(req.url)) return next(req);
  const epoch = inject(AuthzEpoch);
  const state = req.context.get(RETRY_STATE);
  const run = (): Observable<HttpEvent<unknown>> =>
    next(req).pipe(
      catchError((err: unknown) => {
        if (!(err instanceof HttpErrorResponse)) return throwError(() => err);
        if (state.retried) {
          if (state.afterNetworkError && req.method === 'DELETE' && err.status === 404) {
            return of(new HttpResponse({ status: 204, url: req.url }));
          }
          return throwError(() => err);
        }
        const delay = retryDelaySeconds(req, err, epoch);
        if (delay === null) return throwError(() => err);
        state.retried = true;
        state.afterNetworkError = err.status === 0;
        return timer(delay * 1000).pipe(switchMap(() => run()));
      }),
    );
  return run();
};

/**
 * Outermost: maps a failed answer to an `ApiError` (R-UI-042) and applies the session
 * consequences once, after the retry layer gave up. 401 `unauthenticated` during use ends the
 * session (dialog "Your session has ended"); 401 `reauthentication_required` asks for recent
 * authentication; 403 drops the cached decisions.
 */
export const problemInterceptor: HttpInterceptorFn = (req, next) => {
  if (!isApiUrl(req.url)) return next(req);
  const injector = inject(Injector);
  return next(req).pipe(
    catchError((err: unknown) => {
      if (!(err instanceof HttpErrorResponse)) return throwError(() => err);
      const problem = problemOf(err);
      const apiError = new ApiError(
        err.status,
        problem,
        problem?.requestId ?? err.headers?.get('X-Request-Id') ?? null,
        retryAfterSeconds(err),
      );
      apiError.afterRetry = req.context.get(RETRY_STATE).retried;
      const session = injector.get(SessionStore);
      if (err.status === 401) {
        if (problem?.code === 'reauthentication_required') {
          session.markReauthRequired();
        } else if (!SESSION_PROBES.some((p) => req.url.startsWith(p))) {
          session.markEnded();
        }
      } else if (err.status === 403) {
        session.dropCheckCache();
      }
      return throwError(() => apiError);
    }),
  );
};
