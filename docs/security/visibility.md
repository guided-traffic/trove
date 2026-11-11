# Visibility levels: who reads what

Status: specified in [authorization.md](../spec/authorization.md) R-AUTHZ-003, R-AUTHZ-030 and R-AUTHZ-033 to R-AUTHZ-036 and
[identity-and-authentication.md](../spec/identity-and-authentication.md) R-AUTH-037 and R-AUTH-044 (task T-18), and built in
steps. Built: the schema with the rules of the container in the database ([00073_visibility.sql](../../internal/meta/postgres/migrations/00073_visibility.sql),
[00074_external_users.sql](../../internal/meta/postgres/migrations/00074_external_users.sql),
[00075_principal_builtins.sql](../../internal/meta/postgres/migrations/00075_principal_builtins.sql)), the contracts
([visibility.go](../../internal/authz/visibility.go): `Level`, `Part`, `Subject.Internal`), the levels in the grant index, and the removal of
the anonymous switch, the principal `authenticated` and `requireAuthentication`. Until the decision point reads the levels, a request
without a credential is refused with 401 everywhere and no level opens anything; the table below describes the mechanism as specified. The
change that builds each remaining part names its code here.

This page answers one question: besides the members of a project and the principals a grant names, who reads a repository's
artifacts and its wiki, who decides that, and what keeps the answer from being wider than intended. The decision point, the grant
index and the denial semantics are in [permissions.md](permissions.md); projects and their roles in
[delegated-administration.md](delegated-administration.md); the internet-facing door in [bastion.md](bastion.md); further endpoints
and their exposure rules in [endpoints-and-exposure.md](endpoints-and-exposure.md).

## Who reads what

A level is `private`, `internal` or `public`. A project has one; a repository has one for its artifacts and one for its wiki
(R-AUTHZ-033). A level only adds `read`; grants, project roles and capabilities add what they add, and nothing takes away what
another source gives (R-AUTHZ-035).

| Caller | `private` | `internal` | `public` |
|---|---|---|---|
| No credential (the anonymous subject), on an endpoint whose `auth.anonymous` is `true` | — | — | reads |
| No credential, on an endpoint whose `auth.anonymous` is `false` | — | — | — (401) |
| User through a UI session or a personal token, not flagged external | — | reads, within the token's scope | reads |
| User flagged external (R-AUTH-044) | — | — | reads |
| Any user through a Bastion (Q-BAS-148) | — | — | reads |
| Service-account token, a project's included | — | — | reads |
| CI token | — | — | reads |
| Bastion registration | — | — | reads (no route admits it) |

"—" means: only what a grant, a project role or a capability gives. Every row is bounded by the exposure of the endpoint the
request arrived on, which binds a read by a level as it binds a read by a grant (R-EP-013). A `public` read is not narrowed by the
token scope or by the caps of a CI token, because it needs no credential at all; an invalid credential is still refused with 401 and
never read as anonymous (R-AUTH-001).

## Who decides

| Decision | Who | Bound |
|---|---|---|
| Whether requests without a credential are admitted at all | The operator: an endpoint's `auth.anonymous` in the configuration file | The default endpoint's value is the ceiling of `public`: while it is `false` no level can be set to `public` (409 `public_not_allowed`, Q-AUTH-232) |
| The level of a project | An administrator (`projects:write`), with recent authentication | At most `public`, subject to the line above |
| The levels of a repository | Its project's Owner or `repositories:write-config`, with recent authentication to raise | Artifacts at most the project's level, wiki at most the artifacts' (422; the database refuses the state with `AI008` and a check constraint) |
| The installation's default | `security-settings:write` | Decides only objects created later |
| Whether a user is external | `identities:write`, with recent authentication; `newUsersExternal` for users created later | No identity-provider claim, group sync or SCIM attribute writes it |

No policy entry, role binding, project membership or owner record can name `anonymous` or `authenticated` (422
`principal_not_allowed`, refused by the database with `AI009`, Q-AUTHZ-279), so no grant can open what the levels keep closed.

## The wiki

