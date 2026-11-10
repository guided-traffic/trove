# Ansible Galaxy: the virtual class

The virtual class of the Ansible Galaxy format serves a local and a remote member behind one server URL (R-ANS-022 to R-ANS-024). This
page is for a contributor to it: what the engine does and what the format adds, the documents of a member, the merge, the handlers, the
publish through the deploy target, the role ids, and the tests. The specification is [formats/ansible.md](../spec/formats/ansible.md);
the page of the format as a whole is [ansible.md](ansible.md).

## What has to be in your head first

- **The engine does the plan, the format does the documents.** The virtual engine ([internal/virtual](../../internal/virtual)) applies the
  repository's patterns, the pin rules and claims, and the per-member read check of the caller, caches the merged document by the members
  that took part, and walks the tiers of an archive. The format contributes `formats.VirtualAdapter` ([virtual.go](../../internal/formats/ansible/virtual.go)):
  the strategy per path, the document of a local member, the merge, and where the cached archives of a remote member are. It does not
  orchestrate the cache (C-9).
- **Two documents are merged, the rest is first match.** The version list (`collections/<ns>/<name>/versions.json`) and the collection
  document (`…/collection.json`) are merged over all members; the version document (`…/<version>.json`), the archive and the role
  documents are resolved to the first member that holds them (the tiers of R-REPO-019 step 6: local, cached remote, remote). Because the
  version document and the archive walk the same plan, the archive is served from the member whose document the client read.
- **The merged bytes hold no address.** A member's document is neutral: a local member's is a list of its rows, a remote member's is the
  upstream's document as the remote class stored it. The handler renders the answer for the repository the client addressed
  (`rq.Addressed()`), through the leaf `galaxydoc`, after the cache. A document therefore never names a member or an upstream.
