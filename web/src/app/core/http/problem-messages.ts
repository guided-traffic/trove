import { ApiError } from '../../api/problem';

export interface ProblemText {
  /** The message to show. */
  readonly text: string;
  /**
   * False when the code has no translation: `text` is then the English `detail` of the server
   * and the screen shows the request ID with a copy button (R-UI-042).
   */
  readonly known: boolean;
}

/**
 * Translates a problem `code` (R-UI-042, R-UI-045). API text stays English; the UI maps the codes
 * it knows to translated sentences and falls back to the server's `detail` for the rest.
 * Resource-specific codes are added by the owning screens' areas as they are built.
 */
export function describeProblem(error: ApiError): ProblemText {
  const known = (text: string): ProblemText => ({ text, known: true });
  switch (error.code) {
    case 'unauthenticated':
      return known($localize`:@@problem.unauthenticated:You are not signed in, or your session has ended.`);
    case 'reauthentication_required':
      return known($localize`:@@problem.reauthentication_required:This action needs a recent sign-in.`);
    case 'forbidden':
      return known($localize`:@@problem.forbidden:You do not have the permission for this.`);
    case 'csrf_failed':
      return known($localize`:@@problem.csrf_failed:The request was refused as a possible forgery. Reload the page and try again.`);
    case 'not_found':
      return known($localize`:@@problem.not_found:This does not exist, or you may not see it.`);
    case 'already_exists':
      return known($localize`:@@problem.already_exists:An object with this key or name already exists.`);
    case 'conflict':
      return known($localize`:@@problem.conflict:The request conflicts with the current state.`);
    case 'idempotency_in_progress':
      return known($localize`:@@problem.idempotency_in_progress:The same request is still being processed. Try again in a moment.`);
    case 'idempotency_key_reused':
      return known($localize`:@@problem.idempotency_key_reused:This form was already submitted with different content. Reopen it and try again.`);
    case 'token_already_issued':
      return known($localize`:@@problem.token_already_issued:A token was already issued for this request.`);
    case 'precondition_failed':
      return known($localize`:@@problem.precondition_failed:Changed by someone else since you opened it.`);
    case 'precondition_required':
      return known($localize`:@@problem.precondition_required:The request did not say which version it changes. Reload the page and try again.`);
    case 'authz_epoch_unknown':
      return known($localize`:@@problem.authz_epoch_unknown:Your last change may not have been saved.`);
    case 'payload_too_large':
      return known($localize`:@@problem.payload_too_large:The request is too large.`);
    case 'validation_failed':
      return known($localize`:@@problem.validation_failed:Some entries are not valid.`);
    case 'malformed_request':
      return known($localize`:@@problem.malformed_request:The request could not be understood.`);
    case 'invalid_cursor':
      return known($localize`:@@problem.invalid_cursor:The list changed while you were paging. Start again from the first page.`);
    case 'rate_limited':
      return known(rateLimited(error.retryAfterSeconds));
    case 'upstream_error':
      return known($localize`:@@problem.upstream_error:An upstream service did not answer correctly.`);
    case 'unavailable':
    case 'authz_stale':
      return known($localize`:@@problem.unavailable:The service is temporarily unavailable.`);
    case 'internal':
      return known($localize`:@@problem.internal:Something went wrong on the server.`);
    default:
      return fallback(error);
  }
}

function rateLimited(seconds: number | null): string {
  return seconds === null
    ? $localize`:@@problem.rate_limited:Too many requests. Wait a moment and try again.`
    : $localize`:@@problem.rate_limited_wait:Too many requests. Try again in ${seconds}:seconds: seconds.`;
}

function fallback(error: ApiError): ProblemText {
  if (error.status === 0) {
    return {
      text: $localize`:@@problem.network:The server could not be reached. Check your connection and try again.`,
      known: true,
    };
  }
  return { text: error.problem?.detail ?? error.message, known: false };
}
