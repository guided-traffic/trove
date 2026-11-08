# OCI virtual repositories

Status: built (T-16, WP9): the virtual class of the OCI format (R-OCI-027, R-OCI-028): a pull through an address that combines local and remote repositories, the
lists as unions, and a push that is executed on the deployment target. The routing of virtual repositories (patterns, claims, pin rules) is on
[repository-model.md](repository-model.md), the decision point and the token scope on [permissions.md](permissions.md) and
[tokens-and-credentials.md](tokens-and-credentials.md), the exposure rules on [endpoints-and-exposure.md](endpoints-and-exposure.md), what a remote member's
upstream can make trove store on [oci-remote.md](oci-remote.md), and the upload sessions on [oci-uploads.md](oci-uploads.md).

A virtual repository holds nothing of its own: every answer is built from members, so on every road the question is which members take part for this caller and
which repository's rules apply. The rules are those of the repository the client addressed, the virtual one (`rq.Addressed()`), for exposure and for the scope of a
token; the member decides the grants. The requirements are R-OCI-027 and R-OCI-028 in [spec/formats/oci.md](../spec/formats/oci.md) and R-AUTHZ-009 in
[spec/authorization.md](../spec/authorization.md); the code is [virtual.go](../../internal/formats/oci/virtual.go),
[virtual_read.go](../../internal/formats/oci/virtual_read.go) and [virtual_write.go](../../internal/formats/oci/virtual_write.go).

## Reads: which members take part

| Step | What is decided | On which repository | Where | Answer |
|---|---|---|---|---|
| 1 | `read` on the virtual repository itself (the gate), before any member is consulted | the virtual repository | the dispatcher's route-level decision | 404 as for a missing repository; 401 for an anonymous caller who cannot read every member |
| 2 | `read` on the image name and the path under the endpoint's ceiling, and the repository's patterns | the virtual repository | `Authorize` at the top of each handler of `virtual_read.go` | 404 `MANIFEST_UNKNOWN`/`NAME_UNKNOWN` |
| 3 | The members that take part: the routing (patterns, claims, pin rules) for the image name as the client typed it, then `read` and the right to fill per member | each member, decided as a read through the virtual repository | `Resolver.Plan` → `members` | a member the caller may not read takes no part, silently (R-AUTHZ-009 rule 2) |
| 4 | The path of the item in each member: a local member's tag or digest, a remote member's cache or upstream, the other spelling of a Docker Hub name | each member, on a request made for the member (`Request.Member`) | the handler of a direct request | a member that refuses the caller is a miss |
| 5 | Every tag and every referrer of a list, once in its member and once on the virtual repository | member, then virtual repository | `PathsAllowed` (the listing form, no audit event per entry) | the entry is not listed and never named in a `Link` |

Dependency confusion is closed in step 3: a claim of an image name for a local member keeps every remote member from serving it through the virtual address, so a
public image of the same name does not replace an internal one. A first push of a name that a remote member serves is a shadowing decision of the writer (R-REPO-048),
made on the deployment target with the same check as for any format; a writer who holds `manage` decides it and the push claims the name (`autoClaim`).

`Trove-Resolved-From` names the member that served an item. It is never sent on an endpoint with exposure rules, and a member's failure is worded as the addressed
repository's there, so the code of a failure does not tell that the virtual repository reaches an upstream beyond what H-294 says for every format.

## Writes: where a push goes

The write routes serve the class virtual (ADR-180). The handler resolves the deployment target, decides `deploy` and the ownership of the image name on a request made
for the target, commits there, and calls the upload sessions with the request for the addressed repository: a session records the addressed repository and the target, and
every `Location` of a session and of a blob names the addressed repository. The session port checks the principal, the binding (the image name) and `deploy` on the
target on every call, and a virtual repository whose deployment target changed ends the sessions of the old one (404 `BLOB_UPLOAD_UNKNOWN`).

A manifest push links the blobs and manifests it names that the target lacks from the members that hold them, before the reference check. The source of the link is the
virtual repository by name, so the host resolves it as a read of the virtual repository is resolved (the routing, the per-member read check, the claims, the block of a
finding); a caller can therefore link only what it could pull through the same address. The commit is the caller's `deploy` on the target's path, decided before the link.
An index's manifests are read from the members' stored copies only (the first two tiers; no upstream request is made for a push).

