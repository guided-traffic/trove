/** One field-level problem; `pointer` is an RFC 6901 pointer into the request body (R-API-009). */
export interface FieldProblem {
  readonly pointer: string;
  readonly code: string;
  readonly detail?: string;
}

/**
 * The `application/problem+json` body of every management API error (RFC 9457, R-API-009).
 * Resource-specific extensions are reachable through the index signature.
 */
export interface Problem {
  readonly type?: string;
  readonly title?: string;
  readonly status?: number;
  readonly detail?: string;
  readonly instance?: string;
  readonly code: string;
  readonly requestId?: string;
  readonly errors?: readonly FieldProblem[];
  readonly currentEtag?: string;
  readonly [extension: string]: unknown;
}

/** True for an object that carries the mandatory `code` member of a trove problem. */
export function isProblem(value: unknown): value is Problem {
  return (
    typeof value === 'object' &&
    value !== null &&
    typeof (value as { code?: unknown }).code === 'string'
  );
}

/**
 * Failure of an API call after the interceptors have run. `problem` is null when the answer was
 * not a problem document (a proxy error page, a network failure with `status` 0).
 */
export class ApiError extends Error {
  /**
   * The request was sent twice (R-UI-042). A 412 on a retried PUT or PATCH may mean that the
   * first attempt did commit: compare the stored resource with the draft before opening the
   * conflict dialog.
   */
  afterRetry = false;

  constructor(
    readonly status: number,
    readonly problem: Problem | null,
    readonly requestId: string | null,
    /** `Retry-After` in whole seconds, when the answer carried one. */
    readonly retryAfterSeconds: number | null = null,
  ) {
    super(problem?.detail ?? problem?.title ?? `HTTP ${status}`);
    this.name = 'ApiError';
  }

  /** The stable error code, or null without a problem document. */
  get code(): string | null {
    return this.problem?.code ?? null;
  }

  /** The `ETag` the server holds now, from a 412 (R-API-012). */
  get currentEtag(): string | null {
    return this.problem?.currentEtag ?? null;
  }

  /** Field problems by JSON pointer; the first problem of a pointer wins. */
  get fieldErrors(): ReadonlyMap<string, FieldProblem> {
    const map = new Map<string, FieldProblem>();
    for (const e of this.problem?.errors ?? []) {
      if (!map.has(e.pointer)) map.set(e.pointer, e);
    }
    return map;
  }
}

export function isApiError(value: unknown): value is ApiError {
  return value instanceof ApiError;
}
