import { ApiError } from '../../api/problem';
// >>> M1 ui-b
import { describeAccessProblem } from './problem-messages-access';
// <<< M1 ui-b

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
    // >>> M1 ui-a
    case 'repository_not_found':
      return known($localize`:@@problem.repository_not_found:This repository does not exist, or you may not see it.`);
    case 'repository_disabled':
      return known($localize`:@@problem.repository_disabled:This repository is disabled.`);
    case 'repository_referenced':
      return known($localize`:@@problem.repository_referenced:Other repositories still include this one.`);
    case 'invalid_key':
      return known($localize`:@@problem.invalid_key:The key must have 2 to 64 characters: lower-case letters, digits, dot, underscore or hyphen, starting with a letter or digit, without two dots in a row and not ending in a dot.`);
    case 'reserved_name':
      return known($localize`:@@problem.reserved_name:This name is reserved.`);
    case 'invalid_pattern':
      return known($localize`:@@problem.invalid_pattern:This pattern is not valid.`);
    case 'invalid_document':
      return known($localize`:@@problem.invalid_document:The document contains a member that is unknown or misspelt.`);
    case 'immutable':
      return known($localize`:@@problem.immutable_field:This field cannot be changed after creation.`);
    case 'not_applicable':
      return known($localize`:@@problem.not_applicable:This setting does not apply to this class or format.`);
    case 'unsupported_class':
      return known($localize`:@@problem.unsupported_class:This format does not support the chosen class.`);
    case 'invalid_path':
      return known($localize`:@@problem.invalid_path:The path is not valid: no empty, dot or double-dot segments, no backslash or control characters.`);
    case 'path_conflict':
      return known($localize`:@@problem.path_conflict:A file and a folder cannot share a path.`);
    case 'not_admitted':
      return known($localize`:@@problem.not_admitted:The include and exclude patterns of this repository do not admit this path.`);
    case 'content_unavailable':
      return known($localize`:@@problem.content_unavailable:The content is temporarily unavailable. Try again later.`);
    case 'endpoint_not_configured':
      return known($localize`:@@problem.endpoint_not_configured:This endpoint is no longer configured, so its rules cannot be created or replaced.`);
    case 'exposure_rule_limit_reached':
      return known($localize`:@@problem.exposure_rule_limit_reached:This endpoint has reached the maximum number of exposure rules.`);
    case 'format_not_exposable':
      return known($localize`:@@problem.format_not_exposable:Repositories of this format cannot be exposed through rules yet.`);
    case 'invalid_version_range':
      return known($localize`:@@problem.invalid_version_range:The version range is not valid in the syntax of this format.`);
    case 'dimension_not_supported':
      return known($localize`:@@problem.dimension_not_supported:This format has no such filter.`);
    case 'invalid_audit_range':
      return known($localize`:@@problem.invalid_audit_range:The export needs a start and an end, at most 400 days apart.`);
    case 'key_ring_incompatible':
      return known($localize`:@@problem.key_ring_incompatible:The replicas do not hold the same key IDs, so the secrets cannot be re-encrypted yet.`);
    // <<< M1 ui-a
    //
    // >>> M2 ownership
    case 'package_owned_by_other_owner':
      return known($localize`:@@problem.package_owned_by_other_owner:This package name belongs to other owners. Ask one of them, or someone who manages the repository, to add you.`);
    case 'last_owner':
      return known($localize`:@@problem.last_owner:This is the only owner of the package. Add the new owner first, or release the name.`);
    case 'populate_not_permitted':
      return known($localize`:@@problem.populate_not_permitted:This content is not in the cache, and you may not fetch it from the upstream.`);
    // <<< M2 ownership
    default:
      // >>> M1 ui-b
      return accessProblem(error) ?? fallback(error);
      // <<< M1 ui-b
  }
}

// >>> M1 ui-b
function accessProblem(error: ApiError): ProblemText | null {
  const text = describeAccessProblem(error.code);
  return text === null ? null : { text, known: true };
}
// <<< M1 ui-b

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
