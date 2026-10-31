A local repository holds the packages your teams publish{{if .Repository}} to `{{.Repository}}`{{end}}. trove keeps every published tarball and renders the package documents from its own records, so a version is visible on every replica as soon as its publish has returned.

### Published versions never change

A name and version are accepted once. Publishing the same version again answers `409` with `npm_version_exists`, whoever publishes it and whatever the tarball holds; only a client's retry of a publish whose answer was lost is recognised, and changes nothing. That is what keeps the integrity values of a lockfile valid: the bytes of a version in this repository never change. Publish a new version instead.

### Unpublish keeps the version number blocked

Unpublishing deletes the version for good and leaves a tombstone: the same version number answers `409` with `npm_version_unpublished` from then on, for every publisher. No time window applies. Unpublishing needs the delete permission; the section on dist-tags and unpublish has the commands.

### latest does not move backwards

A publish under the dist-tag `latest` of a version lower than the current `latest`, or of a pre-release while `latest` is a release, stores the version and leaves `latest` where it is (the repository setting `latestGuard`, on by default). npm prints the notice `latest stays at <version>; move it with npm dist-tag add`; move the tag yourself if you meant it.

### Who may publish a name

The first publish of a name records its owner: the publisher, or the owner the publisher's permission names. From then on only owners and holders of the manage permission publish, tag or unpublish the package; anybody else gets `403` with `package_owned_by_other_owner` until an owner adds them. A repository can turn package ownership off.

New names follow npm's rules for new packages: lower case, URL-safe, at most 214 characters, and neither the scope nor the name starts with a dot, an underscore or a hyphen. A new name that differs from a name the repository holds only in `-`, `_` or `.` is refused with `400` `npm_invalid_name`, because `acme.ui` next to `acme-ui` invites mistakes.

### What trove checks in a publish

The manifest your client sends must agree with the `package.json` inside the tarball on the dependencies, binaries, engines, operating systems, CPUs and install scripts (`400` `npm_manifest_mismatch`), and the tarball must match the integrity the request announces (`409` `conflict`). Your client takes care of both; a request built by hand may not.

`npm deprecate` is not available yet and fails with `E404` without changing anything.
