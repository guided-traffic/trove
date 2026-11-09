# Debian virtual repositories

Perspective: what a virtual repository of the Debian (APT) format trusts when it merges the indexes of its members into one index and signs it
with a key of its own, and who can change what that signature vouches for. The mechanisms are the merge
([index/merge.go](../../internal/formats/deb/index/merge.go)), the build of a virtual repository
([virtual_build.go](../../internal/formatdata/deb/virtual_build.go), [virtual_store.go](../../internal/formatdata/deb/virtual_store.go),
[deb_virtual.sql](../../internal/meta/postgres/queries/deb_virtual.sql), [refresh_virtual.go](../../internal/formatdata/deb/refresh_virtual.go)), the
gate and the member paths ([virtual_gate.go](../../internal/formats/deb/virtual_gate.go),
[handlers_virtual.go](../../internal/formats/deb/handlers_virtual.go)), the rule that lets a virtual repository hold generated items
([publish_virtual.go](../../internal/items/publish_virtual.go)) and the hooks that enqueue its builds
([wire_deb_virtual.go](../../internal/app/wire_deb_virtual.go)). The requirements are
[R-DEB-025 to R-DEB-028](../spec/formats/debian.md#r-deb-025--virtual-repository-what-it-serves-and-how-it-merges); the developer view is
[deb.md](../developer/deb.md#the-virtual-class). The key trove signs with is [deb-signing.md](deb-signing.md); what a remote member checks before it
stores a byte is [deb-remote.md](deb-remote.md); the build and publication path it shares with a local repository is [deb-index.md](deb-index.md).

A client that trusts the key of a virtual repository trusts the merge: every package that one of its members lists and that the claims and
patterns let the member serve. The virtual repository signs the result and nothing it was not given by a member it could verify.

## Trust boundaries

```
 APT client --(1)--> virtual repository --(2)--> local member (rows, published generation)
   holds the         merges, signs with          remote member (engine: stored, verified release and index)
   virtual key       its own key                          |
                                                          (3)--> upstream archive, through the remote engine

 (1) the request is decided by the decision point: read on the virtual repository, and for the generated files read on every member
 (2) the build reads the members' stored documents; a caller's rights play no part in what it merges
 (3) background requests of the remote engine, under the egress, credential and throttling rules of the remote
```

| Party | Trusts | Can do |
|---|---|---|
| A client | the virtual repository's key and nothing of a member | read the merged index; fetch each file from the member whose paragraph it read |
| A deployer of a local member | the ownership and claim rules of the member | add or remove the member's packages in every virtual repository that lists it; add a suite ([H-728](#h-728--a-deployer-of-a-local-member-adds-a-suite-to-every-virtual-repository-that-lists-it)) |
| An operator of a remote member | `deb.upstreamKeys` of the member | decide which releases of the upstream the member stores, and so which the virtual repository merges |
| An operator of the virtual repository | the member list, patterns, pin rules, claims, the signing key | decide which members and names the merge takes |
| The upstream of a remote member | by the member's keys | serve releases a current key signs; cannot make the virtual repository merge an unverified one |

## What is checked, and where

| Input | Check | Where |
|---|---|---|
| A remote member's release | the stored copy is verified by a key of the member's current set (`verified_by`); a member without keys is refused when it is added (R-DEB-020) and at a build | `remoteMember` |
| The same | `Valid-Until`, when it has one, has not passed (`member_expired`); the `Date` is not older than the one the last build recorded for the same upstream address (`member_rollback`) | `admitRelease` |
| A member's index file | a remote's is the file its verified release names (digest compared before the merge reads it); a local member's is its published generation | `openMemberIndex` |
| A paragraph | package, version and architecture match the grammar of the format; `Filename` is relative, with no empty or dot segment and no backslash; otherwise it is left out and counted | `index.MergeMember`, `MemberFilename` |
| The merge | at most 1,000,000 distinct packages per index (`ErrTooMany`); a remote index is read at most 1 GiB | `index.Seen`, `maxIndexRead` |
| The result | signed by the virtual repository's own key; `Valid-Until` is the earliest of the members' | `virtualSuite`, `index.Assemble` |
| A read of a generated file | read on the virtual repository and on every member without package restriction (403 `deb_members_unreadable`; anonymous 401) | `virtualGate` |
| A read of a package file | the member path names a member of the virtual repository; read on it for the coordinate; for a remote member its `cacheFillAction` | `handleMember` |
| A write | the dispatcher sends it to the deploy target; a grant of `deploy` or `delete` on the virtual repository itself has no effect (R-AUTHZ-009 rule 5) | `redirectClass`, `TestR_AUTHZ_007_DebActionMapOfTheVirtualClass` |
| A generated item | the publication port is the only writer of a virtual repository's items | `checkPublishTarget` |

**What the claims do.** The order of the members and the names each serves are decided on stored configuration, never by the caller
(Q-DEB-63): a name claimed by a local member loses every remote paragraph, which is the protection against a remote member that offers an
internal name with a higher version. `TestR_DEB_025_AClaimKeepsTheNameOfTheOtherMembersOut` shows the paragraph leaving the index.

**A build that cannot merge a member publishes nothing.** The suite keeps its previous signed generation and its state says `failed` with the
reason (`member_unverified`, `member_expired`, `member_rollback`, `member_unavailable`), because a signed index without a member's packages would
remove them from every client. [H-722](../spec/formats/debian.md#h-722--a-repository-whose-builds-fail-keeps-serving-its-last-generation) is the
cost of that choice.

**Verified in this repo.** `test/integration/debvirtual` runs the production composition root with a local member, a remote member that reads the
counting stub and a real signature check: the merge and the signature with the virtual repository's key, the claim, the gate for four principals
and the anonymous caller, the member paths, the upload through the deploy target, a key change that unverifies a stored release, a release older
than the recorded `Date`, the carried-over `Valid-Until`, the shadowing check through a real virtual repository, and `apt-get update` and
`apt-get install` from `debian:13` and `ubuntu:22.04`. **Not verified end to end:** `member_expired` (the stub can serve a `Valid-Until` in the past
or six days ahead, not one that passes between two builds; `TestAdmitRelease…` in
[virtual_build_test.go](../../internal/formatdata/deb/virtual_build_test.go) holds the rule) and the dropping of the recorded `Date` when a member
leaves (the next record holds the members of that build only, so it follows from `virtualSuite`).

## What this does not cover

### H-725 · A change of claims reaches the merged index at the next check, not at once

The claims of the Debian repositories are part of the inputs of a build (a digest of their revisions, `ClaimsFingerprint`), but the service
that changes claims has no hook a format can use, so no build is enqueued by the change. The periodic task `deb-index-refresh` enqueues a build
for a virtual repository whose last check is older than `virtual.mergedMetadataTtlSeconds` (it runs every minute), and the first build after
that sees the new revisions. Until then the merged index lists a name from a member a claim has since taken it from, or leaves out a name a
claim has given it. A download through the member path is decided by the resolution of the moment (R-REPO-019, as read in `handleMember`), which
this page did not test separately. Live today for every virtual repository; the time is the period, and a local publication or a change of the
members builds sooner.

### H-726 · The build after a local publication is enqueued after the commit that published, not in it

`triggerVirtuals` runs when the publication of a local member has returned and logs a failure to enqueue as a warning. A replica that stops
between the two loses that trigger. The next check by the period (H-725) builds the virtual repository, so the delay is bounded by
`virtual.mergedMetadataTtlSeconds`, not by `deb.virtual_index_interval`. Dormant until a replica stops in that window; real for a deployment
that sets a long period.

### H-727 · A member whose release has no Valid-Until can be held at its last verified copy for as long as the upstream is unreachable or unverifiable

The remote engine stores a newer release only when a current key verifies it and keeps serving the last verified one otherwise, and a build
that cannot ask the upstream merges that copy (R-DEB-026). Many archives set no `Valid-Until`; for them nothing bounds the age of the copy, and
an attacker who can block the upstream or replace its answers with documents no key verifies freezes the member without any failure showing in
the state of the virtual repository. The virtual repository's own signature date does not advance while the inputs are unchanged. Live for a
member whose archive sets no `Valid-Until`; the operator sees it in the member's release `Date`, which no screen shows next to the age.

### H-728 · A deployer of a local member adds a suite to every virtual repository that lists it

The suites a virtual repository serves are those of `deb.suites` and those of its local members (R-DEB-025). A principal with `deploy` on a
member who uploads to a new suite makes the next build create that suite in each virtual repository that lists the member, signed with the
virtual repository's key and readable by everyone who may read the virtual repository and its members. The suite lists that member's packages
and what the other members' archives have for the same suite name. Live whenever a member's deployers are not the virtual repository's
operators. No setting closes the list; the deploy grants on the member are the control.
