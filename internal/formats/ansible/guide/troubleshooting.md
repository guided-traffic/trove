| What you see | Why, and what to do |
|---|---|
| `HTTP Error 401` at the first request, naming the server | The repository needs a credential and the client sent none or a wrong one. Check that `ANSIBLE_GALAXY_SERVER_TROVE_TOKEN` is set in the shell that runs the command, and that the server in `ansible.cfg` is named `trove`; the variable is read for that name only. |
| `HTTP Error 403` or `permission_denied` on a publish | The token may read but not deploy to the repository, or the namespace belongs to another publisher. Ask for the deploy permission on the repository. |
| `Mismatch artifact hash` | The archive the client downloaded does not match the SHA-256 in the version document. On a remote repository the upstream changed a version after it was announced; trove refuses to serve the changed bytes. Report it to the operator; do not work around it. |
| `Galaxy import process failed` with a description | The server refused the archive: not a collection archive, a name or version that breaks the rules, or a manifest that disagrees with the file name. The description names the reason. |
| `409 version_exists` | That version is published with other bytes. Publish a new version number. |
| `409 version_tombstoned` | That version was deleted. A number is never reused until an administrator clears the tombstone. |
| `The v1 API is not available` or a role that is not found | A role install reached a repository that serves no roles, which a local repository never does. Put a virtual repository with a remote Galaxy member into `server_list`. |
| A role downloads its metadata and then fails with `401` | The role archive is fetched without the token header. Add the `~/.netrc` line of the configure snippet for this host. |
| `Collection ... not found` on a server that should have it | A claim or pin rule of the repository kept the name away from this member, or an earlier server of `server_list` answered the question. Ask the servers in order with `-s <name>`. |
| `503 upstream_unavailable` | A remote member could not be asked and the merged answer would have been incomplete. Try again; the answer is complete once the upstream is back. |
