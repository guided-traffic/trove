# OCI remote repositories

Status: built (T-16, WP8): the remote class of the OCI format, a cache in front of another registry (R-OCI-021 to R-OCI-025). The handshake's I/O, the
credential hosts and the token cache are the engine's and are on [remote-repositories.md](remote-repositories.md#the-token-handshake-and-the-credential-hosts);
this page is what the format adds: what a registry can make trove store and show. The mirror mode and the virtual class are other pages.

The upstream is a party trove does not trust. A registry names the realm trove asks for a token, the host a blob is redirected to, the bytes of a manifest
and the tags it lists; trove stores some of it and serves it to clients that trust trove. The outbound rules it relies on are on [egress.md](egress.md) and
[transport-encryption.md](transport-encryption.md), the read permission on [permissions.md](permissions.md). The requirements are R-OCI-020 to R-OCI-025 in
[spec/formats/oci.md](../spec/formats/oci.md); the code is [internal/formats/oci](../../internal/formats/oci) (`remote.go`, `remote_auth.go`,
`remote_revalidate.go`, `remote_handlers.go`).

## Where an address comes from

| Address | Built from | Checked by |
|---|---|---|
| A manifest, blob or tags list | `remote.url` (origin and namespace prefix), the image name and the tag or digest of the canonical path; the path passed the grammars of R-OCI-002 | `registryURL`; a path outside the layout is 404 and sends nothing |
| The token realm | The `realm` of the upstream's challenge | The engine: the origin or an `extraHosts` entry, else 502 `foreign_host_denied` and nothing is sent; the credential goes to a `sendCredentials` host only |
| A blob's redirect target | The upstream's `Location` | The engine's redirect rules (R-REPO-039): up to `proxy.max_redirects` hops, `https` to `http` refused, every hop through the egress rules and TLS verification. `extraHosts` does **not** gate a redirect (Q-OCI-125), so the upstream chooses the host a blob comes from; the repository's credential and the token go to no redirect target (the stub's CDN answers 400 to an `Authorization`) |
| A pass-through query | The image name and a digest or the `n` and `last` of the client, as the grammars admit them | `UpstreamQuery`; a path that is none of the two shapes is 404 |

The scope of a token is taken from the request and never from the challenge (Q-OCI-82): a hostile or stale challenge cannot make trove ask the realm for
a repository the request does not address. The scope is read from the right of the path, so an image name that holds a protocol word (`a/manifests/b`) is
scoped as itself. The token goes to the upstream origin only.

The upstream test asks `/v2/` at the root of the host, never below the namespace prefix of the URL (Q-REPO-281).

## What is read of an upstream document

| Document | Bound | What is checked | Where |
|---|---|---|---|
| A manifest by tag or by digest | 4 MiB, 32 levels, 100,000 values, 10,000 descriptors (R-OCI-020); a longer answer is refused by its announced length or while it is read, before it is spooled | The strict parser of the format; every digest in a descriptor passes the grammar; `schemaVersion` 1 and the schema 1 types are refused; a manifest by digest is verified against the digest of the path by the engine, a tag against the `Docker-Content-Digest` it announces | `PrepareFill`, `InspectFill`, `AcceptResponse` |
| A tags list | 4 MiB and 100,000 entries | JSON of `name` and `tags`; a tag outside the grammar is dropped; the page a client gets is rendered by trove, with trove's name | `PrepareFill`, `parseTaglist` |
| A referrers answer | 4 MiB; at most 1,000 descriptors are decided and listed | An image index of descriptors; a descriptor with a digest outside the grammar, one the caller may not read, or (with a filter) of another `artifactType` is dropped | `readableReferrers` |
| A token document | 64 KiB; the token 16 KiB | `token` or `access_token`; a token with a space, a control character or a non-ASCII byte is refused | `ReadToken` |
| A blob | `limits.maxArtifactBytes` | The SHA-256 of the bytes against the digest of the path | the engine; blobs are not parsed or unpacked |

A document that fails is a 502 and nothing is stored: no entry, no item, no row, no negative entry. A manifest carries rows into the format's tables only
after it passed the parser, in the transaction that stores it; the rows go with the alias item (Q-OCI-34).

## Names

On a repository whose upstream is Docker Hub itself (no namespace prefix) a one-component name is below `library/` (Q-OCI-14, R-OCI-025):
a read is allowed on **both** spellings, `nginx` and `library/nginx` (Q-OCI-129): `Authorize` on the one the client typed and a non-auditing decision-point check
on the other, for every route and for each tag and referrer a list shows. The cache, the screens and the populate check use `library/<n>`, so an exclusion written
for either spelling, in a token scope, a policy, an exposure rule, a pattern or a claim, holds whichever the client types, for an image that is cached and for one
that is not. A grant scoped by package or path must name both (`nginx` and `library/nginx`); an include-all grant covers both. The token service keeps deciding on the
typed name as a ceiling; the handler decides both. A cross-repository mount whose source is a Docker Hub remote's cache is decided the same way for the source
(`SourceSpeller`). Below a namespace prefix nothing is mapped. The catalog of the registry root decides each name with the decision point only, not with the patterns
and the claims of a remote (R3-2): a name of a remote whose row predates a pattern or a claim is listed and unreadable.

## When trove cannot ask the upstream

A repository that is offline, the installation in offline mode, an open circuit and a caller who may not fill the repository (`cacheFillAction`) are answered
from the cache: a tags list ends at the stored window, without a `Link`, and referrers answer 404 so that a client falls back to the tag schema through the same
remote. They are not answered with an empty index, which would say that nothing refers to the digest (Q-OCI-128). A pass-through query is decided for populate
with no package (the engine builds the check with the path of the query), so a populate grant scoped by package is never met by it and fails closed (H-669).

## A tag the upstream does not allow trove

A registry answers `401` with `error="insufficient_scope"` to a valid token for a repository that does not exist or is private (Docker Hub). That is read as a
missing image: 404 `MANIFEST_UNKNOWN` with a negative entry, and the remembered challenge is kept (Q-OCI-127). It is read only after the engine's own token was
refused; it is a miss only (a stored tag whose revalidation meets it keeps serving stale, and no gone mark is set, so the cached manifests and blobs of the image keep
serving). It is also what a private image of the upstream looks like to a client of trove, so trove does not tell the two apart. The negative entry is keyed by
repository and path, not by credential: a rotation of the secret does not clear it, the not-found period or `POST …/cache/invalidate` does.

## What a reader of the cache may see

A caller who may read a repository reads every image name of its cache that its grants admit, by the same decisions as the local class: the pull of a tag and of a
digest are decided on the name and the path, a tags list is filtered tag by tag, referrers descriptor by descriptor. A caller who may not fill the
repository (`cacheFillAction`) gets what the cache holds and a refused fill is audited as every refused fill is.

## What this does not cover

### H-666 · A cut tags list is assumed to be in byte order

A window beyond the stored part of a tags list the upstream cut is passed through, and a window inside it is answered from the stored document, which
`parseTaglist` sorts. An upstream that cuts a list it does not list in byte order stores a part that is not the first tags in byte order, and a client that
pages by `last` can miss tags (a tag listed after the cut that sorts before the last stored one). Distribution registries, Docker Hub and GHCR list in byte
order. Live for such an upstream only; the effect is a missing tag in a listing, never a wrong image, because a pull names its tag.

### H-667 · A tag is checked against the digest the same upstream announces, and against nothing else

The digest of a manifest by digest and of a blob is verified against the path. A tag is verified against the `Docker-Content-Digest` of the same answer, which
catches an inconsistent upstream, a proxy that changed the bytes or a cut transfer; it does not catch an upstream that serves other bytes with a matching header,
and an upstream that sends no such header (the distribution specification makes it optional for a `GET`) is believed: the manifest is stored under the digest of
the bytes received and served by its tag. The control against an attacker on the path is TLS ([transport-encryption.md](transport-encryption.md)), and against a
hostile upstream the client pinning by digest. Live today for every remote.

### H-668 · The descriptors of a referrers answer are the upstream's claims

A referrers answer is passed through after the caller's readability filter. The `artifactType`, the `annotations` and the sizes of its descriptors are what the
upstream says; trove does not fetch the manifests to compare them. A client that reads them (a signature verifier selecting by `artifactType`) trusts the
upstream for the listing and verifies the manifest it then pulls by digest. Live today for every remote; the answer is kept for the metadata period.

### H-669 · A populate grant scoped by package never admits a pass-through query

The tags list window beyond the stored document and the referrers are pass-through queries. Their populate check is built from the path of the query, which names
no package to the engine (`formathost/query.go`), so a caller whose `cacheFillAction` permission is scoped by package or by path is treated as one who may not
fill: the stored window answers, and referrers answer 404. It fails closed. Live today for a repository whose fill permission is not the repository-wide one;
real for such a caller, who sees no referrers and the stored part of a long tags list only.
