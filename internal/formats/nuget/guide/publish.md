{{if not .Anonymous}}{{if or .PublishTarget (not .Repository)}}{{if eq .Client "dotnet"}}`dotnet pack` builds the `.nupkg` and `dotnet nuget push` sends it. The token travels as the API key (`--api-key`); the source is the address of the repository, so the push goes there whatever your `nuget.config` says. The credentials of the `nuget.config` are not needed for a push, but they do no harm: the client tries the API key first and answers the challenge of trove with them if the key is no token.{{else}}`nuget pack` builds the `.nupkg` and `nuget push` sends it. The token travels as the API key (`-ApiKey`); the source is the address of the repository, so the push goes there whatever your `nuget.config` says.{{end}}{{if eq .Class "virtual"}} Through a virtual repository the push reaches its deployment target{{if .PublishTarget}}, `{{.PublishTarget}}`{{end}}, and answers exactly as that repository would, with its permissions, patterns and owners.{{end}}

A package pushed with the API key of a CI job needs the deploy permission on the repository, on the ID and, once the ID has owners, ownership. The first push of an ID that a remote member of a virtual repository already serves needs the manage permission on the ID; trove refuses it with `403` `shadows_remote_package` otherwise, so that an internal package never replaces a public one without somebody deciding it.

### Retries

A push is safe to repeat. When the connection breaks after trove stored the version, the client sends it again, and trove answers success for the identical file and changes nothing. A push of a version that exists with other bytes answers `409` `nuget_version_exists`. `--skip-duplicate` (`-SkipDuplicate` for nuget.exe) turns that `409` into a skipped package in a push of several, and skips a version whose number was deleted for good as well.

### Failed pushes print a status and no body

`dotnet nuget push` and `nuget push` report a failure by its status code and reason phrase (`403 (Forbidden)`, `409 (Conflict)`), because a failed push prints no response body. The code of the answer is in the body that `curl` shows, and the troubleshooting section lists each code with its status. The push itself is a `PUT` of the `.nupkg` as `multipart/form-data` to `{{.RegistryURL}}v3/package` with the header `X-NuGet-ApiKey`; `curl -X PUT -H "X-NuGet-ApiKey: ${TROVE_TOKEN}" -F package=@Acme.Widgets.1.2.3.nupkg {{.RegistryURL}}v3/package` shows the answer in full.

### Limits

A package is limited by the upload size of the installation and by the limits an administrator set for the archive: the number of entries, the unpacked size and the size of the `.nuspec`. A larger push answers `413` `payload_too_large`, a package that breaks an archive rule `400` `nuget_invalid_package`. Pushing a symbol package with the package resource is refused with `400`.
{{else}}You cannot push through `{{.Repository}}`: {{if eq .Class "virtual"}}it has no deployment target, or you do not hold the deploy permission on it{{else}}you do not hold the deploy permission on it{{end}}. Ask whoever manages the repository for the permission, or push to another repository.
{{end}}{{end}}
