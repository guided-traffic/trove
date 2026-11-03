# GitLab CI pipelines

How a GitLab CI job gets a short-lived trove token without a stored secret: the id_tokens block, the token exchange with curl, narrowing the token, configuring npm and NuGet with it, revoking it as the last step of the job, what never to do with it, and what a refused exchange means.

> **Not available in this version yet.** GitLab CI federation is being built. Until it is released, the token exchange answers `501` with the code `ci_not_implemented`, and so does every CI operation of the management API. This page describes the behaviour the feature is built to. Until then, a pipeline uses a service-account token stored as a masked, protected CI/CD variable ([Personal and service-account tokens](access-tokens.md#personal-and-service-account-tokens)).

## How it works

A job asks GitLab for an ID token addressed to trove, posts it to trove, and gets a `trv_cit_` token back that lives for minutes. trove decides what the token may do from the verified facts about the job: which GitLab project it belongs to, whether it runs on a protected branch or tag, how the pipeline was started. Nothing secret is stored in GitLab, and the token dies with the job. The rules that turn those facts into permissions are the identity mappings, which an administrator of your trove project or of trove writes ([GitLab CI federation](../admin/gitlab-ci.md)).

## Before you start

- **A mapping for your pipelines.** Ask the administrator of your trove project for one. Tell them the numeric ID of your GitLab project (GitLab shows it on the project's page; a job sees it as `CI_PROJECT_ID`), which branches or tags publish, and which repositories the pipeline installs from and publishes to. Publishing needs a protected branch or tag in GitLab, unless the mapping explicitly allows unprotected refs.
- **Three values from them.** trove's base URL, which is the audience of your ID token (for example `https://trove.example.com`); the addresses of the repositories; and which of them you may publish to.
- **curl and jq in the job's image.** curl 7.55 or later reads a header from standard input (`-H @-`), and from 7.66 on `--retry` waits as the answer's `Retry-After` says.

## Request the ID token

Declare an ID token with `id_tokens:` in the job, or in a hidden job that the jobs extend:

```yaml
.trove:
  id_tokens:
    TROVE_ID_TOKEN:                        # example: any variable name works
      aud: https://trove.example.com       # example: trove's base URL
```

- `aud` is trove's base URL exactly as the administrator gave it, a single string. A list that also names another service is refused (`audience_not_exclusive`): that service could use the same token at trove. Declare one entry per service instead.
- `id_tokens` may stand under `default:`, but a job that declares its own `id_tokens` replaces the default entirely; GitLab does not merge them.
- GitLab sets the ID token's expiry to the job's timeout, or to five minutes when the job has none. The trove token never lives longer.
- `CI_JOB_JWT` and `CI_JOB_JWT_V2` no longer exist in GitLab, and `CI_JOB_TOKEN` is not an ID token; trove accepts neither.

## Exchange the ID token

The exchange is one form-encoded `POST` to `/api/v1/auth/oidc/token`. This `before_script` of the hidden job does it, keeps both tokens out of the log and the process list, and exports the trove token as `TROVE_TOKEN`:

```yaml
variables:
  TROVE_URL: "https://trove.example.com"           # example

# Hidden job: requests the ID token and exchanges it in before_script.
.trove:
  id_tokens:
    TROVE_ID_TOKEN:
      aud: https://trove.example.com                # example: must be an allowed audience of the CI issuer
  before_script:
    - |
      if ! command -v jq >/dev/null || ! command -v curl >/dev/null; then
        apt-get update -qq && apt-get install -y -qq --no-install-recommends curl jq ca-certificates >/dev/null
      fi
    - |
      response="$(printf '%s' "$TROVE_ID_TOKEN" | curl -sS --retry 3 \
        --data-urlencode 'grant_type=urn:ietf:params:oauth:grant-type:token-exchange' \
        --data-urlencode 'subject_token_type=urn:ietf:params:oauth:token-type:id_token' \
        --data-urlencode 'subject_token@-' \
        "$TROVE_URL/api/v1/auth/oidc/token")"
      TROVE_TOKEN="$(printf '%s' "$response" | jq -er '.access_token // empty')" \
        || { printf '%s\n' "$response" | jq '{error, error_description, reason, requestId}' >&2; exit 1; }
      export TROVE_TOKEN
      unset response TROVE_ID_TOKEN
```

- `printf '%s' "$TROVE_ID_TOKEN" | curl --data-urlencode 'subject_token@-'` hands curl the ID token on standard input. `printf` is a shell builtin, so the token never appears in the runner's process list.
- `--retry 3` repeats the exchange after a `429` or a `503`, waiting as `Retry-After` says. Neither answer records the ID token as used, so the repeat is accepted. curl reads the token into memory once and sends it again.
- On success, `jq` takes `access_token`; on a refusal it prints `error`, `error_description`, `reason` and `requestId` and stops the job. None of them ever contains a token or a claim value.
- `unset` removes the ID token from the shell once it is exchanged. Keep it if a later step of the job exchanges again ([Several exchanges in one job](#several-exchanges-in-one-job)).
- The `apt-get` line installs the tools on Debian-based images that lack them; leave it out where the image has them.

The answer of a successful exchange (values are examples):

```json
{
  "access_token": "trv_cit_…",
  "issued_token_type": "urn:ietf:params:oauth:token-type:access_token",
  "token_type": "Bearer",
  "expires_in": 900,
  "scope": "read:web-npm deploy:web-npm read:web-nuget deploy:web-nuget read:@any",
  "ci_issuer": "gitlab-main",
  "mappings": ["web-invoice-api-publish"]
}
```

`scope` lists what the token may do, one item per action and repository selector: first the grants of the permission policies the mappings belong to, with their selectors as the policies write them, then the mappings' own grants. `mappings` names the mappings that granted it. The token value is in this answer only, and there is no refresh token: a job that needs a new token exchanges again. The answer carries `Cache-Control: no-store`, so no proxy keeps it.

### How long the token lives

`expires_in` is the smallest of: the `ttl` you asked for, or 15 minutes; the maximum of the matched mappings and of the issuer, where set; one hour; and what is left of the ID token. Your administrator may have set other values. An exchange whose token would live less than 30 seconds is refused: with `subject_token_expires_too_soon` when the ID token has less than that left, with `ttl_below_minimum` when your `ttl` asks for less.

## Narrow the token

Without further parameters the token carries every grant of the matched mappings. Add `scope` to the exchange to ask for less, so that a step that only installs cannot publish:

```shell
--data-urlencode 'scope=read:@any:npm deploy:web-npm'      # example
```

- `scope` is a space-separated list of items `<action>` or `<action>:<repository selector>`. The actions are `read`, `annotate`, `deploy`, `overwrite` and `delete`; the selectors are those of permission policies: a key (`web-npm`), a key pattern (`web-*`), a class (`@any`, `@any-local`) or a class or pattern with a format (`@any:npm`). An item without a selector stands for every repository.
- An item that asks for an action the mappings do not grant refuses the whole exchange with `scope_not_granted`; there is no partial token. A key that names no repository answers the same, so check the spelling of a key when you see it. `manage` or an unknown action answers `scope_malformed`. At most 50 distinct items.
- `--data-urlencode 'ttl=600'` asks for a shorter lifetime in whole seconds. It can only shorten, and not below 30 seconds (`ttl_below_minimum`).
- Package-name and path limits are not part of `scope`; the mapping's grants carry them.

## Use the token with npm

npm expands `${TROVE_TOKEN}` in `.npmrc` when it runs, so the file never holds the value. Write it with a quoted heredoc delimiter (`<<'EOF'`), so the shell does not expand the variable either:

```yaml
publish-npm:
  stage: publish
  image: node:24-bookworm                               # example
  extends: .trove
  rules:
    - if: $CI_COMMIT_TAG && $CI_COMMIT_REF_PROTECTED == "true"
  script:
    # npm expands ${TROVE_TOKEN} at run time; the token is never written to disk.
    - |
      cat > .npmrc <<'EOF'
      registry=https://trove.example.com/api/npm/web-npm-all/
      //trove.example.com/api/npm/web-npm-all/:_authToken=${TROVE_TOKEN}
      //trove.example.com/api/npm/web-npm/:_authToken=${TROVE_TOKEN}
      EOF
    - npm ci
    - npm version --no-git-tag-version --allow-same-version "${CI_COMMIT_TAG#v}"
    - npm publish --registry https://trove.example.com/api/npm/web-npm/
    # Last step: revoke the CI token, so it does not outlive the job.
    - |
      printf 'Authorization: Bearer %s\n' "$TROVE_TOKEN" | curl -sS -X DELETE -H @- "$TROVE_URL/api/v1/tokens/current"
```

The credential lines name trove's host, so npm never sends the token to another registry. `npm version --allow-same-version` keeps the job working when `package.json` already carries the tag's version. The `rules:` run the job only for protected tags; a job on an unprotected branch would get a token without `deploy`, and `npm publish` would fail with `E403`. [Connecting a package client](client-setup.md) explains the npm configuration in general.

## Use the token with NuGet

A `nuget.config` in the repository root reads the token from the environment as `%TROVE_TOKEN%`. NuGet expands it on every operating system, and developers can use the same file with their own personal token in `TROVE_TOKEN`:

```xml
<?xml version="1.0" encoding="utf-8"?>
<configuration>
  <packageSources>
    <clear />
    <add key="trove" value="https://trove.example.com/api/nuget/web-nuget-all/v3/index.json" />
  </packageSources>
  <packageSourceCredentials>
    <trove>
      <add key="Username" value="ci" />
      <add key="ClearTextPassword" value="%TROVE_TOKEN%" />
    </trove>
  </packageSourceCredentials>
</configuration>
```

```yaml
push-nuget:
  stage: publish
  image: mcr.microsoft.com/dotnet/sdk:10.0              # example
  extends: .trove
  rules:
    - if: $CI_COMMIT_TAG && $CI_COMMIT_REF_PROTECTED == "true"
  script:
    # nuget.config reads %TROVE_TOKEN% for restores from web-nuget-all.
    - dotnet restore
    - >-
      dotnet pack src/Acme.InvoiceApi/Acme.InvoiceApi.csproj
      -c Release -o out --no-restore -p:PackageVersion="${CI_COMMIT_TAG#v}"
    - >-
      dotnet nuget push "out/*.nupkg"
      --source "$TROVE_URL/api/nuget/web-nuget/v3/index.json"
      --api-key "$TROVE_TOKEN"
    - |
      printf 'Authorization: Bearer %s\n' "$TROVE_TOKEN" | curl -sS -X DELETE -H @- "$TROVE_URL/api/v1/tokens/current"
```

- The user name in `nuget.config` is not checked. When `TROVE_TOKEN` is not set, NuGet keeps the literal text `%TROVE_TOKEN%`, so a missing token shows up as `401`, not as anonymous access.
- `dotnet nuget push --api-key` sends the token as `X-NuGet-ApiKey`. The token stands in `dotnet`'s argument list while the push runs, visible to other processes on the same runner.
- `<clear />` keeps every other source out, so packages resolve through trove only.

## Revoke the token as the last step

```shell
printf 'Authorization: Bearer %s\n' "$TROVE_TOKEN" | curl -sS -X DELETE -H @- "$TROVE_URL/api/v1/tokens/current"
```

In a `.gitlab-ci.yml`, write the line as a block scalar (`- |`, as the pipelines above do): a plain list item with a colon and a space in it (`Authorization: Bearer`) is a YAML mapping, and GitLab refuses the job.

`DELETE /api/v1/tokens/current` revokes the token that authenticates it and answers `204`; the token is refused on every replica within seconds. The header comes from standard input (`-H @-`) for the same reason as the exchange.

Make it the last line of `script`, not an `after_script`. `after_script` runs in a separate shell that does not see the variables exported in `before_script` and `script`, so it never holds the token, and it does not run for a job that times out. A job that fails or is cancelled before the last line keeps its token until `expires_in` has passed, 15 minutes at most unless your administrator set other values.

`GET /api/v1/tokens/current` with the same header shows the token's expiry and scope, if a step wants to check them.

## A complete pipeline

The pieces above together, for the GitLab project `acme/invoice-api`, whose npm package is `@acme/invoice-client` and whose NuGet package is `Acme.InvoiceApi` (all names and addresses are examples):

<details>
<summary>The whole .gitlab-ci.yml</summary>

```yaml
stages: [publish]

variables:
  TROVE_URL: "https://trove.example.com"

.trove:
  id_tokens:
    TROVE_ID_TOKEN:
      aud: https://trove.example.com
  before_script:
    - |
      if ! command -v jq >/dev/null || ! command -v curl >/dev/null; then
        apt-get update -qq && apt-get install -y -qq --no-install-recommends curl jq ca-certificates >/dev/null
      fi
    - |
      response="$(printf '%s' "$TROVE_ID_TOKEN" | curl -sS --retry 3 \
        --data-urlencode 'grant_type=urn:ietf:params:oauth:grant-type:token-exchange' \
        --data-urlencode 'subject_token_type=urn:ietf:params:oauth:token-type:id_token' \
        --data-urlencode 'subject_token@-' \
        "$TROVE_URL/api/v1/auth/oidc/token")"
      TROVE_TOKEN="$(printf '%s' "$response" | jq -er '.access_token // empty')" \
        || { printf '%s\n' "$response" | jq '{error, error_description, reason, requestId}' >&2; exit 1; }
      export TROVE_TOKEN
      unset response TROVE_ID_TOKEN

publish-npm:
  stage: publish
  image: node:24-bookworm
  extends: .trove
  rules:
    - if: $CI_COMMIT_TAG && $CI_COMMIT_REF_PROTECTED == "true"
  script:
    - |
      cat > .npmrc <<'EOF'
      registry=https://trove.example.com/api/npm/web-npm-all/
      //trove.example.com/api/npm/web-npm-all/:_authToken=${TROVE_TOKEN}
      //trove.example.com/api/npm/web-npm/:_authToken=${TROVE_TOKEN}
      EOF
    - npm ci
    - npm version --no-git-tag-version --allow-same-version "${CI_COMMIT_TAG#v}"
    - npm publish --registry https://trove.example.com/api/npm/web-npm/
    - |
      printf 'Authorization: Bearer %s\n' "$TROVE_TOKEN" | curl -sS -X DELETE -H @- "$TROVE_URL/api/v1/tokens/current"

push-nuget:
  stage: publish
  image: mcr.microsoft.com/dotnet/sdk:10.0
  extends: .trove
  rules:
    - if: $CI_COMMIT_TAG && $CI_COMMIT_REF_PROTECTED == "true"
  script:
    - dotnet restore
    - >-
      dotnet pack src/Acme.InvoiceApi/Acme.InvoiceApi.csproj
      -c Release -o out --no-restore -p:PackageVersion="${CI_COMMIT_TAG#v}"
    - >-
      dotnet nuget push "out/*.nupkg"
      --source "$TROVE_URL/api/nuget/web-nuget/v3/index.json"
      --api-key "$TROVE_TOKEN"
    - |
      printf 'Authorization: Bearer %s\n' "$TROVE_TOKEN" | curl -sS -X DELETE -H @- "$TROVE_URL/api/v1/tokens/current"
```

</details>

The first publish of `@acme/invoice-client` and `Acme.InvoiceApi` binds each name to your GitLab project; from then on no other GitLab project can publish versions of them, also after your project is renamed or moved. An administrator of trove can also generate a ready-to-paste snippet of this kind for a mapping.

## Several exchanges in one job

GitLab gives a job one ID token per `id_tokens` entry for its whole run, and documents no way for the job to ask for another. A job that outlives its trove token, or runs steps that need different scopes, exchanges the same ID token again; keep `TROVE_ID_TOKEN` for that instead of unsetting it. Each exchange is counted, and the issuance records show it. If your administrator switched on single use for the GitLab instance, a second exchange is refused with `subject_token_replayed`.

## What the token can do

The token works like any token on the package protocols (npm, NuGet), on `/files/…`, and on the operations of the API that read or change repository content: as a bearer token, as the password of HTTP Basic authentication with any user name (unless an administrator switched Basic authentication off), and as the NuGet API key. It acts as the mappings that granted it, never as a person: it holds no system role, cannot create tokens and gets `403` `ci_token_restricted` on every management operation except `GET` and `DELETE /api/v1/tokens/current`.

## What never to do

- **Never print a token.** No `echo "$TROVE_TOKEN"`, no `set -x` around the exchange or the steps that use it. The trove token is created during the job, so GitLab does not mask it in the job log, and anyone who can read the log could use it until it expires.
- **Never put a token on a command line** you can avoid: `curl -H "Authorization: Bearer $TROVE_TOKEN"` shows the value in the runner's process list. Pipe it in as the snippets do.
- **Never write the value into a file** that ends up in an artifact or a cache. Keep `${TROVE_TOKEN}` and `%TROVE_TOKEN%` as references, and quote the heredoc delimiter that writes `.npmrc`.
- **Never send `CI_JOB_TOKEN`** to trove or to any other service. It is a GitLab credential that acts with your access level on GitLab, and trove refuses it: a value starting with `glcbt-` with `wrong_subject_token_type`, a job token in GitLab's newer JWT form with `unknown_issuer`, `unknown_signing_key`, `signature_invalid` or `audience_invalid`.
- **Never address one ID token to several services.** One `id_tokens:` entry per service.
- **Never rely on `after_script` to revoke.** It does not see the token.
- **Nothing to store.** No CI/CD variable needs to hold a trove token once the exchange works; remove the service-account token the pipeline used before.

## When the exchange is refused

The job log shows the members `error`, `error_description` and `reason`. The `reason` is the part to look up:

| `reason` | What it means for your pipeline | What to do |
|---|---|---|
| `audience_invalid` | The ID token's `aud` is not one trove accepts | Set `aud` to trove's base URL exactly |
| `audience_not_exclusive` | `aud` also names another service | Declare a separate `id_tokens:` entry for each service |
| `wrong_subject_token_type`, `subject_token_malformed` | The job sent something other than its ID token, such as `CI_JOB_TOKEN`, or a misspelt variable | Send the variable of the `id_tokens:` block |
| `malformed_request` | A parameter is missing, empty or sent twice; an empty `subject_token` means the variable of the `id_tokens:` block is not set in this job (a job that declares its own `id_tokens` replaces the default's) | Declare `id_tokens:` in the job, or in `default:` without overriding it |
| `unknown_issuer`, `issuer_disabled` | trove does not trust your GitLab instance, or not any more | Ask an administrator of trove |
| `no_matching_mapping` | No mapping covers this pipeline: another project, an unprotected branch, a merge request pipeline, a pipeline source the mapping leaves out | Run it on the refs the mapping names, or ask your project administrator |
| `cross_project_source_denied` | A merge request pipeline from a fork, run in your project | Intended: fork code gets no token by default |
| `token_expired`, `subject_token_expires_too_soon` | The ID token has expired, or is about to | Exchange at the start of the job; give the job a timeout |
| `ttl_below_minimum` | The `ttl` you asked for is below 30 seconds, or a bound your administrator set is | Ask for a longer `ttl`, or ask your administrator |
| `subject_token_replayed` | The ID token was exchanged before, and single use is on | Exchange once per job |
| `scope_malformed`, `scope_not_granted` | `scope` is invalid, or asks for more than the mappings grant | Correct `scope`, or ask for the grant |
| `rate_limited` (`429`) | Too many exchanges at once | `--retry` waits and repeats; if it persists, tell your administrator |
| `signing_keys_unavailable`, `database_unavailable`, `configuration_changed` (`503`) | A temporary condition at trove | `--retry` repeats; if it persists, tell your administrator |

With the exchange switched off by the administrator (`ci.exchange.enabled: false`) the endpoint answers `404` as problem details, which carry none of these members, so the error line of the snippet prints only `null` values.

For anything you cannot fix yourself, give your administrator the time, the GitLab project and job, and the `reason`. The answer also carries a `requestId`, which leads straight to the refusal in trove's audit trail; the error line of the snippet prints it, so give it to your administrator as well. The administrator's table of every reason is in [GitLab CI federation](../admin/gitlab-ci.md#troubleshoot-refused-exchanges).

## When a later request is refused

| What you see | What it means |
|---|---|
| `E403` from `npm publish`, `403` from `dotnet nuget push` | The token may not publish there: the pipeline ran on an unprotected ref and matched a read-only mapping, `scope` left `deploy` out, or the package name is outside the names the mapping allows |
| `E404` from `npm install`, `404` from a NuGet restore | The token may not read the package or the repository, for example because the mapping no longer is in the policy that gave it the read; trove answers a refused read like a missing package unless the repository shows existence |
| `403` `package_owned_by_other_owner` | Another GitLab project, or someone else, owns the package name. A name that already has versions is not taken over by a pipeline; an administrator of the repository adds your GitLab project as an owner |
| `E401`, `401` `invalid_token` | The token has expired or was revoked, for example a step after the closing revocation still used it |
| `403` `ci_token_restricted` | The step called a management operation, which a CI token never may |

[Errors and troubleshooting](troubleshooting.md) explains the error answers of the package clients.
