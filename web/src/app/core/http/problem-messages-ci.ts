/**
 * Translated messages of the problem codes of the GitLab CI federation (R-UI-042, R-UI-045, R-CI-002,
 * R-CI-010 to R-CI-014). `describeProblem` consults this table after the access table. The 422 codes
 * arrive as the `code` of the problem (an issuer's validation) or as `errors[].code` with the pointer
 * of the member (a mapping's save-time rules); both read from this one table. Returns null for a code
 * this table does not know. `action_not_allowed`, `repository_outside_project` and
 * `project_prefix_required` live in the access table.
 */
export function describeCiProblem(code: string | null): string | null {
  switch (code) {
    case 'ci_not_implemented':
      return $localize`:@@problem.ci.not-implemented:CI identity is not available yet on this server. Nothing is wrong with your session; the feature is still being switched on.`;
    case 'discovery_unreachable':
      return $localize`:@@problem.ci.discovery-unreachable:The issuer's discovery document or key set could not be fetched. Check the issuer URL, that the server can reach it, and the outbound proxy setting.`;
    case 'issuer_mismatch':
      return $localize`:@@problem.ci.issuer-mismatch:The discovery document names a different issuer than the one entered. The issuer URL must match the document exactly.`;
    case 'no_usable_keys':
      return $localize`:@@problem.ci.no-usable-keys:The issuer publishes no signing key that trove can use. Keys that are too weak or of an unknown type are ignored.`;
    case 'algorithms_not_offered':
      return $localize`:@@problem.ci.algorithms-not-offered:The issuer does not offer any of the allowed signing algorithms.`;
    case 'pin_claim_not_immutable':
      return $localize`:@@problem.ci.pin-claim-not-immutable:A pin claim must be an immutable ID, such as project_id or namespace_id, never a name or a path.`;
    case 'public_issuer_required':
      return $localize`:@@problem.ci.public-issuer-required:This issuer URL is public by construction, so it cannot be marked as a private issuer.`;
    case 'kind_not_available':
      return $localize`:@@problem.ci.kind-not-available:This kind of issuer is not available yet. Only GitLab issuers can be created.`;
    case 'issuer_managed_by_configuration':
      return $localize`:@@problem.ci.issuer-managed-by-configuration:This issuer is declared in the installation's configuration and can only be changed there.`;
    case 'job_token_fallback_not_available':
      return $localize`:@@problem.ci.job-token-fallback-not-available:The job token fallback is not available.`;
    case 'pin_required':
      return $localize`:@@problem.ci.pin-required:The issuer is public, so the condition must pin the project or the namespace: add an equals or in condition on an immutable ID such as project_id or namespace_id.`;
    case 'catch_all_condition':
      return $localize`:@@problem.ci.catch-all-condition:This condition matches every job of the issuer. Narrow it down to a project, a namespace or a ref.`;
    case 'unprotected_write':
      return $localize`:@@problem.ci.unprotected-write:A mapping that can publish, overwrite or delete must require protected refs: add the condition ref_protected = true, or allow unprotected refs in the options.`;
    case 'unpinned_write':
      return $localize`:@@problem.ci.unpinned-write:A mapping that can publish, overwrite or delete must pin the project or the namespace with an equals or in condition.`;
    case 'empty_grant':
      return $localize`:@@problem.ci.empty-grant:The mapping grants nothing. Add a policy, a group or an inline grant.`;
    case 'template_invalid':
      return $localize`:@@problem.ci.template-invalid:The template is not valid. Check the braces, the claim name and the filter names.`;
    case 'template_unanchored':
      return $localize`:@@problem.ci.template-unanchored:A template in a repository pattern needs literal text next to the claim, for example a fixed suffix after it.`;
    case 'condition_invalid':
      return $localize`:@@problem.ci.condition-invalid:The condition is not valid: an empty list, too deep (4 levels), too many conditions (64), too many values (256), or a pattern that does not compile.`;
    case 'mapping_limit_reached':
      return $localize`:@@problem.ci.mapping-limit-reached:This issuer has the maximum number of mappings. Delete one first.`;
    case 'priority_taken':
      return $localize`:@@problem.ci.priority-taken:Another mapping of this tier has this priority. Priorities are unique within a tier.`;
    case 'unknown_policy':
      return $localize`:@@problem.ci.unknown-policy:A policy of the list does not exist.`;
    case 'unknown_group':
      return $localize`:@@problem.ci.unknown-group:A group of the list does not exist.`;
    case 'groups_not_allowed_in_project':
      return $localize`:@@problem.ci.groups-not-allowed-in-project:A project's mapping cannot carry groups. Groups are global grants; use the project's policies or inline grants.`;
    case 'policy_outside_project':
      return $localize`:@@problem.ci.policy-outside-project:A project's mapping can carry only the project's own policies.`;
    case 'egress_denied':
      return $localize`:@@problem.ci.egress-denied:The server may not reach this address.`;
    case 'invalid_snippet':
      return $localize`:@@problem.ci.invalid-snippet:No snippet can be made for this mapping and client.`;
    default:
      return null;
  }
}
