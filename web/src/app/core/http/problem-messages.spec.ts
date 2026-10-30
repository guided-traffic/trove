import { ApiError } from '../../api/problem';
import { describeProblem } from './problem-messages';

describe('describeProblem', () => {
  it('R-UI-042 translates a known code and ignores the English detail', () => {
    const e = new ApiError(403, { code: 'forbidden', detail: 'missing repositories:write-config' }, 'r1');
    expect(describeProblem(e)).toEqual({ text: 'You do not have the permission for this.', known: true });
  });

  it('R-UI-042 an unknown code shows the English detail and is marked unknown, so the request ID is offered', () => {
    const e = new ApiError(409, { code: 'a_code_from_the_future', detail: 'The server explains it in English.' }, 'rid-1');
    expect(describeProblem(e)).toEqual({ text: 'The server explains it in English.', known: false });
  });

  // >>> M1 ui-b
  it('R-UI-042 the codes of the identity and permission screens are translated and known', () => {
    const e = new ApiError(409, { code: 'last_admin', detail: 'The last administrator cannot be removed.' }, 'rid-1');
    expect(describeProblem(e)).toEqual({ text: 'The last administrator binding cannot be removed.', known: true });
    expect(describeProblem(new ApiError(422, { code: 'unknown_principal' }, null)).known).toBe(true);
  });
  // <<< M1 ui-b

  // >>> M2 ownership
  it('R-UI-042 R-AUTHZ-019 the codes of package ownership and of the populate check are translated and known', () => {
    for (const code of ['package_owned_by_other_owner', 'last_owner', 'populate_not_permitted']) {
      const p = describeProblem(new ApiError(403, { code, detail: 'English detail' }, 'rid-1'));
      expect(p.known, code).toBe(true);
      expect(p.text, code).not.toBe('English detail');
    }
  });
  // <<< M2 ownership

  it('R-UI-042 a lost connection and a rate limit with a wait time are described', () => {
    expect(describeProblem(new ApiError(0, null, null)).text).toContain('could not be reached');
    expect(describeProblem(new ApiError(429, { code: 'rate_limited' }, null, 7)).text).toContain('7');
  });

  it('R-UI-042 every generic code of R-API-009 that the UI handles has a translated text', () => {
    for (const code of [
      'unauthenticated', 'reauthentication_required', 'forbidden', 'csrf_failed', 'not_found',
      'already_exists', 'conflict', 'idempotency_in_progress', 'idempotency_key_reused',
      'token_already_issued', 'precondition_failed', 'precondition_required',
      'authz_epoch_unknown', 'payload_too_large', 'validation_failed', 'malformed_request',
      'invalid_cursor', 'rate_limited', 'upstream_error', 'unavailable', 'authz_stale', 'internal',
    ]) {
      expect(describeProblem(new ApiError(400, { code }, null)).known, code).toBe(true);
    }
  });
});
