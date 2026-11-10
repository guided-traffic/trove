# Ansible Galaxy remote repositories

Status: built: the remote class of the Ansible Galaxy format, the proxy and cache of a Galaxy API v3 and of its v1 role API
([R-ANS-016 to R-ANS-018 and R-ANS-020](../spec/formats/ansible.md)), the mirror (R-ANS-021) and the token exchange of a hub behind single
sign-on (R-ANS-019), the last proven against the stub's realm only ([H-842](../spec/formats/ansible.md#h-842--the-hub-token-exchange-is-specified-from-documentation-and-never-run-against-the-service)).

This page describes what an upstream Galaxy can make trove do and show, and what limits that. The upstream is a party trove does not trust:
its documents decide which addresses trove contacts, which bytes it stores and what its clients are told. The outbound rules it relies on
(addresses, TLS, credentials, redirects) are on [egress.md](egress.md), [transport-encryption.md](transport-encryption.md) and
[data-and-secret-flow.md](data-and-secret-flow.md); the patterns, claims and the shadowing check on [repository-model.md](repository-model.md); the read
permission on [permissions.md](permissions.md). The code is [internal/formats/ansible](../../internal/formats/ansible) (`remote*.go`,
`handlers_remote.go`) and the data port [internal/formatdata/ansible](../../internal/formatdata/ansible/remote_store.go).

## Where an address comes from

A Galaxy names its API in its root document, `GET <remote.url>`: `{"available_versions": {"v3": "v3/", "v1": "v1/"}}`. trove reads it first
and builds every later upstream address from `remote.url`, a prefix that document announced and the canonical path of the request, never from a
host or a URL a client sent.

| Step | What is decided | Where | Answer |
|---|---|---|---|
| 1 | The API root is a metadata document of the repository: stored, revalidated, collapsed and counted like every other | the engine's discovery step (`Discoverer`), [remote_discovery.go](../../internal/formats/ansible/remote_discovery.go) | 502 `upstream_unavailable` when the document is no API root, with the hint that a Galaxy's API root usually ends in `/api/`; nothing is stored |
| 2 | A prefix is a relative path of at most 128 characters: no scheme, no leading slash, no `.` or `..` segment, no query | `parseDiscovery` | 502 `upstream_unavailable` |
| 3 | A collection name, a version and a role id that reach the wire are the ones the grammar of the path accepts; every segment is one escaped segment | `parseTarget`, `upstreamURL` | the path is no resource of the remote: 404 |
| 4 | The host of an address is the upstream's origin or matches `remote.extraHosts`; a redirect leaves `https` for `http` never | the upstream client of the engine (`Allowed`) | 502 `foreign_host_denied`, 502 `insecure_redirect`; no request is sent |
| 5 | The credential of the remote goes to the origin and to the `extraHosts` entries with `sendCredentials`, and nowhere else | the client's hop policy | the request carries no `Authorization` to a host the remote does not send it to |

The address of an archive is the `download_url` of its version document, kept as a URL-map row (R-REPO-043) only for the archive of the
document's own collection and version, and only when its host is the upstream's or an `extraHosts` entry: a version document that names its
archive elsewhere is refused whole (502), so a document cannot send a fetch to a host the operator did not allow. It passes steps 3 to 5 again
when it is used.

## What is read of an upstream document

| Document | Bound | What is checked | Where |
|---|---|---|---|
| API root | 16 MiB (`MaxDocumentBytes`) | the shape above | `parseDiscovery` |
| Collection | 16 MiB | the namespace and the name are the path's | `checkCollection` |
| Version list (a page) | 16 MiB; at most 51,200 versions per collection | `meta.count` and the entries parse; a longer list is 502 | `parsePage` |
| Version | 16 MiB | the namespace, name and version are the path's; `artifact.sha256` is 64 lower-case hexadecimal digits; `download_url` is `http` or `https` on an allowed host without user information | `checkVersion` |
| Role lookup, a role's version page | 16 MiB | a JSON object with a list of results; a role without a numeric id is left out | `renderRoleLookup`, `parseRoleVersions` |

