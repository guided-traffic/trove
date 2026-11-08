A package file is uploaded with one request: `PUT` to `upload/<file>` with the `.deb` as the body, and the suite in the query. The `distribution` parameter is required; the `component` defaults to `main`; both may repeat or hold comma-separated values (at most 16 of each), and the file is listed in every combination. The file name must end in `.deb`; trove names the stored file itself, under the pool directory of the source package.

The answer is `201` for a new file and `200` when the identical bytes were stored already, in which case the coordinates of the request were added. The body names the stored path, the package, the version, the architecture, the digest, the coordinates and, per suite, the `changeSeq` and the `publishedSeq`. A client that needs the package in the index reads the index state of the repository with `waitFor=<suite>:<changeSeq>`.

| Answer | Cause |
|---|---|
| `400` `deb_distribution_required` | The request names no `distribution`. |
| `400` `deb_invalid_coordinate` | A suite or component is outside its grammar. |
| `409` `deb_version_exists` | Other bytes are stored for this package, version and architecture; delete the file first, or upload the changed package under a new version. |
| `409` `deb_filename_conflict` | Another version of the package has the same file name, because the versions differ only by their epoch. |
| `409` `deb_withheld` | The bytes were deleted before a restore and are withheld until an administrator reinstates them. |
| `422` `deb_invalid_archive`, `deb_invalid_control`, `deb_unsupported_compression` | The file is no Debian package, its control data is not accepted (the answer names the field), or its control archive is compressed with a method trove does not read. |
{{if .PublishTarget}}
Uploads through this repository are stored in `{{.PublishTarget}}`.
{{end}}