- **A merge that cannot ask a member fails.** A partial result is never cached and never served as complete: the answer is 503
  `upstream_unavailable` with `Cache-Status: detail=partial` ([Q-ANS-44](../spec/formats/ansible.md#q-ans-44--a-merge-that-could-not-ask-a-member-fails-instead-of-serving-a-partial-list)).
- **The caller's view is applied after the merge.** The merged list is shared by every caller; `versionFilter` ([handlers_virtual.go](../../internal/formats/ansible/handlers_virtual.go))
  drops the versions a caller's path grants hide, asking `Resolver().ReadScope` for the members and `MayRead(member, package, version)` for the
  member that won the version, as `formats.CallerVersions` does for one repository. The list, its count, the collection's highest version
  and times and the version document use it; without a path-deciding grant it asks nothing. The version document is authorized on the path
  of the archive, as the local class does.
- **A remote member's list is read whole.** The first stored page arrives as the member's document; the adapter reads the others through the
  member (`Request.Member`), so the member's populate right decides whether a page the caller may not bring into the cache may be
  fetched. One budget of 512 pages serves a merge.
- **Conflicts are found where they can be.** A conflict is a version that two members hold with different archive digests. A digest of a
  remote version is known once its version document was read, so the merge reads the document of every version a local and a remote
  member both hold, and of no other ([Q-ANS-45](../spec/formats/ansible.md#q-ans-45--the-digest-conflict-is-looked-for-in-the-versions-a-local-and-a-remote-member-share)).

## The files

| File | Responsibility |
|---|---|
| [class_virtual.go](../../internal/formats/ansible/class_virtual.go) | registers the class and its adapter; the description of the two merged documents |
| [virtual.go](../../internal/formats/ansible/virtual.go) | `virtualAdapter`: `Strategy`, `ArtifactPrefix`, `LocalDocument` (list, collection, version document of a local member from the rows), `Merge` (versions, times), the list document |
| [virtual_remote.go](../../internal/formats/ansible/virtual_remote.go) | a remote member's parts: the pages read through the member, the cached and uncached parts, the recorded digests, `recordCollisions`, the page budget |
| [handlers_virtual.go](../../internal/formats/ansible/handlers_virtual.go) | the handlers of the routes of the class: root, collection, versions, version, artifact; the paging; the weak validator |
| [virtual_gate.go](../../internal/formats/ansible/virtual_gate.go) | the deploy target: the publish handed to the local class's handler, the task address rewrite, the import task lookup; `remoteMembers` |
| [virtual_roles.go](../../internal/formats/ansible/virtual_roles.go) | role ids by member: lookup, versions, archive |
| [virtual_metrics.go](../../internal/formats/ansible/virtual_metrics.go) | `trove_ansible_virtual_digest_conflicts_total`, bound through `registerBinder` ([bind.go](../../internal/formats/ansible/bind.go)) |
| [merge/](../../internal/formats/ansible/merge) | the pure leaf: `MergeLists`, `Page`, `MergeCollection`, `Compare` |

## The merge

`merge.MergeLists(parts []Part)` takes one `Part` per member (a remote member the caller may fill is two: its cached versions, tier
`TierRemoteCached`, and the rest, `TierRemote`; one the caller may not fill supplies the cached versions only, R-AUTHZ-008) and returns
the union newest first by semantic-version precedence, build metadata ignored, one entry per version, the entry of the lowest (tier,
position) part, the part it came from, and the conflicts. A local member's version never loses to a remote's because the tier decides
before the position. The pagination is cut after the merge (`merge.Page`), so `limit` and `offset` are consistent across members.

The collection document takes its `highest_version` from the merged list and its times from the members' collection documents
(`MergeCollection`): the earliest `created_at`, the latest `updated_at`. This is a second call of the engine and a second merged
document, so the first stays small.

## The local member

`LocalDocument` reads the rows of the member through the data port (`Store`): the list as the member's versions with their SHA-256
(read in pages of 1000), the collection summary, or one version row rendered with `galaxydoc.RenderVersion`. The revision it returns
is the `rev_seq` of the member's package row, read before the rows, so a publish that raced can only make it older than the document
and the next hit rebuilds (R-ARCH-015). A member without the collection holds no document.

## Publish and the import task

`ansible.collection.publish` serves the classes local and virtual. The handler of the virtual class (`virtualPublish`) resolves the deploy
target (`Resolver().DeployTarget`: 405 `no_deploy_target` without one, and without a local repository), derives a request for it
(`Request.WriteTarget`, so everything is decided on the target) and runs **the handler the local class registered** for the route. It
holds the 202 answer and gives the address of the task the virtual repository's, because the client polls the address it was given. The
checks of R-ANS-008, the ownership and the shadowing check of R-REPO-048 are the local handler's and the engine's: the shadowing check asks
the remote members of every virtual repository that has the target as a member for `collections/<ns>/<name>/collection.json`
(`PackageDocumentPath`). The upload through `/files/` of a virtual repository is routed by the dispatcher and needs no code here.

`virtualImport` looks the task up in the target at the time of the read (`Store.Task`): a task of a target the repository no longer has is
404, and so is an id that is no UUID.

## Roles

A role exists in a remote member whose upstream has `v1`. The id a virtual repository serves is `<member key>-<upstream id>`
([Q-ANS-19](../spec/formats/ansible.md#q-ans-19--role-ids-in-a-virtual-repository-name-their-member)); the part after the last `-` is
digits. `virtualRolesFind` runs the handler of the remote class for each remote member of the plan in order and answers with the first
whose answer is not empty, the roles' `id` rewritten and every role route in the document given the member's id (`rewriteRoleRoutes`);
`virtualRoleVersions` and `virtualRoleArchive` decode the id and run the handler of the remote class for that member with the upstream's
id. An id that names a member that is not in the plan (not a member, not readable, not remote) is 404 in the `v1` shape. Local members take
no part. The discovery document lists `v1` when the stored `api-root.json` of any remote member of the plan does.

## Extending it

1. A new merged document of a collection: add its kind to `documentOf` in [virtual.go](../../internal/formats/ansible/virtual.go), its
   strategy to `Strategy`, its local form to `LocalDocument` and its merge to `Merge`, and a handler in
   [handlers_virtual.go](../../internal/formats/ansible/handlers_virtual.go) that decides the read on the package first.
2. A new route: it is declared once in [routes.go](../../internal/formats/ansible/routes.go) with its classes; the virtual handler is a
   `registerHandler(formats.Virtual, route, …)` in `handlers_virtual.go`.
3. A new metric: register it in `newVirtualMetrics`, count it from the merge, and add the series to the forget test.

## Tests

| Tier | Where | What |
|---|---|---|
| Unit, leaf | [merge](../../internal/formats/ansible/merge/merge_test.go) | the union, the order, the winner by tier, the conflicts, the page, the collection times, `Compare` and its fuzz target |
| Unit, class | [virtual_test.go](../../internal/formats/ansible/virtual_test.go) | the handlers on the kit's Host with a resolver that plans and merges as the engine does and a remote engine over canned documents: the list and its paging, the conflict counter, pages through the member, the restricted member, the partial merge, the collection, the version document, the archive, the discovery document, the publish through the target, the task lookup, the role ids, the metric |
| Kit | [conformance_test.go](../../internal/formats/ansible/conformance_test.go) | check 5 (merge, with a local and two remote members) and check 13 (the merged request is decided on its canonical path) |
| Integration | [test/integration/ansiblevirtual](../../test/integration/ansiblevirtual) | the real engine, a local and a remote member that reads the stub: the union and its pages, a version both members hold, claims, the shadowing check, publish through the target and the task through the virtual address, a caller who may read one member, the partial merge with the stub down, the discovery document, the role ids |
| Integration | [test/integration/importer/ansible_test.go](../../test/integration/importer/ansible_test.go) | the importer against the stub into a local repository |

A test that replaces a handler of another class (`withHandler`) restores it when it ends; the registry is process-wide.
