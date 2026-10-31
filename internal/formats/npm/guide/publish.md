{{if not .Anonymous}}{{if or .PublishTarget (not .Repository)}}{{if eq .Client "npm"}}`npm publish` in the package directory publishes to the registry of your configuration. To publish to {{if .Repository}}`{{.Repository}}`{{else}}the repository{{end}} whatever the publisher's configuration says, name it in `package.json`:

```json
{ "publishConfig": { "registry": "{{.RegistryURL}}" } }
```
{{else if eq .Client "pnpm"}}`pnpm publish` in the package directory publishes to the registry of your configuration, or to the `publishConfig.registry` of `package.json`. pnpm refuses to publish from a working tree with uncommitted changes, or from a branch other than the main one, unless you add `--no-git-checks`. pnpm 10 hands the upload to npm, so npm must be installed next to it.
{{else if eq .Client "yarn"}}`yarn npm publish` in the package directory publishes to `npmPublishRegistry` when `.yarnrc.yml` sets it, else to `npmRegistryServer`. Yarn wants the package in its own lockfile first: run `yarn install` in the package directory once before you publish.
{{else}}`yarn publish --new-version <version>` in the package directory publishes to the registry of `.npmrc`; without `--new-version` it asks for the version, and `--no-git-tag-version` keeps it from committing and tagging in git.
{{end}}
A scoped package goes to the registry of its scope when your configuration binds one.{{if eq .Class "virtual"}} Through a virtual repository the publish reaches its deployment target{{if .PublishTarget}}, `{{.PublishTarget}}`{{end}}, with that repository's permissions, patterns and owners.{{end}}

### Limits and retries

A tarball may hold at most 256 MiB, unpack to at most 2 GiB in at most 100,000 files, with a `package.json` of at most 1 MiB (the defaults; an installation can change the first three). A larger publish answers `413` `npm_too_large`.

When the connection breaks after trove stored the version, npm sends the publish again. trove recognises an identical retry by the same publisher within ten minutes (the default), changes nothing and answers success with the notice `<name>@<version> was already published by you at <time>; nothing changed`.

`npm publish --provenance` needs `--access public`: trove stores the provenance bundle with the version, and serves no attestations yet. Staged publishing (`npm publish --stage`) answers `501`.
{{else}}You cannot publish through `{{.Repository}}`: {{if eq .Class "virtual"}}it has no deployment target, or you do not hold the deploy permission on it{{else}}you do not hold the deploy permission on it{{end}}. Ask whoever manages the repository for the permission, or publish to another repository.
{{end}}{{end}}