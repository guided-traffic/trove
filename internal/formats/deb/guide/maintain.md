### Delete a package file

`DELETE` of the pool path of a file removes it: the next index no longer lists it and `apt-get install hello=2.10-3` reports that the version was not found. The deletion is recorded for the audit trail and for a restore: a file deleted after a backup stays withheld when that backup is restored, until an administrator puts it back with `trove admin deb reinstate <project>/<repository> <path>`.

### Move a package between suites

`PUT …/deb/coordinates` replaces the suites and components a file is listed in. Adding a coordinate needs the permission to deploy, removing one the permission to delete, both on the package. A removed coordinate is recorded like a deletion, because pulling a package out of a suite narrows what clients receive.

### Signing keys

The key of the repository is rotated with a next key that signs beside the first one while clients replace their key files, then promoted; see the key list of the repository. A client that never replaced its key file fails `apt-get update` after the promotion, which is what APT is built to do.