A virtual repository without a deployment target takes no write: the decision point refuses it (403 `DENIED`), and a registry token carries no `push` for it.

A `DELETE` through a virtual repository answers 405 `UNSUPPORTED` and reaches nothing.

## What this does not cover

### H-700 · The resolution of a virtual repository is not counted in `trove_virtual_resolutions_total`

The engine counts which tier served an artifact (`resolved`); the format's own walk calls the members' handlers and does not report the tier to the engine, so the metric
does not move for a pull through an OCI virtual repository and an operator cannot see from it how often an upstream is asked. The remote members' own request metrics
(`trove_http_requests_total`, the engine's fetch counters) show the upstream traffic. Live today.

### H-701 · A tags list the upstream cut is continued only when a client addresses the remote itself

A remote member contributes the tags list it stores. An upstream that cuts its list (Docker Hub and GHCR page at a few hundred tags) leaves a stored part and a mark
(`x-trove-truncated`), and the union through the virtual repository holds the stored part only: a tag beyond it is missing from the listing, never wrong, and a pull by
its tag still resolves. A client that lists the remote repository itself gets the upstream's further pages (R-OCI-024). Live today for every remote member whose upstream
cuts its list.

### H-702 · A push links from the virtual repository by the name the client typed, so a Docker Hub member's `library/` spelling is not found for a one-component name

The link step resolves `nginx/_blobs/<d>` through the virtual repository, and a Docker Hub remote member stores `library/nginx/_blobs/<d>`. A push of an image named `nginx`
that is based on layers the member cached under `library/nginx` is not linked (the client uploads the layers, or the reference check answers `MANIFEST_BLOB_UNKNOWN` when
the client skipped them because the virtual repository's `HEAD` answered 200 from the member's cache under the typed name). The pull path applies the mapping per member; the link
step goes through the host's resolution of the source, which does not. Live today for a virtual repository with a Docker Hub member without a namespace prefix and a push under a
one-component name.

### H-703 · The union of a tags list reads every tag of every local member, up to 100,000 each, on every request

The tags list of a virtual repository is merged per request from what each member holds and the caller may read, and paged afterwards. A page of ten tags of a name with
100,000 local tags reads, decides and merges all of them. The cost is bounded by `MaxTaglistEntries` per member and by the number of members, and a remote member contributes a stored
document of at most the same size; there is no merged-metadata cache because a cached merge would have to be decided per caller (Q-OCI-160). Live today for a virtual repository over
a local member with a very long list.

### H-704 · A member that refuses the caller on a path is a miss, and its refusal is audited as a direct request's

A member whose path grants or patterns refuse the caller for a tag or a digest answers the walk with the refusal of a direct request, which the walk reads as a miss and
goes on. The caller is therefore told "not found" by the virtual repository for an item that exists in a member it may not read, which is the intended answer (R-AUTHZ-009 rule 8), and
the denied decision of the member is written to the audit trail as for a direct request, once per member consulted. A busy virtual repository over a member that refuses a name
writes that event on every pull of the name. Not verified: whether the audit trail deduplicates these events per credential and name in the same way as for a direct request.

### H-755 · The deletion path of a virtual repository is covered by its 405 test only, not by the conformance suite

The conformance suite's content-management category is run through the local address and not through a virtual one, because a delete through a virtual repository is refused by design with 405
`UNSUPPORTED` (R-OCI-028, [Q-OCI-211](../spec/formats/oci.md#q-oci-211--the-conformance-suite-runs-all-four-categories-through-the-local-address-and-pull-push-and-discovery-through-the-virtual-address)).
What keeps the refusal honest is `TestR_OCI_028_ADeleteThroughAVirtualRepositoryAnswers405AndDeletesNothing` ([test/integration/ocivirtual](../../test/integration/ocivirtual/virtual_test.go)); no test drives the
suite's delete sequence at a virtual address, so a change that let a delete through (to the target or to a member) would be caught by that one test and by nothing that exercises the other
specifications of the category. Live today.

