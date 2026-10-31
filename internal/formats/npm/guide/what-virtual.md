A virtual repository is one address for several npm repositories, its members, in an order an administrator sets. Pointing your client at a virtual repository is the recommended setup: internal and public packages come through one address and one credential.

### How the members answer

- **Package documents are merged.** Every member that knows the package contributes its versions. Each version's entry comes from the member that would serve its tarball: the local members in their order first, then the remote members that hold the tarball in their cache, then the remote members in their order. A version that a local and a remote member both hold with different bytes therefore comes from the local member.
- **Dist-tags are merged too.** `latest` is the highest of the members' own `latest` tags, releases before pre-releases, and every other tag comes from the first member that has it. An administrator can choose other rules (`latestStrategy`, `distTagStrategy`).
- **Tarballs** come from the member whose entry the document carries, so the bytes always match the integrity your client sees.
- **You need read on the members as well.** A member you cannot read is left out without a word; a token that may read only the virtual repository and none of its members gets `404` for every package.

A publish to a local member is visible through the virtual repository at once. A change upstream shows up within the remote member's metadata period: the merged result is cached for the virtual repository's own period (two hours unless it sets another), but never longer than the remote documents it was built from.

### Writes go to the deployment target

Publish, dist-tag changes and unpublish through a virtual repository go to its deployment target, a local member, with that repository's permissions and rules.{{if .PublishTarget}} For `{{.Repository}}` that is `{{.PublishTarget}}`.{{else if .Repository}} You cannot publish through `{{.Repository}}`: it has no deployment target, or you do not hold the deploy permission on it.{{end}} A virtual repository without a deployment target refuses every write (`403`, or `405` with `no_deploy_target`).

### Protection against dependency confusion

A public package that carries the name of an internal one, usually with a higher version, must never win. trove decides this on the server:

- **Namespace claims.** The first publish of a name to a local repository claims that name (automatic claims are on by default), and an administrator can claim a whole scope such as `@acme/*` before anything is published in it. A claimed name resolves only from the claiming local member: the remote members are not asked, so neither a higher public version nor a missing internal one lets an upstream package in, and the answer is `404` when the local member lacks the version. A claim for the whole installation also makes every remote repository refuse the name.
- **Pin rules** of the virtual repository name, per package pattern, the members that may serve it.
- **The shadowing check.** The first publish of a name that a remote member already serves would replace the public package for every client of the virtual repositories that contain the local repository. trove refuses it with `403` `shadows_remote_package` unless the publisher holds the manage permission on the package; when it cannot ask the upstream, the answer is `503` `shadow_check_unavailable`, nothing is stored, and you retry later. Later versions of a name the local repository holds already are never checked.
