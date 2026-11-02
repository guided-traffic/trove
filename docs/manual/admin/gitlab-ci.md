# GitLab CI federation

How GitLab CI jobs get short-lived trove tokens without a stored secret, and how administrators set it up: registering a GitLab instance, letting the pipelines of a GitLab project publish to the repositories of a project, giving a whole GitLab instance or group read access, testing mappings with the dry run, reading the issuance records, revoking tokens and troubleshooting refused exchanges.

> **Not available in this version yet.** GitLab CI federation is being built. Until it is released, the token exchange and every CI operation of the management API answer `501` with the code `ci_not_implemented`, and the administration area has no CI screens. This page describes the behaviour the feature is built to, so that you can plan with it. Until then, pipelines use a service-account token as [Permissions](permissions.md#a-ci-service-account) shows.

## What the federation does

1. A GitLab job declares an `id_tokens:` entry, and GitLab gives it an ID token: a JWT that the instance signs and addresses to trove (its `aud` is trove's base URL).
2. The job posts the ID token to `POST /api/v1/auth/oidc/token`. The ID token is the only credential; nothing secret is stored in GitLab.
3. trove checks the signature against the keys the instance publishes, the audience and the times, and then evaluates the instance's identity mappings: ordered rules that turn verified claims such as `project_id` and `ref_protected` into grants.
4. It answers with a `trv_cit_` token that lives 15 minutes by default, at most one hour, and never longer than the ID token, which GitLab lets expire with the job's timeout.
5. Every exchange writes an issuance record naming the GitLab project, pipeline, job and ref, and the first publish of a package name binds the name to the GitLab project that published it.

```text
GitLab instance ── ID token, aud = trove's base URL ──▶ GitLab job
                                                            │  POST /api/v1/auth/oidc/token
                                                            ▼
                                trove: issuer → signing keys → mappings → trv_cit_ token
                                                            │
                         npm, dotnet in the job ◀───────────┘  TROVE_TOKEN, valid for minutes
```

The words this page uses:

- **CI issuer**: a GitLab instance that trove trusts, with its validation settings. It only serves the token exchange; it is not an identity provider that people sign in with.
- **Mapping**: an ordered rule of one issuer, made of a condition on the ID token's claims and the grants a matching job receives. Each mapping is a principal of its own, `ci:<issuer>/<mapping>`. A CI token acts as its mappings: it is never a user, never part of `authenticated`, never joins a group automatically, holds no system role and never `manage`.
- **Issuance record**: the stored link between a CI token and the verified job it was issued to.

The pipeline author's side, the `id_tokens:` block and the exchange, is in [GitLab CI pipelines](../user/gitlab-ci-pipelines.md).

## Who does what

| Task | Who | Where |
|---|---|---|
| Trust a GitLab instance (a CI issuer) | Your Helm values, or holders of `ci:write` (roles `admin` and `security-admin`) | `config.ci.issuers`; `/api/v1/ci/issuers` |
| Mappings for the whole installation (tier `system`) | Holders of `ci:write` | `/api/v1/ci/issuers/{issuer}/mappings` |
| Mappings of a project (tier `project:<key>`) | The project's administrators while its switch **Manage policies and service accounts** is on, and holders of `ci:write` | `/api/v1/projects/{key}/ci/…` |
| Read issuers, mappings and the dry run | Holders of `ci:read` (`admin`, `security-admin`, `auditor`); a project's administrators for their project, with the issuers read-only | as above |
| Read issuance records | Holders of `tokens:read` (`admin`, `security-admin`, `auditor`); a project's administrators for the tokens its mappings contributed to | `/api/v1/ci/issuances`; `/api/v1/projects/{key}/ci/issuances` |
| Revoke CI tokens | Holders of `tokens:revoke` (`admin`, `security-admin`); a project's administrators by disabling or deleting a mapping of the project; the job itself | [Revoke CI tokens](#revoke-ci-tokens) |
| Write the pipeline | The developers of the GitLab project | [GitLab CI pipelines](../user/gitlab-ci-pipelines.md) |

To a project administrator, a project it does not administer answers `404` and a switch that is off `403` `project_switch_off`, as for every project operation ([Projects and delegated administration](projects.md)).

### Calling the operations

The examples call the API with the shell function `trove_curl` from [Personal access tokens](../user/access-tokens.md#with-curl-and-the-management-api), which hands curl the token in `TROVE_TOKEN` on standard input. The token has to exercise your administration, so it needs the administrator scope ([Scopes](tokens.md#scopes)), which the token form of the UI does not set. A browser session works as well; creating, changing and deleting issuers and mappings through it needs a recent sign-in.

- Field names on the wire are lowerCamelCase (`allowedAudiences`, `crossProjectSource`, `maxTtlSeconds`). Claim names inside conditions and templates keep GitLab's spelling (`project_id`).
- `GET` of an issuer or mapping returns an `ETag`. `PUT` replaces the whole record and requires that tag in `If-Match` (`428` `precondition_required` without it, `412` `precondition_failed` with a stale one), so a change is read, modify, write. A member left out of a `PUT` takes its default.
- A refused write is a problem with a `code` (`pin_required`, `unprotected_write`, …) and the pointer of the member, as described in [Errors and troubleshooting](../user/troubleshooting.md#how-trove-reports-an-error).

## Register a GitLab instance

An issuer is one GitLab instance. Several instances are several issuers, each with its own mappings: the token's `iss` claim selects exactly one issuer, and its keys alone verify the token.

### In the Helm values

Declare the instances under `config.ci.issuers`, so that your GitOps repository provisions them:

```yaml
config:
  ci:
    issuers:
      - name: gitlab-main                       # example; ^[a-z0-9][a-z0-9._-]{1,63}$
        kind: gitlab                            # the only kind of this version
        issuer: https://gitlab.example.com      # example; exactly the iss of the instance's ID tokens
        public_issuer: false                    # example: an instance without open sign-up; default true
        description: Self-managed GitLab        # example
```

- The migration Job (`trove migrate`, run before every install and upgrade) applies the list in one transaction: each entry is created or replaced by `name` and shows `source: configuration`. No `web` or `worker` replica writes these issuers.
- They are read-only in the API: `PUT`, `DELETE`, enabling and disabling answer `409` `issuer_managed_by_configuration`. Change them in your values. Their mappings are managed through the API like any other.
- An entry you remove is disabled at the next upgrade, not deleted: it shows `removedFromConfiguration: true`, refuses every exchange with `issuer_disabled`, and keeps its mappings and issuance records. Only then may an administrator delete it through the API.
- The migration stops with an error that names both when an entry has the name of an issuer created through the API, or the issuer URL of another issuer; nothing is applied.
- The start-up check refuses an entry whose name breaks the pattern, a kind other than `gitlab` (`github` and `generic` are not available in this version), an issuer URL that is not `https` or appears twice, an algorithm outside the list below, `clock_skew_seconds` outside 0 to 300, an empty `allowed_audiences`, and `max_ttl_seconds` above `ci.token.max_ttl`.

Every field of an issuer can be set here in snake_case, except the job-token fallback:

<details>
<summary>The fields of an issuer</summary>

| Helm value | API field | Default | Meaning |
|---|---|---|---|
| `name` | `name` | required | The handle in the API, the audit trail and the metrics; immutable |
| `kind` | `kind` | required | `gitlab`; `github` and `generic` answer `422` `kind_not_available` |
| `issuer` | `issuer` | required | The expected `iss`, compared exactly, with no trailing-slash or case normalization; an `https` URL, unique, immutable. For GitLab, the instance's base URL |
| `description` | `description` | `""` | Free text |
| `enabled` | `enabled` | `true` | A disabled issuer refuses every exchange; disabling revokes the unexpired tokens issued through it |
| `discovery_url` | `discoveryUrl` | `null`: `<issuer>/.well-known/openid-configuration` | The OpenID discovery document; its `issuer` must equal `issuer` exactly |
| `jwks_uri` | `jwksUri` | `null`: the `jwks_uri` of the discovery document | Overrides where the signing keys are fetched; `https` only |
| `allowed_audiences` | `allowedAudiences` | the base URL and `npm:<host name of the base URL>` | The accepted `aud` values, exact strings, at least one. The second default value is reserved for the npm clients' own trusted publishing, which this version does not offer |
| `exclusive_audience` | `exclusiveAudience` | `true` | Every element of `aud` must be an allowed audience, so a token addressed to another service as well is refused |
| `allowed_algorithms` | `allowedAlgorithms` | `["RS256"]` | Of `RS256`, `RS384`, `RS512`, `PS256`, `PS384`, `PS512`, `ES256`, `ES384`, `EdDSA`; `none` and the HMAC algorithms never |
| `allowed_typ` | `allowedTyp` | `["JWT"]` | Accepted `typ` header values, compared case-insensitively |
| `allow_missing_typ` | `allowMissingTyp` | `true` | Accept tokens without a `typ` header |
| `clock_skew_seconds` | `clockSkewSeconds` | `60` | Leeway for `exp`, `nbf` and `iat`, at most 300 |
| `required_claims` | `requiredClaims` | the GitLab preset | Claims that must be present and not null. The preset: `iss`, `aud`, `exp`, `iat`, `jti`, `sub`, `project_id`, `namespace_id`, `project_path`, `namespace_path`, `ref`, `ref_type`, `ref_protected`, `pipeline_source`, `pipeline_id`, `job_id`, `user_id`, `user_login` |
| `public_issuer` | `publicIssuer` | `true` | Anyone can obtain ID tokens from the instance; turns on the pinning rule ([below](#public-instances-and-gitlabcom)) |
| `pin_claims` | `pinClaims` | `project_id`, `namespace_id`, `job_project_id`, `job_namespace_id` | The claims that count as immutable IDs for the pinning rule; only a subset of the default (`422` `pin_claim_not_immutable` otherwise) |
| `single_use_subject_tokens` | `singleUseSubjectTokens` | `false` | Refuse a second exchange of the same ID token (`subject_token_replayed`). Off for GitLab, because a job has one ID token for its whole run |
| `max_ttl_seconds` | `maxTtlSeconds` | `null`: only the global bound | Upper bound of this issuer's CI tokens, at most `ci.token.max_ttl` |
| `ca_bundle_ref` | `caBundleRef` | `null` | A reference to a PEM bundle for an instance behind a private CA ([below](#an-instance-behind-a-private-ca)); never key material |
| `use_outbound_proxy` | `useOutboundProxy` | `false` | Fetch the discovery document and the keys through the installation's outbound proxy |
| — | `validate` | `true` | API create only: fetch the discovery document and the keys before the issuer is stored |
| — | `source`, `removedFromConfiguration`, `mappingCount`, `keys`, `version`, `createdAt`, `updatedAt` | read-only | Shown on read; accepted and ignored on write |

</details>

### Through the API

An instance that is not in the values is created by a holder of `ci:write` with `POST /api/v1/ci/issuers`:

```json
{
  "name": "gitlab-com",
  "kind": "gitlab",
  "issuer": "https://gitlab.com",
  "description": "GitLab.com, group acme"
}
```

```shell
trove_curl -X POST -H 'Content-Type: application/json' --data @issuer.json https://trove.example.com/api/v1/ci/issuers
```

The answer is `201` with the full record, every default filled in. With `validate` left at `true`, trove first fetches the discovery document and the key set and refuses an issuer that would not work with `422`: `discovery_unreachable`, `issuer_mismatch` (the document's `issuer` is not exactly yours), `no_usable_keys` or `algorithms_not_offered`. A name or issuer URL that exists answers `409`.

| Call | Does |
|---|---|
| `GET /api/v1/ci/issuers` | Lists the issuers with their mapping count and key status |
| `GET /api/v1/ci/issuers/{name}/keys` | The stored key set: key IDs, types, algorithms, ignored keys with the reason, the fetch time, the next refresh and the last error |
| `POST /api/v1/ci/issuers/{name}/keys/refresh` | Fetches the discovery document and the keys now; `502` when the instance is unreachable |
| `PUT /api/v1/ci/issuers/{name}` | Replaces the issuer; `name`, `issuer` and `kind` are immutable |
| `DELETE /api/v1/ci/issuers/{name}` | Deletes it; refused with `409` while it has mappings unless `?cascade=true`, which deletes them and their principals in the same transaction |

Disabling or deleting an issuer revokes every unexpired token issued through it. trove refreshes the keys of every enabled issuer on its own: the task `ci-keys-refresh` runs on the `worker` replicas every five minutes and fetches each issuer whose refresh is due, which follows the instance's `Cache-Control: max-age` within 5 minutes and 24 hours, and is one hour without it. A token that names a key trove does not know triggers a fetch at once, at most once a minute per issuer and replica. A key that the instance removes from its key set stops being accepted after the next fetch.

### Public instances and gitlab.com

Anyone with an account on gitlab.com, or on any instance with open sign-up, can run a pipeline and ask GitLab for an ID token with trove's audience. A valid signature and the right audience therefore prove only that some job on that instance asked for the token, not whose job it is. For an issuer with `public_issuer: true` (the default) every mapping must be **pinned**: the top-level `all` of its condition, or the condition itself if it is a single leaf, holds an `equals` or `in` leaf on one of the issuer's pin claims. Otherwise the save answers `422` `pin_required`.

Pin claims are IDs because paths are not stable: a GitLab project or group path changes on rename and transfer, and a released path can be registered by someone else. `https://gitlab.com` is public by construction, so `public_issuer: false` is refused for it (`422` `public_issuer_required`). Set `public_issuer: false` only for an instance where nobody outside your organization can create an account.

### An instance behind a private CA

trove connects to the instance over TLS with the system roots plus the issuer's `ca_bundle_ref`. The issuer stores only the reference; the bundle is a file in the `web` and `worker` pods. Mount it with the chart's extension points and name its path:

```yaml
extraVolumes:
  - name: gitlab-ca
    secret:
      secretName: gitlab-ca                     # example: a Secret with the key ca.crt
extraVolumeMounts:
  - name: gitlab-ca
    mountPath: /var/run/secrets/gitlab-ca       # example
    readOnly: true
config:
  ci:
    issuers:
      - name: gitlab-main                       # example
        kind: gitlab
        issuer: https://gitlab.example.com      # example
        public_issuer: false                    # example
        ca_bundle_ref: file:/var/run/secrets/gitlab-ca/ca.crt   # example
```

The fetches follow at most three redirects, only to `https`, and pass the installation's outbound address rules (`proxy.outbound.address_policy`): private ranges are permitted by default, loopback and link-local addresses are not, and a refused fetch shows the class `egress_denied` in the key status. URLs come from the issuer record only, never from a token.

### Audiences

The job's `aud` must be one of the issuer's `allowedAudiences`, compared exactly. With the default list, that is trove's base URL as configured in `server.base_url`, for example `https://trove.example.com`. If clients reach trove under further origins (`server.additional_base_urls`) and a pipeline uses one of them as its audience, add it to the list. With `exclusive_audience` on, an ID token whose `aud` also names another service is refused (`audience_not_exclusive`): that service could replay it at trove. Pipelines request one ID token per service instead.

## How mappings are evaluated

- Only enabled mappings of the issuer that the token's `iss` selects are considered.
- Mappings are grouped in **tiers**: `system`, and one tier `project:<key>` per project. Within a tier they are evaluated in ascending `priority`, and the first one whose condition holds is the tier's match; the rest of the tier is not looked at. Across tiers the matches add up: a job that matches a system mapping and a mapping of project `web` carries the grants of both. A project's mapping can therefore neither shadow nor narrow a system mapping or another project's.
- Because the first match decides, a mapping lists everything its jobs need, reading included. Put narrow rules at low numbers and broad ones at high numbers, for example a publish mapping at 100 and a read mapping for the whole group at 900.
- No match in any tier refuses the exchange with `no_matching_mapping`. A matched mapping whose grants cannot be rendered refuses it with `grant_render_failed`; evaluation never falls through to the next mapping.
- A mapping grants in up to three ways: **inline grants** (repositories, package-name or path patterns, actions and ownership), **policies** that list the mapping as a principal, and, in the system tier only, **groups**. Inline grants are frozen into the token at the exchange; policy and group grants are evaluated when the token is used, so changing a policy affects tokens already issued.
- A token's rights are those grants, narrowed by the `scope` the job asked for. The package ownership of the repository decides every write on top.

### The bounds of a project mapping

A project's administrators manage the mappings of their project's tier, with every rule below plus these bounds:

- The name starts with the project key and a hyphen (`422` `project_prefix_required`), so the principal `ci:<issuer>/<key>-…` is unique.
- Inline grants reach the project's repositories only: `@any` means every repository of the project, and a key outside the project is refused (`422` `repository_outside_project`).
- `policies` name policies of the project only (`422` `policy_outside_project`); `groups` are refused (`422` `groups_not_allowed_in_project`); `manage` is never grantable (`422` `action_not_allowed`).
- They list and dry-run only their project's tier, and see the issuance records of tokens that one of their mappings contributed to, showing their mappings only.
- A project with CI mappings is not empty and cannot be deleted (`409` `project_not_empty`).

The issuers stay with the system administrators: a project administrator lists them read-only with `GET /api/v1/projects/{key}/ci/issuers` (name, kind, issuer URL, `publicIssuer`).

### Rules checked on save

| Code | Refused when |
|---|---|
| `catch_all_condition` | The condition has no leaf |
| `pin_required` | The issuer is public and the condition has no `equals` or `in` leaf on a pin claim at its top level |
| `unprotected_write` | The grants hold `deploy`, `overwrite` or `delete`, and the top-level `all` has no `{"claim": "ref_protected", "equals": true}`, unless `options.allowUnprotectedRefs` is `true` |
| `unpinned_write` | On a private issuer, a mapping without a pin writes through a policy or a group, or through an inline grant without `ownership: bind_project` |
| `action_not_allowed` | `manage` in any grant |
| `empty_grant` | No policy, group or inline grant |
| `condition_invalid` | Deeper than 4 levels, more than 64 leaves, more than 256 values in an `in` list, a regular expression over 512 characters or one that does not compile, an invalid glob, a leaf with no or several operators |
| `template_invalid`, `template_unanchored` | A template with a syntax error or an unknown filter; a `repositories` template without literal text next to its substitution |
| `unknown_policy`, `unknown_group` | A named policy or group does not exist |
| `mapping_limit_reached` | The issuer holds `ci.mappings.max_per_issuer` mappings |
| `priority_taken` (`409`) | Another mapping of the same tier and issuer has this priority |

A save that succeeds can carry `warnings`: `unpinned_condition` (no pin on a private issuer), `write_without_pipeline_source` (a write mapping without a `pipeline_source` condition), `shared_ownership` (a writing grant with `ownership: none`), `repository_template_unconstrained` (a `repositories` template without a `require` filter), `unprotected_refs_allowed`, `cross_project_read_only`, `mutable_user_claim` (a condition on `user_login` or `user_email`, which GitLab users can change), `sub_pattern` (a glob or regular expression on `sub`) and `unguarded_template_claim` (a template on a claim that is neither required nor guarded by `present: true`).

<details>
<summary>Condition syntax</summary>

A condition is a tree of combinators and leaves. Combinators: `all` (every child holds), `any` (one child holds), `not` (one child); `all` and `any` need at least one child. A leaf names a top-level claim with `claim`, or a nested value with `path` (a list of member names), and has exactly one operator:

| Operator | Holds when |
|---|---|
| `equals` | The claim equals the literal |
| `in` | It equals one of the literals |
| `glob` | It matches the glob pattern |
| `regex` | It fully matches the regular expression (RE2, anchored by trove) |
| `present` | `true`: the claim exists and is not null; `false`: it is absent or null |

`ignoreCase: true` compares case-insensitively for `equals`, `in`, `glob` and `regex`. GitLab sends IDs and booleans as strings; trove compares `true` and `"true"`, `4242` and `"4242"` as equal. An array claim holds when any element matches. Every leaf except `present: false` is false on a missing claim, so `not` around a leaf on a missing claim is true: a condition that needs a claim says `present: true`.

Globs use `/` as the separator. `*` matches within one segment, `?` one character other than `/`, `**` whole segments only: `acme/**` matches `acme/app` and `acme/billing/app` but not `acme`; `acme/**/app` matches `acme/app`. `\` makes the next character literal. There are no character classes and no `{a,b}`; use `in` or `any`. Beware the prefix trap: `acme*/**` also matches `acme-evil/app`.

Conditions should use the separate claims (`project_id`, `ref_type`, `ref`, `ref_protected`, `pipeline_source`, `environment`, `environment_protected`, `deployment_tier`, `environment_action`), not `sub`, whose parts a project can reconfigure.

</details>

<details>
<summary>Templates in inline grants</summary>

The `repositories`, `packages` and `paths` patterns of an inline grant may contain templates, rendered once at the exchange from the token's claims: `{{ namespace_path | slug }}` gives `acme-billing` for the namespace `acme/billing`. The filters are `lower`, `slug`, `replace "a" "b"`, `trim_prefix "x"`, `trim_suffix "x"`, `segment n` (0-based `/` segment, negative from the end) and `require "regex"`, which fails the exchange when the value does not fully match.

A substituted value can never widen a pattern: in `packages` and `paths` its glob characters are escaped and it may not contain `/` (packages) or `..` (paths); in `repositories` it must match `^[a-z0-9._-]+$` and the template needs literal text next to it (`{{ project_path | segment 0 }}-releases`). A missing, null, array or object claim fails the exchange with `grant_render_failed`.

</details>

## Let a GitLab project's pipelines publish

The goal: the pipelines of the GitLab project `acme/invoice-api` publish the npm packages `@acme/*` to the local repository `web-npm` and the NuGet package `Acme.InvoiceApi` to `web-nuget`, both repositories of the trove project `web`, and read every repository of the project to install their dependencies. You are an administrator of `web`, and its switch **Manage policies and service accounts** is on. All names, IDs and addresses below are examples.

1. **Find the issuer.** `trove_curl https://trove.example.com/api/v1/projects/web/ci/issuers` lists the instances you can choose from. Here it is `gitlab-main`, with `publicIssuer: false`.
2. **Find the GitLab project ID.** GitLab shows the numeric ID on the project's page, and a job sees it as `CI_PROJECT_ID`. Here it is `4242`. Use the ID, never the path.
3. **Protect the refs that release.** In GitLab, protect the tags or branches whose pipelines publish, for example tags `v*`. Only their pipelines carry `ref_protected: "true"`.
4. **Create the mapping** with `POST /api/v1/projects/web/ci/mappings`:

   ```json
   {
     "issuer": "gitlab-main",
     "name": "web-invoice-api-publish",
     "description": "Protected refs of acme/invoice-api publish to web-npm and web-nuget",
     "priority": 100,
     "match": {"all": [
       {"claim": "project_id", "equals": "4242"},
       {"claim": "ref_protected", "equals": true},
       {"claim": "pipeline_source", "in": ["push", "web"]}
     ]},
     "grants": {"inline": [
       {"repositories": ["web-npm"], "packages": {"include": ["@acme/*"]},
        "actions": ["read", "deploy"], "ownership": "bind_project"},
       {"repositories": ["web-nuget"], "packages": {"include": ["Acme.InvoiceApi", "Acme.InvoiceApi.*"]},
        "actions": ["read", "deploy"], "ownership": "bind_project"},
       {"repositories": ["@any"], "packages": {"include": []}, "actions": ["read"]}
     ]},
     "maxTtlSeconds": 1800
   }
   ```

   ```shell
   trove_curl -X POST -H 'Content-Type: application/json' --data @mapping.json https://trove.example.com/api/v1/projects/web/ci/mappings
   ```

   The answer is `201` with the stored mapping, its resolved defaults and any `warnings`. The mapping is the principal `ci:gitlab-main/web-invoice-api-publish`.
5. **Test it** with the dry run ([below](#test-a-mapping-with-the-dry-run)): a claim set of a protected tag pipeline selects the mapping, the same claims with `"ref_protected": "false"` show the failing leaf.
6. **Hand over the pipeline.** Give the developers the issuer's audience (trove's base URL), the repository addresses and [GitLab CI pipelines](../user/gitlab-ci-pipelines.md), whose reference pipeline matches this mapping.

The third inline grant reads every repository of the project, the virtual repositories and their members included; an empty include list leaves package names unrestricted. It is part of this mapping because the first match decides: a protected pipeline of `acme/invoice-api` that matches this mapping does not also get a read mapping further down the tier. If the job installs from repositories outside the project, ask an administrator of trove for a grant there; a project mapping cannot name them.

### Why each condition is there

| Leaf | Why |
|---|---|
| `project_id` equals `"4242"` | The pin. The ID never changes; the path changes on rename and transfer, and another project can take a released path. On a public issuer a mapping without it cannot be saved (`pin_required`); on a private issuer a write through a policy cannot (`unpinned_write`) |
| `ref_protected` equals `true` | Required for every mapping that grants `deploy`, `overwrite` or `delete` (`unprotected_write`). On a protected branch or tag, only people allowed to push or merge there can run pipelines, and protected variables and runners are available only there. A condition on a branch name alone is met by anyone who can push a branch, and a merge request pipeline of a fork, run in your project, carries `ref_protected: "false"` |
| `pipeline_source` in `push`, `web` | Tag pushes and pushes to protected branches are `push`, pipelines started by hand are `web`; add `schedule` if scheduled pipelines release. Keep `merge_request_event`, `external_pull_request_event` and `trigger` out of a mapping that writes. Without this leaf the save warns `write_without_pipeline_source` |

To publish from tags only, add `{"claim": "ref_type", "equals": "tag"}`. `options.allowUnprotectedRefs: true` lifts the `ref_protected` requirement; it is visible in the API and the audit trail, and it lets every branch a developer pushes publish.

### What ownership bind_project means

`ownership: bind_project` is the default of every inline grant that holds `deploy`, `overwrite` or `delete`; the stored mapping always shows it. On a local repository that enforces package ownership, it binds each package name to the GitLab project that first publishes it:

- **First publish of a new name.** The publish claims the name for the owner `ci:gitlab-main:project:4242`, the issuer and the immutable project ID, in the same transaction as the upload. Of two first publishes of one name from two projects at the same moment, exactly one wins; the other gets `403`.
- **Later publishes.** The owning project publishes new versions. Every other GitLab project gets `403` `package_owned_by_other_owner`, even when its own grant pattern matches the name, so two mappings whose patterns overlap cannot take over each other's packages.
- **Rename and transfer in GitLab.** Ownership stays, because the project ID does not change. A new project that reuses the old path is refused.
- **A name with versions but no owner** (imported, published before ownership was enforced, or released) is refused with `package_owned_by_other_owner`: a pattern match never takes over an existing package. A holder of `manage` on the repository, which every administrator of the project is, adds the owner `ci:gitlab-main:project:4242` with `POST /api/v1/repositories/<key>/packages/<name>/owners` ([Managing repositories](repositories.md#npm-local-package-ownership)), and the next publish succeeds.

`ownership: none` on a writing grant makes the mapping itself the owner of the names it publishes, so every GitLab project the mapping matches can publish versions of them; the save warns `shared_ownership`. Use it only for a mapping that is meant to be shared by several projects.

## Give a GitLab instance or group read access

The goal: pipelines read the repositories of the project `web`, not only those of `acme/invoice-api`. A read-only mapping is never subject to `unprotected_write` or `unpinned_write`, so on a private instance it may be pinned or not; on a public instance it must be pinned.

```json
{
  "issuer": "gitlab-main",
  "name": "web-acme-read",
  "description": "Every pipeline of group acme and its subgroups reads the project's repositories",
  "priority": 900,
  "match": {"all": [
    {"claim": "project_path", "glob": "acme/**"}
  ]},
  "grants": {"inline": [
    {"repositories": ["@any"], "packages": {"include": []}, "actions": ["read"]}
  ]}
}
```

Create it with `POST /api/v1/projects/web/ci/mappings` as above. Its priority number is higher than the publish mapping's, so protected pipelines of `acme/invoice-api` still match the publish mapping first; every other pipeline of the group falls through to this one. Which condition fits depends on the instance.

### On a private instance

| Who reads | Condition | Notes |
|---|---|---|
| Every project of the instance, personal projects included | `{"claim": "project_id", "present": true}` | Every GitLab ID token carries `project_id` (a required claim), so the leaf holds for every job of the instance. The save warns `unpinned_condition` |
| A group and its subgroups, by path | `{"claim": "project_path", "glob": "acme/**"}` | `acme/**` matches `acme/app` and `acme/billing/app`. The save warns `unpinned_condition`. Never write a prefix glob such as `acme*/**`, which also matches a group `acme-evil` that anyone may create later |
| A group, by ID | `{"claim": "namespace_id", "in": ["4711", "4712", "4713"]}` | A pin; the list names the group and each subgroup that has projects ([Groups with subgroups](#groups-with-subgroups)) |
| Some projects, by ID | `{"claim": "project_id", "in": ["4242", "4243"]}` | A pin |

A path condition follows paths, not projects: a project created later under a path that a rename, transfer or deletion released, or under a new group whose name fits the glob, reads what the mapping grants. On an instance where ordinary users may create groups and projects, prefer the ID forms; the `unpinned_condition` warning is the only signal.

### On gitlab.com and other public instances

A read for the whole instance is refused: `{"claim": "project_id", "present": true}` holds no pin, so the save answers `422` `pin_required`. On a public instance anyone can create an account and a project and ask GitLab for an ID token with trove's audience; without a pin, every one of them would read your packages. Pin the group by ID or the projects by ID, as in the last two rows above. A path condition may be added next to the pin to narrow it further, never instead of it.

### Groups with subgroups

A GitLab ID token's `namespace_id` is the ID of the project's immediate namespace, and trove knows no claim that carries the ID of the top-level group or of the groups in between. A project in `acme/billing` therefore carries the ID of `acme/billing`, not that of `acme`. To pin a group with subgroups by ID, list the group's ID and the ID of every subgroup that holds projects in an `in` leaf (at most 256 values), or list the project IDs. A subgroup created later is not covered until its ID is added; keeping the list current is a manual task.

> **Open point, being checked in this phase:** whether a GitLab ID token carries an immutable ID of the top-level group. The specification assumes it does not, and GitLab's claims are being checked against a real GitLab instance. This section is updated with the result; until then, list the IDs as described.

### The whole project or one repository

| Grant | Reads |
|---|---|
| `"repositories": ["@any"]` | Every repository of the project, also those created or assigned later; a repository that leaves the project leaves the grant at once |
| `"repositories": ["@any:npm"]` | Every npm repository of the project |
| `"repositories": ["web-npm-all", "web-npm", "web-npmjs"]` | Exactly these repositories |

A client that installs through a virtual repository needs `read` on the virtual repository and on the members it reads from, so name the members next to it, or use `@any`. A system mapping, which holders of `ci:write` create through `/api/v1/ci/issuers/{issuer}/mappings`, can grant over repositories of any project and none; there, `@any` means every repository of the installation.

### Fork pipelines

A merge request pipeline from a fork that a member runs in your project uses the fork's CI configuration with your runners. trove recognises it when `project_id` and `job_project_id` (GitLab 18.4 and later) or `source_project_id` (GitLab 19.5 and later) differ, and by default a matched mapping then contributes nothing (`cross_project_source_denied`). `options.crossProjectSource: read_only` lets such a pipeline have the mapping's grants capped to read; the save warns `cross_project_read_only`. The fork's code controls its CI configuration and can send anything the token reads elsewhere, so leave the default unless the readable content is not confidential. On older GitLab versions trove cannot recognise these pipelines. They carry the fork's `project_id` and `ref_protected: "false"`, so mappings pinned to your project's IDs do not match them, and no write mapping does unless it sets `allowUnprotectedRefs`.

## Test a mapping with the dry run

The dry run evaluates mappings against a claim set and shows the decision. It takes claims, never a token, so nobody has to paste a live credential; it issues nothing and records nothing.

| Call | Evaluates | Needs |
|---|---|---|
| `POST /api/v1/projects/{key}/ci/issuers/{issuer}/evaluate` | The project's tier only | Administration of the project with **Manage policies and service accounts**, or `ci:read` |
| `POST /api/v1/ci/issuers/{issuer}/evaluate` | Every tier of the issuer | `ci:read` |

```json
{
  "claims": {
    "iss": "https://gitlab.example.com",
    "project_id": "4242", "project_path": "acme/invoice-api",
    "namespace_id": "4711", "namespace_path": "acme",
    "ref": "v1.4.0", "ref_type": "tag", "ref_protected": "true",
    "pipeline_source": "push", "pipeline_id": "377", "job_id": "981"
  },
  "scope": "deploy:web-npm"
}
```

```shell
trove_curl -X POST -H 'Content-Type: application/json' --data @claims.json https://trove.example.com/api/v1/projects/web/ci/issuers/gitlab-main/evaluate
```

Write the claims as GitLab sends them, IDs and booleans as strings. `scope` is optional and narrows as the exchange's parameter does. `now` (Unix seconds) is optional too: without it no time claim is checked, with it `exp`, `nbf` and `iat` are checked against that moment. The answer:

| Member | Shows |
|---|---|
| `crossProject` | Whether the claims classify as a fork pipeline |
| `trace` | Every mapping looked at, per tier: `matched`, `contributes` (false for a matched mapping that denies a fork pipeline), `failedLeaf` (a pointer into the condition, such as `/all/1`), `detail`, and `leaves` with each leaf's operator, expected and actual values and whether it passed |
| `selected` | The mappings the token would carry |
| `failure` | The reason the exchange would be refused with, such as `no_matching_mapping`; absent when it would succeed |
| `grants`, `scope`, `ttlSeconds` | The rendered grants, the scope and the lifetime the token would get |
| `warnings` | Warnings about the mappings involved |

Real claims come from an earlier exchange: the issuance record holds the claim snapshot of every token, and the audit event of a refused exchange holds it once the signature was verified. Copy the snapshot into `claims`, change a value, and watch which leaf fails.

## Read the issuance records

Every successful exchange writes an issuance record in the same transaction that creates the token. It holds only verified claims, never the token, its hash, `user_email` or `user_identities`.

| Call | Returns | Needs |
|---|---|---|
| `GET /api/v1/ci/issuances` | Records, newest first; filters `issuer`, `projectId`, `projectPath`, `pipelineId`, `jobId`, `mapping`, `active` (unexpired and unrevoked), `from`, `to`; pages with `limit` and `cursor` (`nextCursor`) | `tokens:read` |
| `GET /api/v1/ci/issuances/{tokenId}` | One record and its use: the number of requests, the last use and the packages it deployed | `tokens:read` |
| `GET /api/v1/projects/{key}/ci/issuances` | The records of tokens one of the project's mappings contributed to, naming the project's mappings only; filters `issuer`, `jobId`, `active` | Administration of the project with **Manage policies and service accounts**, or `tokens:read` |

```shell
trove_curl "https://trove.example.com/api/v1/ci/issuances?issuer=gitlab-main&jobId=981"
```

A record names `tokenId`, `issuer`, `mappingNames`, `projectId`, `projectPath`, `namespaceId`, `pipelineId`, `jobId`, `ref`, `refType`, `refProtected`, `pipelineSource`, `environment`, `sha`, `userId` and `userLogin` of the job, the allowlisted `claims`, the frozen `grants`, `requestedScope` and `grantedScope`, `crossProject`, `issuedAt`, `expiresAt`, `revokedAt`, `active`, `clientIp` and `userAgent`. The audit events of package operations carry the same `tokenId`, so a publish leads back to the job that made it. Records are kept for `ci.issuances.retention` (one year by default) after the token expired.

The exchange and its consequences are also audit events under **Administration › System › Audit log**:

| Event | Category | When |
|---|---|---|
| `ci.exchange.succeeded` | `auth` | A token was issued: token ID, issuer, mappings, claim snapshot, granted scope, lifetime |
| `ci.exchange.denied` | `auth` | An exchange was refused with `400` or `429`: the reason, and the claims once the signature was verified. Per replica, client address and reason, the first refusal of a minute is recorded at once and the rest of that minute as one event with a count |
| `ci.exchange.repeated`, `ci.exchange.replayed` | `auth` | One ID token was exchanged more than `ci.replay.warn_after_exchanges` times (10 by default), or a second time under single use |
| `ci.token.revoked` | `token` | CI tokens were revoked, with the cause: mapping, issuer, bulk, single, or the job itself |
| `ci.issuer.*`, `ci.mapping.*` | `identity` | Issuers and mappings were created, updated, disabled, enabled or deleted, with the record before and after |
| `ci.keys.refreshed`, `ci.keys.refresh_failed` | `auth` | A key fetch succeeded or failed |
| `ci.package_ownership.claimed`, `ci.package_ownership.conflict` | `authz` | A CI publish claimed a package name, or was refused because another owner holds it |

The metrics `trove_ci_exchanges_total{issuer,outcome,reason}`, `trove_ci_exchange_duration_seconds`, `trove_ci_tokens_active`, `trove_ci_signing_keys_age_seconds`, `trove_ci_signing_keys_refresh_total` and `trove_ci_mapping_set_stale_total` describe the federation; their labels carry no project or user names. See [Monitoring and operations](operations.md).

## Revoke CI tokens

| To revoke | Do | Who |
|---|---|---|
| The token of a running job, at its end | The job's last step calls `DELETE /api/v1/tokens/current` ([GitLab CI pipelines](../user/gitlab-ci-pipelines.md#revoke-the-token-as-the-last-step)) | The job |
| One token | `DELETE /api/v1/tokens/{tokenId}`, with the `tokenId` of the issuance record | `tokens:revoke` |
| Every token of a pipeline, job, project or mapping | `POST /api/v1/ci/issuances/revoke` with `issuer` and at least one of `projectId`, `pipelineId`, `jobId`, `mapping`; the answer is `{"revoked": <n>}` | `tokens:revoke` |
| Every token of a mapping, and stop it matching | Disable the mapping (`enabled: false`) or delete it | `ci:write`; the project's administrators for its mappings |
| Every token of an instance | Disable or delete the issuer; for an issuer of the Helm values, remove it from `config.ci.issuers` | `ci:write`; the Helm values |

```shell
trove_curl -X POST -H 'Content-Type: application/json' \
  --data '{"issuer": "gitlab-main", "pipelineId": "377"}' \
  https://trove.example.com/api/v1/ci/issuances/revoke
```

To disable a mapping of a project, read it with `trove_curl -i https://trove.example.com/api/v1/projects/web/ci/mappings/gitlab-main/web-acme-read`, set `"enabled": false` in the body, and send it back with `PUT` and `-H 'If-Match: "<the ETag>"'`. Disabling revokes the mapping's unexpired tokens and disables its principal in the same transaction, so a token that escaped the sweep is refused as well. Deleting a mapping revokes its tokens and deletes its principal: a mapping created later under the same name is a new principal and inherits no policy entry or ownership.

- **First match.** Disabling or deleting a mapping lets its jobs fall through to the next mapping of the tier, which may grant more. Disabling a read-only mapping for merge request pipelines placed in front of a publish mapping lets those pipelines publish. Check the tier with the dry run before you disable.
- **Editing does not revoke.** The inline grants of issued tokens stay as they were until the tokens expire; grants through policies follow a change at once. To cut a widened mapping off at once, edit it and revoke its tokens by `mapping`.
- **Propagation.** A revocation reaches every replica within `auth.revocation_bound`, 2 seconds by default, also while Valkey is unavailable ([Managing tokens](tokens.md#revocation-and-its-propagation)).
- **Jobs that fail.** A job that fails, is cancelled or times out before its last step keeps its token until the token expires: 15 minutes by default, never more than `ci.token.max_ttl` or the rest of the ID token's lifetime. trove learns nothing about the end of a job.

## Troubleshoot refused exchanges

A refused exchange answers in the OAuth error shape, not as problem details:

```json
{
  "error": "invalid_request",
  "error_description": "no identity mapping of CI issuer gitlab-main matched the token",
  "reason": "no_matching_mapping",
  "requestId": "4bf92f3577b34da6a3ce929d0e0e4736"
}
```

`reason` is the stable name to look for. `error_description` holds no claim value and no token material. `requestId` equals the `X-Request-Id` header and finds the `ci.exchange.denied` event, which holds the details. With `ci.exchange.expose_reason: false`, every `400` answers `invalid_request` without `reason`; the audit event and the log keep it.

To work through a refusal: find the event by request ID, time or project; for a refused match, copy its claim snapshot into the [dry run](#test-a-mapping-with-the-dry-run); read the reason in the table below.

<details>
<summary>Every reason of a refused exchange</summary>

| Reason | HTTP and `error` | Cause | Fix |
|---|---|---|---|
| `malformed_request` | 400 `invalid_request` | A parameter is missing, sent twice, or the body cannot be parsed | Send each parameter once, form-encoded as the pipeline guide shows |
| `unsupported_content_type` | 400 `invalid_request` | Neither `application/x-www-form-urlencoded` nor `application/json` | Use `--data-urlencode` |
| `unsupported_subject_token_type` | 400 `invalid_request` | `subject_token_type` is not the ID-token type (the job-token type is not available) | `urn:ietf:params:oauth:token-type:id_token` |
| `unsupported_requested_token_type` | 400 `invalid_request` | `requested_token_type` other than an access token | Leave it out |
| `actor_token_not_supported` | 400 `invalid_request` | An `actor_token` was sent | Leave it out |
| `subject_token_malformed` | 400 `invalid_request` | Not a signed JWT, larger than `ci.exchange.max_subject_token_bytes`, a `crit` header or duplicate members | Send the variable of the `id_tokens:` block |
| `wrong_subject_token_type` | 400 `invalid_request` | The value starts with `glcbt-`: the job sent `CI_JOB_TOKEN`. A job token in GitLab's newer JWT form fails later instead, with `unknown_issuer`, `unknown_signing_key`, `signature_invalid` or `audience_invalid` | Use `id_tokens:`; `CI_JOB_TOKEN` is never accepted |
| `unknown_issuer` | 400 `invalid_request` | No issuer has exactly this `iss` | Register the instance; compare its `issuer` character for character with the `iss` that the denial event shows |
| `issuer_disabled` | 400 `invalid_request` | The issuer is disabled, or was removed from the Helm values | Enable it, or declare it again |
| `issuer_mismatch` | 400 `invalid_request` | The `ci_issuer` parameter names another issuer than the token's `iss` | Correct or drop the parameter |
| `algorithm_not_allowed` | 400 `invalid_request` | The token's `alg` is not in `allowedAlgorithms` | Check what the instance signs with |
| `token_type_not_allowed` | 400 `invalid_request` | The `typ` header is not allowed | Check `allowedTyp` and `allowMissingTyp` |
| `unknown_signing_key` | 400 `invalid_request` | The key ID is unknown, also after the on-demand fetch; the instance rotated its key or the fetch fails | `GET …/keys` for the last error; `POST …/keys/refresh` |
| `signature_invalid` | 400 `invalid_request` | The signature does not verify with the instance's keys | A forged or altered token; if genuine jobs get it, refresh the keys and check `jwksUri` |
| `audience_invalid` | 400 `invalid_request` | No element of `aud` is an allowed audience | Set `aud` in `id_tokens:` to trove's base URL exactly, or add the audience to the issuer |
| `audience_not_exclusive` | 400 `invalid_request` | `aud` also names another service | One `id_tokens:` entry per service |
| `token_expired` | 400 `invalid_request` | `exp` passed, beyond the clock skew. GitLab sets it to the job's timeout, or to five minutes when the job has none | Exchange early in the job; give the job a timeout |
| `token_not_yet_valid`, `issued_in_future` | 400 `invalid_request` | `nbf` or `iat` lies in the future beyond the clock skew | Synchronise the clocks of GitLab and trove |
| `required_claim_missing` | 400 `invalid_request` | A claim of `requiredClaims` is missing or null; `error_description` names it. With single use, a token without `jti` | Check the GitLab version and the issuer's `requiredClaims` |
| `subject_token_replayed` | 400 `invalid_request` | Single use is on for the issuer (off by default for GitLab) and the ID token was exchanged before | Switch `singleUseSubjectTokens` off, or exchange once per job |
| `subject_token_predates_restore` | 400 `invalid_request` | Single use is on and the ID token was issued before a restore of trove finished | Run the job again |
| `cross_project_source_denied` | 400 `invalid_request` | A fork pipeline run in the parent project, and no matched mapping opts in | Intended; see [Fork pipelines](#fork-pipelines) |
| `no_matching_mapping` | 400 `invalid_request` | No mapping of any tier holds for the claims | Dry-run the claim snapshot of the denial event |
| `grant_render_failed` | 400 `invalid_request` | A template of the matched mapping refers to a missing claim, or a value fails its context or a `require` filter | Fix the template, or guard the claim with `present: true` |
| `subject_token_expires_too_soon` | 400 `invalid_request` | The token would live less than `ci.token.min_ttl` (30 s by default): the ID token is about to expire, or `ttl` asked for less | Exchange earlier; ask for a longer `ttl` |
| `job_token_rejected`, `job_not_running` | 400 `invalid_request` | Only with the job-token fallback, which this version does not have | — |
| `grant_type_unsupported` | 400 `unsupported_grant_type` | `grant_type` is not the token-exchange type | `urn:ietf:params:oauth:grant-type:token-exchange` |
| `scope_malformed` | 400 `invalid_scope` | An unknown action, `manage`, an invalid selector, or a key that names no repository | Correct `scope` |
| `scope_not_granted` | 400 `invalid_scope` | A `scope` item asks for an action the matched grants do not hold; no partial token is issued | Narrow `scope`, or extend the mapping |
| `audience_not_this_server` | 400 `invalid_target` | The `audience` or `resource` parameter is not trove's base URL | Leave them out, or send the base URL |
| `rate_limited` | 429 `slow_down` | A rate limit is spent; `Retry-After` says when to retry | See [Rate limits and runner networks](#rate-limits-and-runner-networks) |
| `signing_keys_unavailable` | 503 `temporarily_unavailable` | The stored key set is older than `ci.signing_keys.max_stale` (24 h): the instance has been unreachable, or no `worker` replica ran the refresh | Check the key status and the `worker` replicas |
| `database_unavailable` | 503 `temporarily_unavailable` | PostgreSQL is unavailable | Retry; check the database |
| `configuration_changed` | 503 `temporarily_unavailable` | The issuer or its mappings changed twice while the exchange ran | Retry |
| `gitlab_unreachable` | 503 `temporarily_unavailable` | Only with the job-token fallback | — |
| `internal` | 500 `server_error` | A defect | Report it with the request ID |

</details>

Until the federation is released, the exchange answers `501` `ci_not_implemented` as problem details, and so does every CI operation of this page. With `ci.exchange.enabled: false` the exchange answers `404`.

### Refusals after the exchange

| What the job sees | Cause |
|---|---|
| `403` on `npm publish` or `dotnet nuget push` | The token holds no `deploy` there: an unprotected ref matched a read-only mapping, the `scope` left it out, or the package name is outside the grant's patterns |
| `403` `package_owned_by_other_owner` | Another GitLab project or principal owns the name, or the name has versions and no owner ([What ownership bind_project means](#what-ownership-bind_project-means)) |
| `401` `invalid_token` | The token expired or was revoked; a step after the closing revocation still used it |
| `401` `principal_disabled` | The mapping that issued the token is disabled |
| `403` `ci_token_restricted` | The job called a management operation; a CI token is accepted only on the package protocols, `/files/…`, the repository-content operations of the API, and `GET` and `DELETE /api/v1/tokens/current` |

### Problems with an issuer

- **The key status.** `GET /api/v1/ci/issuers/{name}/keys` shows the last fetch, the next refresh and `lastError`, with the failure class, the HTTP status and the host, never a byte of a response. `trove_ci_signing_keys_age_seconds` climbing towards `ci.signing_keys.max_stale` means fetches fail: exchanges keep working on the stored keys until then and answer `503` `signing_keys_unavailable` afterwards.
- **A create refused with `422`.** `discovery_unreachable` (DNS, TLS, the CA bundle, the address rules), `issuer_mismatch` (the instance announces another issuer URL than yours, often a different host or a trailing slash), `no_usable_keys` (no key meets the rules: signing use, a matching key type, RSA keys of at least 2048 bits), `algorithms_not_offered`.
- **The migration stops.** An entry of `config.ci.issuers` collides with an issuer created through the API, by name or URL. Delete the API issuer, or rename the entry.

## Rate limits and runner networks

The exchange needs no credential, so two limiters bound what a caller can cause. Both count across all replicas:

```yaml
config:
  ci:
    exchange:
      rate_limit:
        per_client_ip_per_minute: 600   # default; per client address
        per_client_ip_burst: 100        # default
        per_subject_per_minute: 300     # default; per issuer and GitLab project
        per_subject_burst: 100          # default
        exempt_cidrs: []                # default; example: ["10.20.0.0/16"], a runner network behind NAT
```

- The **per-address** limit is checked first, before the request body is read. The address is the client address after the trusted-proxy handling, so `server.trusted_proxies` must name your ingress; otherwise every exchange counts against the ingress's address.
- Runners that reach trove through a few NAT addresses carry the jobs of the whole fleet on one address. List their networks in `exempt_cidrs`: those addresses skip the per-address limit and stay bounded by `server.max_in_flight` and the per-project limit.
- The **per-project** limit counts after the token's signature has been verified, per issuer and the token's `project_id`. Forged tokens never reach it, and on a public instance an outsider's project spends only its own budget.
- Exceeding either answers `429` `slow_down` with `Retry-After`. Neither records the ID token as used, so the retry of the pipeline guide is accepted.
- While Valkey is unavailable, each replica enforces its share of each budget; no exchange fails because Valkey failed.

## What stays with the system administrators

Project administrators manage the mappings of their project. These stay with holders of the system capabilities and with your Helm values:

- the issuers: registering, changing, disabling and deleting instances, their audiences, CA bundles and key refreshes;
- mappings of the `system` tier, mappings that grant through groups, and grants on repositories outside a project;
- the snippet operation `GET /api/v1/ci/issuers/{issuer}/snippets/gitlab-ci?mapping=<name>&format=npm` (or `nuget`), which needs `ci:read`;
- the issuance records of every mapping, revoking one token, and the bulk revocation;
- the `ci.*` configuration keys and the package ownership of repositories outside a project.

## Configuration keys

<details>
<summary>Every key of the ci section</summary>

| Key | Default | Meaning |
|---|---|---|
| `ci.issuers` | `[]` | The issuers of the Helm values ([above](#in-the-helm-values)) |
| `ci.token.default_ttl` | `15m` | Lifetime of a CI token when the job asks for none |
| `ci.token.max_ttl` | `1h` | Upper bound of every CI token; at most `auth.tokens.cit.max_ttl` (`6h`), or the start fails naming both keys |
| `ci.token.min_ttl` | `30s` | A token that would live shorter is not issued (`subject_token_expires_too_soon`) |
| `ci.exchange.enabled` | `true` | `false` answers every exchange with `404` |
| `ci.exchange.expose_reason` | `true` | `false` leaves `reason` out of the `400` answers; logs and audit keep it |
| `ci.exchange.max_body_bytes` | `65536` | Largest exchange request |
| `ci.exchange.max_subject_token_bytes` | `16384` | Largest ID token |
| `ci.exchange.rate_limit.*` | see [above](#rate-limits-and-runner-networks) | The two limiters and `exempt_cidrs` |
| `ci.signing_keys.default_refresh_interval` | `1h` | Next refresh when the key response has no `Cache-Control: max-age` |
| `ci.signing_keys.min_refresh_interval`, `.max_refresh_interval` | `5m`, `24h` | Clamp of the refresh interval; the minimum is also the retry after a failed fetch |
| `ci.signing_keys.unknown_kid_refresh_interval` | `60s` | At most one on-demand fetch per issuer and replica for an unknown key ID |
| `ci.signing_keys.max_stale` | `24h` | A key set older than this fails closed with `503` |
| `ci.signing_keys.fetch_timeout`, `.max_response_bytes` | `5s`, `1048576` | One discovery or key request |
| `ci.signing_keys.cache_safety_ttl`, `ci.mappings.cache_safety_ttl` | `10m` | The safety lifetime of each replica's in-memory copy of the keys and of the mappings |
| `ci.mappings.max_per_issuer` | `1000` | Mappings of one issuer, all tiers together |
| `ci.replay.retention_after_expiry` | `15m` | How long a used ID token is remembered after it expired |
| `ci.replay.warn_after_exchanges` | `10` | From this count on, every further exchange of one ID token records `ci.exchange.repeated` |
| `ci.issuances.retention` | `8760h` | How long an issuance record is kept after its token expired |
| `ci.job_token_fallback.enabled` | `false` | The `CI_JOB_TOKEN` fallback is not built; `true` stops the start |
| `maintenance.tasks.ci-keys-refresh` | `*/5 * * * *` | The key refresh on the `worker` replicas |
| `maintenance.tasks.ci-replay-cleanup` | `7 * * * *` | Deletes expired entries of used ID tokens |
| `maintenance.tasks.ci-issuance-retention` | `40 3 * * *` | Deletes issuance records past their retention |

</details>

[Configuration](configuration.md) explains how keys, environment variables and Helm values relate.

## Limits of this version

- The federation is not released yet; see the note at the top of this page.
- GitLab is the only kind of issuer. `CI_JOB_TOKEN` is never accepted; its fallback is not built.
- Mappings are managed through the API only; there are no configuration documents for them, and mapping management cannot be delegated to a group outside a project.
- A group with subgroups is pinned by listing IDs ([Groups with subgroups](#groups-with-subgroups)).
- The ID token and the CI token are bearer credentials: whoever reads them while they are valid can use them, for example on a runner shared between jobs. A GitLab ID token can be exchanged again until it expires; the exchange count in the audit trail shows it.
- trove trusts every token the instance signs. A GitLab administrator, or anyone who holds the instance's signing key or takes over its domain, can mint ID tokens for any project and ref; mappings cannot detect this. Ownership binding, the short lifetime and the issuance records bound and record the damage.
