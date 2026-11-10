A remote repository is a cache in front of a Galaxy-compatible server such as the public Galaxy. The first request for a document or an archive goes to the upstream; later requests are answered from trove, however many clients ask.

- trove verifies the SHA-256 that the upstream announces for every collection archive against the bytes it fetched, whatever the integrity policy says. An archive that does not match is refused, and the client reports its own `Mismatch artifact hash`.
- Signatures of a collection version pass through unchanged. trove neither signs nor verifies them; a client that has a keyring (`--keyring`) verifies them itself.
- A remote serves roles by translation: the role lookup, the role versions and the role archive. The archive of a role is fetched from the hosts the operator allowed (by default GitHub) and stored without a digest, because the upstream announces none. Roles with a `git+https` or a URL source are not seen by trove: the client fetches them on its own.
- A remote is read-only. Publishing to it is answered with `405`.
- A repository in mirror mode keeps the collections its patterns select, so that they stay installable while the upstream is away.

The address of {{if .Repository}}`{{.Repository}}`{{else}}a repository{{end}} is `{{.RegistryURL}}`.
