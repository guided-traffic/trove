A virtual repository puts local and remote Galaxy repositories behind one address. It is the address to give your clients: one `server_list` entry reads your own collections and the public ones.

- The version list of a collection is the union of the versions of the members, newest first. When two members hold the same version, the member that comes first wins, and a local member's version never loses to a remote's.
- Claims and pin rules of the repository keep a name away from the members that must not serve it. They are decided on the server, before anything is merged, so a public collection cannot slip in under the name of one of yours.
- If a remote member cannot be asked, the merged answer fails with `503` instead of showing a list that is silently incomplete.
- Roles are answered by the first remote member that knows the role. A virtual repository over local members alone serves no roles.
- A publish through the virtual repository is forwarded to its deploy target, which must be a local repository.{{if .PublishTarget}} For you that is `{{.PublishTarget}}`.{{end}}

The address of {{if .Repository}}`{{.Repository}}`{{else}}a repository{{end}} is `{{.RegistryURL}}`.
