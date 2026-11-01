A local repository holds the packages your teams push{{if .Repository}} to `{{.Repository}}`{{end}}. trove keeps every `.nupkg` exactly as it was pushed and renders the version list, the registration documents and the search results from its own records, so a version is visible on every replica as soon as its push has returned.

### Pushed versions never change

A package ID and version are accepted once. Pushing the same version again with other bytes answers `409` `nuget_version_exists`, whoever pushes it and whatever permissions they hold. Pushing the very same file again answers success and changes nothing: that is what makes a push safe to repeat when the connection broke after trove stored the version, and why `dotnet nuget push` and `nuget push` can retry on their own. Push a new version instead of replacing one. That keeps the hashes of lock files and client caches valid.

### A deleted version number stays blocked

Deleting a version for good leaves a tombstone: pushing that version number again answers `409` `nuget_version_deleted`, even with the same bytes. Unlisting a version leaves no tombstone and is undone by relisting it; the section on maintaining versions explains the difference.

### Who may push an ID

The first push of an ID records its owner. From then on only owners and holders of the manage permission push, unlist or delete versions of that ID; anybody else gets `403` with `package_owned_by_other_owner` until an owner adds them. The ID is the one inside the package, not the file name. A repository can turn package ownership off.

IDs in a local repository follow trove's stricter rule: ASCII letters, digits and `_`, in segments separated by a single `.` or `-`, at most 100 characters. IDs are not case sensitive, and the casing of the first push is the display name.

### What trove checks in a push

The package must be a valid ZIP archive with exactly one `.nuspec` at its root, within the limits an administrator set for entries and unpacked size; a package that breaks a rule answers `400` `nuget_invalid_package` and the message names the rule. A symbol package (`.snupkg`) does not go to the package resource. Package signatures stay inside the file and are not verified.
