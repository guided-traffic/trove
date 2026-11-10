A local repository stores the collections that you publish to it. `ansible-galaxy collection install` reads them through the Galaxy API that this repository speaks, and the archive you install is the archive that was published, byte for byte.

- A published version is immutable. Publishing the same file again succeeds and changes nothing; publishing different bytes under an existing version is refused with `version_exists`.
- A version that was deleted leaves a tombstone: its number cannot be published again, so that nobody who has already seen the version meets other bytes under it.
- Collection names are lower case, `namespace.name`, and a version is a semantic version such as `1.2.3` or `1.0.0-rc.1`. The archive names its own coordinates, so a publish needs no extra option.
- A local repository serves **collections only**. It does not answer the role API, so `ansible-galaxy role install` against it alone fails with the client's own message that the v1 API is not available. To install roles through trove as well, list a virtual repository that has a remote Galaxy among its members, or wrap the role into a collection (the `roles/` directory of a collection).

The address of {{if .Repository}}`{{.Repository}}`{{else}}a repository{{end}} is `{{.RegistryURL}}`.
