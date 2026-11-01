A virtual repository is one address for several NuGet repositories, its members, in an order an administrator sets. Using one virtual repository as the only source of a solution is the recommended setup: internal and public packages come through one address and one credential, and nothing else can slip in.

### How the members answer

- **Version lists and registrations are merged.** Every member that knows the ID contributes its versions. The registration entry of a version comes from the member whose `.nupkg` the download will come from: the local members in their order first, then the remote members that hold the package in their cache, then the remote members in their order. A version that a local and a remote member both hold with different bytes therefore comes from the local member, and the entry a client reads always describes the bytes it downloads.
- **Search is merged.** Every member answers the query. Packages whose ID equals a search term come first, then the results member by member in the order of the members; an ID that several members hold appears once, with the union of its versions.
- **Packages** come from the member whose registration entry the client read.
- **You need read on the members as well.** A member you cannot read is left out without a word; a token that may read only the virtual repository and none of its members sees nothing.

A push to a local member is visible through the virtual repository at once, on every replica.

### Writes go to the deployment target

Push, unlist, relist and delete through a virtual repository go to its deployment target, a local member, and answer as that repository would, with its permissions and rules.{{if .PublishTarget}} For `{{.Repository}}` that is `{{.PublishTarget}}`.{{else if .Repository}} You cannot push through `{{.Repository}}`: it has no deployment target, or you do not hold the deploy permission on it.{{end}} A virtual repository without a deployment target lists no publish resource in its service index and answers every write with `405` `no_deploy_target`.

### Protection against dependency confusion

A public package that carries the ID of an internal one, usually with a higher version, must never win. trove decides this on the server:

- **Namespace claims.** The first push of an ID to a local repository claims it (automatic claims are on by default), and an administrator can claim a whole prefix such as `Acme.*` before anything is pushed. A claimed ID resolves only from the claiming local member: the remote members are not asked, so neither a higher public version nor a missing internal one lets an upstream package in, and the answer is `404` when the local member lacks the version.
- **Pin rules** of the virtual repository name, per ID pattern, the members that may serve it.
- **The shadowing check.** The first push of an ID that a remote member already serves would replace the public package for every client of the virtual repositories that contain the local repository. trove refuses it with `403` `shadows_remote_package` unless the publisher holds the manage permission on the ID; when it cannot ask the upstream, the answer is `503` `shadow_check_unavailable`, nothing is stored, and you retry later.

Your client adds one more layer: `<clear />` in `nuget.config` removes every other source, and package source mapping decides which source may serve which ID prefix. The section on configuring the client has both.
