/**
 * Translated messages of the problem codes of the identity, token, login and permission areas
 * (R-UI-042, R-UI-045). `describeProblem` consults this table before it falls back to the server's
 * English `detail`. Returns null for a code this table does not know.
 */
export function describeAccessProblem(code: string | null): string | null {
  switch (code) {
    case 'invalid_credentials':
      return $localize`:@@problem.invalid_credentials:The user name or the password is wrong.`;
    case 'break_glass_locked':
      return $localize`:@@problem.break_glass_locked:The local account is locked after too many failed attempts. Try again later.`;
    case 'break_glass_rotating':
      return $localize`:@@problem.break_glass_rotating:The local password was just replaced. Try again in a moment.`;
    case 'break_glass_restricted':
      return $localize`:@@problem.break_glass_restricted:The local account can neither own nor create tokens.`;
    case 'break_glass_immutable':
      return $localize`:@@problem.break_glass_immutable:The local account cannot be changed here.`;
    case 'local_login_disabled':
      return $localize`:@@problem.local_login_disabled:Local sign-in is switched off.`;
    case 'too_many_pending_logins':
      return $localize`:@@problem.too_many_pending_logins:Too many sign-ins are in progress. Try again in a moment.`;
    case 'provider_unavailable':
      return $localize`:@@problem.provider_unavailable:The identity provider is not available right now.`;
    case 'principal_disabled':
      return $localize`:@@problem.principal_disabled:This account is disabled.`;
    case 'reserved_name':
      return $localize`:@@problem.reserved_name:This name is reserved.`;
    case 'username_invalid':
      return $localize`:@@problem.username_invalid:The user name may contain lower-case letters, digits and . _ @ + - only, and must start with a letter or digit.`;
    case 'managed_by_scim':
      return $localize`:@@problem.managed_by_scim:This object is managed by SCIM and cannot be changed here.`;
    case 'managed_by_config':
      return $localize`:@@problem.managed_by_config:This object is managed by the installation's configuration and cannot be changed here.`;
    case 'last_admin':
      return $localize`:@@problem.last_admin:The last administrator binding cannot be removed.`;
    case 'auto_join_admin_conflict':
      return $localize`:@@problem.auto_join_admin_conflict:An auto-join group cannot hold the administrator role.`;
    case 'role_not_allowed':
      return $localize`:@@problem.role_not_allowed:This role cannot be bound to this kind of principal.`;
    case 'unknown_role':
      return $localize`:@@problem.unknown_role:This role does not exist.`;
    case 'invalid_member':
      return $localize`:@@problem.invalid_member:A member does not exist or cannot be added.`;
    case 'invalid_ttl':
      return $localize`:@@problem.invalid_ttl:The lifetime is not valid.`;
    case 'ttl_exceeds_maximum':
      return $localize`:@@problem.ttl_exceeds_maximum:The lifetime is longer than the maximum for this kind of token.`;
    case 'default_above_maximum':
      return $localize`:@@problem.default_above_maximum:The default lifetime cannot be longer than the maximum.`;
    case 'exceeds_ceiling':
      return $localize`:@@problem.exceeds_ceiling:The value is above the ceiling set in the installation's configuration.`;
    case 'non_expiring_not_allowed':
      return $localize`:@@problem.non_expiring_not_allowed:A token without an expiry is not allowed here.`;
    case 'non_expiring_restricted':
      return $localize`:@@problem.non_expiring_restricted:Only a service-account token can be created without an expiry.`;
    case 'invalid_scope':
      return $localize`:@@problem.invalid_scope:The scope is not valid.`;
    case 'derivation_exceeds_parent':
      return $localize`:@@problem.derivation_exceeds_parent:A token cannot grant more than the token that creates it.`;
    case 'description_required':
      return $localize`:@@problem.description_required:Enter a description.`;
    case 'token_limit_reached':
      return $localize`:@@problem.token_limit_reached:The limit of tokens for this owner is reached. Revoke a token first.`;
    case 'pat_not_allowed':
      return $localize`:@@problem.pat_not_allowed:A personal token cannot be created with this credential.`;
    case 'ci_token_restricted':
      return $localize`:@@problem.ci_token_restricted:A token issued to a pipeline cannot do this.`;
    case 'not_a_token':
      return $localize`:@@problem.not_a_token:The current credential is not a token.`;
    case 'anonymous_not_allowed':
      return $localize`:@@problem.anonymous_not_allowed:Anonymous access is not allowed by the installation's configuration.`;
    case 'invalid_action':
      return $localize`:@@problem.invalid_action:An action is not valid.`;
    case 'invalid_pattern':
      return $localize`:@@problem.invalid_pattern:A pattern is not valid.`;
    case 'unknown_principal':
      return $localize`:@@problem.unknown_principal:A principal does not exist.`;
    case 'unknown_repository':
      return $localize`:@@problem.unknown_repository:A repository selector names a repository that does not exist.`;
    case 'unknown_format':
      return $localize`:@@problem.unknown_format:A selector names a format that does not exist.`;
    case 'duplicate_principal':
      return $localize`:@@problem.duplicate_principal:A principal appears twice in the policy.`;
    case 'anonymous_write_action':
      return $localize`:@@problem.anonymous_write_action:The anonymous principal can only read.`;
    case 'anonymous_dynamic_selector':
      return $localize`:@@problem.anonymous_dynamic_selector:The anonymous principal needs named repositories, not patterns or classes.`;
    case 'authenticated_write_action':
      return $localize`:@@problem.authenticated_write_action:The group of all signed-in users can only read.`;
    case 'action_not_allowed':
      return $localize`:@@problem.action_not_allowed:This principal cannot hold this action.`;
    case 'virtual_action_not_applicable':
      return $localize`:@@problem.virtual_action_not_applicable:A virtual repository accepts only read and manage.`;
    default:
      return null;
  }
}
