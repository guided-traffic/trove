Build the archive with `ansible-galaxy collection build` and publish it with `ansible-galaxy collection publish`. The collection's own `galaxy.yml` names the namespace, the name and the version, so the command needs no coordinates: the file name and the manifest inside the archive have to agree, and the namespace and name have to be lower case.

The command prints the import messages of the server and exits with an error when the import failed; the repository then holds no version. A publish always needs a token that may deploy to the repository.{{if eq .Class "virtual"}} A virtual repository forwards the publish to its deploy target, which has to be a local repository; without one the answer is `405 no_deploy_target`. The import task is read through the address you published to.{{end}}

- Publishing the same file again succeeds and installs identical bytes. Different bytes under an existing version are refused with `409 version_exists`.
- A version number that was deleted cannot be published again (`409 version_tombstoned`) until an administrator clears its tombstone.
- A name that a remote member of a virtual repository already serves is refused for a first publish unless you may manage the repository, so that a public collection cannot be shadowed by accident.
- The upload dialog of the web interface and `curl -T` publish through the `/files/` route with the same checks and without the task.

The archive is read once, in the request, within the limits of the installation. An archive that is not a collection archive fails the import with the reason, and `ansible-galaxy` prints it.