A document that fails is 502 and the previous stored entry stays; nothing of it is stored. The stored bytes are the upstream's, as received; a
client sees a rendering of them with trove's addresses and without the plugin listing (`metadata.contents`, `docs_blob`, `manifest`, `files`), so
the 0.85 MB version documents of the public Galaxy cost the client nothing and no upstream address reaches it.

## The digest rule

The announced SHA-256 decides the archive ([Q-ANS-13](../spec/formats/ansible.md#q-ans-13--the-announced-sha-256-is-always-verified-the-first-recorded-digest-stays)):

- the gate (`BeforeArtifact`, [remote_gate.go](../../internal/formats/ansible/remote_gate.go)) returns the recorded SHA-256 of the version, and the engine
  verifies the bytes against it whatever `integrity.checksumPolicy` says; `ignore` and `pass-through` are refused when the repository is saved
  (`ChecksumAlwaysVerified`). A mismatch is 502, nothing is stored, the integrity event is written;
- the fill hook reads the archive before any byte reaches a client and holds its `MANIFEST.json` to the namespace, name and version of the path
  (`identity_mismatch`), so an upstream that serves another collection under a valid digest is refused too ([Q-ANS-37](../spec/formats/ansible.md#q-ans-37--a-fetched-archive-is-held-to-the-identity-of-its-path-requires_ansible-is-not-compared));
- the digest recorded first for a version stays while its archive is cached; the served version document keeps the recorded digest and size and
  the event `proxy.upstream_digest_changed` is written when the upstream announces another ([Q-ANS-38](../spec/formats/ansible.md#q-ans-38--the-first-digest-of-a-version-stays-only-while-its-archive-is-cached), the SQL rule is
  [ansible_remote.sql](../../internal/meta/postgres/queries/ansible_remote.sql) `AnsibleUpstreamRecord`).

Signatures pass through unverified ([H-839](../spec/formats/ansible.md#h-839--a-remotes-signatures-are-passed-through-and-not-verified-by-trove)).

## Roles from GitHub

A role is not an archive the Galaxy stores: each version names a `download_url`, usually a GitHub archive. trove serves roles by translation
(R-ANS-020, [Q-ANS-6](../spec/formats/ansible.md#q-ans-6--roles-are-served-by-translation-from-a-remote-only-collections-are-the-stored-artifact), [Q-ANS-7](../spec/formats/ansible.md#q-ans-7--role-archives-are-pulled-from-the-hosts-of-the-roles-versions-and-cached-without-a-digest)):

- **The host rule.** A version is offered only when its `download_url` is an `https` URL without user information on a host of
  `remote.extraHosts` (the defaults are `github.com` and `codeload.github.com`). Any other version is left out of the list the client sees,
  counted in `trove_ansible_role_versions_dropped_total{repo}`, and no URL-map row is recorded for it, so neither the list nor a direct request
  can make trove fetch from another host ([Q-ANS-40](../spec/formats/ansible.md#q-ans-40--the-role-host-rule-is-applied-twice-when-a-version-list-is-stored-and-when-it-is-served)). The engine's host check applies at the fetch as well.
- **The path is built from the document's own id.** The archive path `roles/<id>/<ref>.tar.gz` is mapped only by the version page of the same
  `<id>`, so a document of one role cannot map another role's archive.
- **No digest.** The upstream announces none, so nothing is verified except that the archive is a gzip-compressed tar archive within the bounds of
  R-ANS-031 (no link, no duplicate name, no bomb, no data after its end) and within `ansible.role_archive_max_bytes` (the declared length and the
  bytes read). Under `integrity.checksumPolicy: require` the archive is refused with 502 `upstream_digest_missing` ([H-840](../spec/formats/ansible.md#h-840--a-role-archive-has-no-announced-digest-and-the-source-is-not-byte-stable)).
- **The credentials.** The client downloads a role archive without the token header and sends `~/.netrc` instead; the role archive route accepts Basic
  with a trove token as the password and answers 401 with `WWW-Authenticate: Basic realm="trove"` without one (R-ANS-015). Git and URL roles bypass trove
  ([H-844](../spec/formats/ansible.md#h-844--git-and-url-roles-bypass-trove)).

## The hub's credential

A hub repository stores an offline token (a refresh token) as its credential and `ansible.authUrl`, the token endpoint of the realm
([Q-ANS-51](../spec/formats/ansible.md#q-ans-51--the-hub-exchange-is-the-optional-interface-credentialexchanger-and-the-engine-sends-the-exchanged-token-and-never-the-credential)). The token is in the body of the request to the realm, where the hop policy that strips an `Authorization` header cannot withhold it,
so the rule is stricter than for a header:

| Step | What is decided | Where | Answer |
|---|---|---|---|
| 1 | `authUrl` is an `https` URL (`http` only for a loopback address) without user information, query or fragment; the repository's `authMode` is `bearer`; the host is the upstream's or an `extraHosts` entry with `sendCredentials` | [validate_ansible.go](../../internal/repository/validate_ansible.go) when the document is saved | 422 `invalid_value` at `/ansible/authUrl` |
| 2 | The same host rule, again, at every exchange: the realm must be a host the credential may go to, and an https upstream is never left for cleartext | [credential_exchange.go](../../internal/proxy/credential_exchange.go) `sendExchanged` | 502, nothing is sent |
| 3 | The offline token goes to the realm and to nothing else; the upstream is called with the access token, never with the offline token, and a request is not sent at all when the exchange failed | `sendExchanged`, the hop policy (`outbound.WithAuthorization`) | 502 `upstream_auth_failed` |
| 4 | The access token is kept in the replica's memory only, under a key that holds the digest of the credential, so a rotated offline token needs a new exchange; neither token is logged, audited or returned by an API | `proxy_upstream_tokens` cache | |

The request to the realm carries no `Authorization` header: the engine marks it as one without the repository's credential
(`outbound.WithoutCredentials`), so the realm receives the refresh token once, in the body. A mirror's request for the collection index takes
the same exchange as a fetch (`Engine.Get`), so a mirror of a hub lists with the access token.

## What this does not cover

### H-856 · The patterns of a remote see a role by its id and not by owner and name

Stage: the include and exclude patterns of a remote repository (R-REPO-005) and the grants, which decide a role by the package name `role/<id>`
([Q-ANS-36](../spec/formats/ansible.md#q-ans-36--a-role-is-a-package-of-the-cores-name-machinery-named-role-and-its-id)).

Live today for every remote of the format. An operator who limits a remote to `acme.*` hides every role, because no role is named `acme.…`; an
operator who wants to hide one role writes `role/16`, the upstream's id, which no document of trove shows. What makes it real: a repository
that must serve roles of some owners only. A role that is excluded answers 404 like any package that is not admitted.

### H-857 · A republished version whose archive was evicted is trusted at its new digest

Stage: `RecordAnnounced` ([remote_store.go](../../internal/formatdata/ansible/remote_store.go)); the recorded digest of a version stays only while its
archive is cached ([Q-ANS-38](../spec/formats/ansible.md#q-ans-38--the-first-digest-of-a-version-stays-only-while-its-archive-is-cached)).

Live today. After the cached archive of a version is evicted or deleted, the next fill of its version document records the digest the upstream
announces then, and the archive fetched next is verified against that one. An upstream that republished the version with other bytes in between is
believed, and no event says it, because there is no cached copy the new digest contradicts. What makes it real: an upstream that republishes
versions and an installation whose cache evicts them (`unusedRetentionHours`). The first fetch of any version is trusted the same way, as for every
format: the digest protects bytes trove holds, not the upstream's history.

### H-859 · The event of a digest that stayed is queued before the fill commits

Stage: the data port queues `proxy.upstream_digest_changed` (an asynchronous event) from `RecordAnnounced`, inside the fill's transaction
([remote_store.go](../../internal/formatdata/ansible/remote_store.go) `digestChanged`).

Live today, with a narrow effect. A fill whose transaction fails afterwards has queued the event already, and the fill is repeated by the next
request, which queues it again; the audit trail may name a changed digest twice. It never hides one. Closing it moves the event to a hook that runs
after the commit, which the contract's `OnFill` does not offer.