The wiki of a repository is the documentation trove serves or shows apart from the files: npm's `readme` members, the NuGet README
route, the README operation of the package API and the collection documentation of the Ansible format (R-AUTHZ-034). It is never
more open than the artifacts, so a caller who reads the wiki reads the package. A caller who reads the artifacts and not the wiki
gets the refusal of a read on a route that serves only wiki content and documents without the wiki members; the README tab is not
shown. One coordinate part decides it, asked through four named forms that the format kit and a document test hold every format and
operation to (R-AUTHZ-034, R-API-058, R-ARCH-010 check 15).

## How a change reaches every replica

| Change | Path | Bound |
|---|---|---|
| A project's or a repository's level | The compiled grant index, through the change areas `authz` and `repo_config` (R-AUTHZ-028) | Typically 100 ms plus one build; at most `cluster.epoch_poll_interval` plus 1 s with or without Valkey; never above `authz.cache.max_staleness` plus one build |
| A user's external flag | The principal cache of the authentication stage, through the change area `auth` (R-AUTH-023) | `auth.revocation_bound` |
| A Bastion's kept answers | Its revocation feed: a lowered project level and a changed repository level drop every answer (`grants`), a changed flag the user's (`principal`) (R-BAS-015) | The feed's poll interval |

Lowering a project, a repository's level and setting the external flag are journaled narrowings: a restore to a point before them
applies them again, and never applies a raise (R-AUTHZ-036, R-AUTH-041).

## What this does not cover

### H-880 · The wiki level does not hide documentation shipped inside an artifact

Component: the wiki part of R-AUTHZ-034. Subject: a repository whose wiki is less open than its artifacts. Live once such a
repository holds packages that carry their README inside the artifact: an npm tarball's `README.md`, the file a `.nupkg` names, the
README and the plugin sources of an Ansible collection archive. Whoever may download the artifact reads them, whatever the wiki
level says; trove cannot change an artifact's bytes without breaking every digest a client verifies. What makes it real: a README or
documentation that is meant to stay inside while the package is public.

### H-881 · Anyone can make a public remote repository fetch from its upstream

Component: the populate check of R-AUTHZ-008 for a read by a level (R-AUTHZ-035 rule 6). Subject: remote repositories whose artifacts
are `public` and whose `cacheFillAction` is `read`. Dormant until such a repository exists; the write that creates the state answers
with the warning `public_populates_remote`. Then every caller on the network can make trove download arbitrary upstream packages:
storage growth, upstream rate limits, possibly upstream cost. The installation-wide fetch budget of R-REPO-040 bounds concurrency,
not volume. What closes it: `cacheFillAction: deploy` on public remote repositories.

### H-882 · A user is internal until an administrator flags it external

Component: the external flag of R-AUTH-044. Subject: users of an identity provider that also signs in external people. Live while
`newUsersExternal` is `false` and such a person signs in for the first time: until an administrator sets the flag, the user reads
every `internal` repository. No claim of the identity provider sets the flag. What closes it: `newUsersExternal: true`, with
employees flagged internal by hand instead.

### H-883 · Lowering a level withdraws nothing a caller already holds

Component: the levels of R-AUTHZ-033. Subject: content that was readable by a level before it was lowered. Live on every lowering:
what clients downloaded stays in their caches, lockfiles and mirrors outside trove; a Bastion keeps serving what it holds until its
feed applies the `grants` entry (R-BAS-026). What makes it real: content that was opened by mistake and read before it was closed.

### H-884 · `internal` is a property of the user, not of the network

Component: the internal subject of R-AUTHZ-030. Subject: personal access tokens and sessions of users who are not external. Live: a
leaked personal token of an internal user reads every `internal` repository from wherever it reaches the default endpoint or a
further endpoint that accepts personal tokens; only the Bastion refuses `internal` reads (Q-BAS-148). The token's scope narrows it
(R-AUTHZ-013); revoking the token ends it within the revocation bound. What makes it real: a leaked personal token and a default
endpoint reachable from where it leaked.

### H-885 · The installation's default level is not journaled

Component: `/api/v1/settings/visibility` (R-API-056). Subject: projects and repositories created after a restore. Dormant until a
restore goes back to a point before the default was lowered: the restored default is the older, more open one, and objects created
afterwards take it until an administrator lowers it again. Nothing that existed opens: the default decides only creations.
