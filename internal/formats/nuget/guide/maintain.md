{{if not .Anonymous}}### Unlist or delete a version

{{if eq .Client "dotnet"}}`dotnet nuget delete <id> <version>` with `--source`, `--api-key` and `--non-interactive` removes a version from the view of the repository:

```shell
dotnet nuget delete Acme.Widgets 1.2.3 --source {{.RegistryURL}}v3/index.json --api-key "${TROVE_TOKEN}" --non-interactive
```
{{else}}`nuget delete <id> <version>` with `-Source`, `-ApiKey` and `-NonInteractive` removes a version from the view of the repository:

```shell
nuget delete Acme.Widgets 1.2.3 -Source {{.RegistryURL}}v3/index.json -ApiKey "${TROVE_TOKEN}" -NonInteractive
```
{{end}}
What that command does depends on the repository's `deleteMode`, which its settings page shows. With `unlist`, the default, the version is only unlisted: search and the Browse tab no longer offer it, it stays in the version list and downloadable, a project that names it keeps restoring, and the registration entry says `"listed": false`. With `delete` the version is deleted for good, its `.nupkg` answers `404`, and its number is blocked for ever (`409` `nuget_version_deleted` on a push). Deleting through the management API, the UI or a file delete always deletes for good. Unlisting a version that is unlisted already, and deleting one that was deleted, answers success again, so a repeated command does not fail.

### Relist a version

NuGet clients have no command that lists a version again. Send the request yourself, with the deploy permission:

```shell
curl -X POST -H "X-NuGet-ApiKey: ${TROVE_TOKEN}" {{.RegistryURL}}v3/package/acme.widgets/1.2.3
```

It answers `200`, also when the version is listed already, and `404` for a version that was deleted for good. Relisting changes which version a client picks for a range, so it needs the same permission and ownership as a push.

Unlisting needs the delete permission, relisting the deploy permission, both on the ID and, where the ID has owners, ownership.{{if eq .Class "virtual"}} Through a virtual repository the request goes to the deployment target{{if .PublishTarget}}, `{{.PublishTarget}}`{{end}}, and no other member is touched.{{end}}
{{end}}
