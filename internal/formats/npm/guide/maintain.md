{{if not .Anonymous}}### Dist-tags

{{if eq .Client "npm"}}`npm dist-tag add <name>@<version> <tag>` sets a tag, `npm dist-tag ls <name>` lists the tags, `npm dist-tag rm <name> <tag>` removes one.{{else if eq .Client "pnpm"}}`pnpm dist-tag add <name>@<version> <tag>` sets a tag, `pnpm dist-tag ls <name>` lists the tags, `pnpm dist-tag rm <name> <tag>` removes one.{{else if eq .Client "yarn"}}`yarn npm tag add <name>@<version> <tag>` sets a tag, `yarn npm tag list <name>` lists the tags, `yarn npm tag remove <name> <tag>` removes one.{{else}}`yarn tag add <name>@<version> <tag>` sets a tag, `yarn tag list <name>` lists the tags, `yarn tag remove <name> <tag>` removes one.{{end}}

Changing a tag needs the deploy permission and, where the package has owners, ownership. `latest` can point to any published version, an older one too, but cannot be removed (`400` `npm_invalid_tag`); a tag named like a version range, such as `1.x`, is refused the same way. A package always has `latest`: when the version it points to is unpublished, `latest` moves to the highest remaining release, and every other tag of that version is removed.

### Unpublish

{{if eq .Client "npm"}}`npm unpublish <name>@<version>` removes one version; `npm unpublish <name> --force` removes the whole package.{{else if eq .Client "pnpm"}}`pnpm unpublish <name>@<version> --force` removes one version.{{else}}Yarn has no unpublish command; use npm or pnpm with the same repository.{{end}} Unpublishing needs the delete permission (deploy alone answers `E403`) and, where the package has owners, ownership. The version is deleted for good and its number never takes other bytes again (`409` `npm_version_unpublished`); no time window applies.{{if and (eq .Class "virtual") (eq .Client "pnpm")}}

pnpm 11 and 12 cannot unpublish through a virtual repository: they send back the revision of the merged document, which trove refuses with `409` `npm_stale_rev`, and retry for about 70 seconds. Unpublish at the deployment target{{if .PublishTarget}}, `{{.PublishTarget}}`,{{end}} instead; pnpm 10 unpublishes through the virtual repository, as npm does.{{end}}

`npm deprecate` is not available yet: it fails with `E404` and changes nothing.
{{end}}