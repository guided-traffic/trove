# AI agent assets and IDE extension formats

This page catalogs the formats and registries for assets that AI coding agents and IDEs consume:
agent skills (`SKILL.md` bundles, compatible with the ClawHub skill registry protocol), agent
plugins (zip bundles indexed into agent plugin marketplaces), agent packages for Microsoft's
[Agent Package Manager (APM)](https://microsoft.github.io/apm/), pull-through proxies for VS Code
compatible extension galleries and the JetBrains plugin marketplace, a registry that governs which
[Model Context Protocol (MCP)](https://modelcontextprotocol.io/) servers and tools agents may use,
and the governance layer on top (scan gates, policies, waivers, project-scoped registries). The
skill, plugin and agent package formats are beta. Repository classes in
general are described in [../repositories.md](../repositories.md); scanning and upstream curation
in general in [../security-scanning-and-curation.md](../security-scanning-and-curation.md).

Throughout this page, `https://repo.example.com/<format>/<repo-key>` stands for the base URL of one
repository for a given format (the path layout is the implementation's choice), `skills-local`,
`plugins-local`, `apm-local`, `vscode-remote` and `jetbrains-remote` are example repository keys,
and `${TOKEN}` is an access token. Paths quoted from third-party protocols (ClawHub, APM, Git
hosts, IDE configuration) are given relative to such a base URL.

## Agent skills

### PKG-AI-001 · Skills repositories
Priority: **P2**

A skill is a versioned bundle of agent instructions: a `SKILL.md` file with YAML frontmatter plus
supporting files (prompts, scripts, assets). A skills repository is a private skill registry that
speaks the ClawHub v1 registry protocol, so ClawHub-compatible clients and plain HTTP scripts can
publish, discover and install skills.

- Classes: local only; there are no remote or virtual skills repositories (REPO-006 in
  [../repositories.md](../repositories.md)).
- The server hands out bundles and their metadata and nothing more; resolving dependencies
  between skills is left to the client.
- Skills appear in the package list with metadata extracted from `SKILL.md` (ART-048 in
  [../artifacts.md](../artifacts.md)). The package list is for browsing; it does not replace the
  client's version resolution.

Creation: system administrator or project administrator; UI location: administration,
repositories, create repository, local, skills format. A repository can also be created
automatically on first publish to a project (PKG-AI-029).

### PKG-AI-002 · Skill package identity and metadata extraction
Priority: **P2**

Every stored zip at the canonical path is one skill version:

```text
<slug>/<version>/<slug>-<version>.zip
my-skill/1.0.0/my-skill-1.0.0.zip   # example
```

- The slug and version are taken from the storage path.
- Slug rule: `^[a-z0-9][a-z0-9-]*$`, so only lowercase ASCII letters, digits and `-`, and the
  first character may not be a hyphen. The version is a semantic version (`1.0.0`, not `v1`).
- If the zip holds a `SKILL.md` anywhere in its tree, the system reads that file's YAML
  frontmatter (delimited by the first two `---` lines). Besides top-level keys it understands the
  metadata blocks `metadata.skills`, `metadata.clawdbot` and `metadata.clawdis` used by
  ClawHub-style skills.
- Extracted values: name, version, description, author, tags, display name, summary, changelog,
  and a fingerprint of the bundle, among others. They are stored as item properties in a
  dedicated property namespace (ART-021 in [../artifacts.md](../artifacts.md)) and feed search and
  the package list.
- Frontmatter fields required for publishing: `slug`, `version` and `description`. Open: either
  `description` is required or only recommended; the spec decides.

| Limit | Value |
|---|---|
| Maximum `SKILL.md` size that is parsed | 5 MB; larger files are stored but not parsed |

Edge cases: a zip without `SKILL.md` is still stored, with incomplete metadata. What exactly the
fingerprint hashes (the zip, or a canonical file list) is unspecified; it is a 64-character
lowercase hex SHA-256.

### PKG-AI-003 · Skill registry base URL and discovery document
Priority: **P2**

Each skills repository has a registry base URL that clients are configured with. Clients
authenticate with user name and password or token embedded in the URL (HTTP Basic), with an access
token, or through a `.netrc` entry; how a bare token is sent is unspecified.

| Operation | Inputs | Output | Required privilege |
|---|---|---|---|
| Discovery document | none | The ClawHub well-known discovery document (`.well-known/clawhub.json` below the base URL) for client auto-discovery | none: the only anonymous skills endpoint |

Every other skills operation, listing included, needs credentials. The content of the discovery
document is otherwise unspecified; it follows the ClawHub protocol.

### PKG-AI-004 · Publish a skill through the registry protocol
Priority: **P2**

| Operation | Inputs | Output | Required privilege |
|---|---|---|---|
| Publish skill | `multipart/form-data` with a `payload` part (JSON with `slug` and `version`, required; display name, summary, changelog and tags, optional) and one or more `files[]` parts | The server bundles the files into a zip and stores it at the canonical path (PKG-AI-002) | Deploy on the repository |

```bash
curl -fsS -u "<user>:${TOKEN}" \
  -F 'payload={"slug":"my-skill","version":"1.0.0"}' \
  -F 'files[]=@SKILL.md' -F 'files[]=@src/action.js' \
  "https://repo.example.com/skills/skills-local/api/v1/skills"   # example
```

- Include a file named `SKILL.md`, or the package list and search lack metadata.
- An invalid slug is rejected.

Open: publishing a version that already exists either is rejected, because published versions are
immutable and users increment the version, or overwrites it, as the companion CLI's "version
collision" prompt and the plugins' overwrite permission (PKG-AI-010) suggest; the spec decides.

### PKG-AI-005 · Download, resolve and single-file operations for skills
Priority: **P2**

| Operation | Inputs | Output | Required privilege |
|---|---|---|---|
| Download by slug and version | `slug`, `version` | The skill zip | Read |
| Resolve by fingerprint | `slug`, `hash` (64 lowercase hex characters, SHA-256) | JSON with the resolved version information | Read |
| Get a single file | Slug, `version`, `path` inside the bundle | The file, for example `SKILL.md` | Read |

<details>
<summary>ClawHub v1 paths used by these operations (relative to the registry base URL)</summary>

| Operation | Method and path |
|---|---|
| Publish | `POST api/v1/skills` |
| Download | `GET api/v1/download?slug=<slug>&version=<version>` |
| Resolve | `GET api/v1/resolve?slug=<slug>&hash=<sha256>` |
| Single file | `GET api/v1/skills/<slug>/file?version=<version>&path=<path>` |
| List skills | `GET api/v1/skills?limit=<n>&cursor=<c>&sort=updated` |
| Skill detail | `GET api/v1/skills/<slug>` |
| List versions | `GET api/v1/skills/<slug>/versions` |
| Version detail | `GET api/v1/skills/<slug>/versions/<version>` |
| Search | `GET api/v1/search?q=<query>&limit=<n>&offset=<n>` |

</details>

### PKG-AI-006 · List, detail, version and search operations for skills
Priority: **P2**

| Operation | Inputs | Output | Required privilege |
|---|---|---|---|
| List skills | Page size, cursor, sort order | Page of skills (slug, display name, summary, last update, number of versions), a next-page cursor and a total | Read |
| Skill detail | Slug | Slug, display name, summary, tags, number of versions, latest version, last update | Read |
| List versions | Slug | Per version: version, fingerprint, publish time, changelog | Read |
| Version detail | Slug, version | Version metadata plus a download URL | Read |
| Search | Query text, limit, offset | Matching skills (slug, version, display name, summary, which field matched), total, limit, offset | Read |

| Parameter | Meaning | Default |
|---|---|---|
| List sort order | Sort key of the listing | `updated` |
| List page size | Items per page | unspecified (200 in examples) |
| Search limit | Results per page; clamped to 1 to 100 | unspecified (20 in examples) |

The companion CLI also lists a repository sorted by number of downloads, so the system tracks
download counts per skill (ART-029 in [../artifacts.md](../artifacts.md)); whether the registry
listing accepts that sort key is unspecified.

### PKG-AI-007 · Scan gate for skills
Priority: **Later**

After a skill is published, the system scans it for malicious content (including an AI-based
"semantic" analysis) and for known vulnerabilities in its components (SEC-087 and SEC-043 in
[../security-scanning-and-curation.md](../security-scanning-and-curation.md)). The scan status
decides whether a skill is installable.

| Status | Meaning | Installable |
|---|---|---|
| Not malicious | Scanned, no issues | yes |
| Not scanned | No completed scan: scanning not applied to the repository, scan pending, scan limit reached, or scanning not activated | on a repository with scanning: not yet; on a repository without scanning: yes, indefinitely |
| Scanning | Scan in progress | no |
| Suspicious | Patterns that warrant review | no |
| Malicious | Malicious code or intent confirmed | no |
| Blocked | An administrator blocked the skill; affects every project sharing the repository | no |

| Operation | Inputs | Output | Required privilege |
|---|---|---|---|
| Get scan gate status | Repository key, URL-encoded in-repository path of the skill zip | Status, repository key, path. Status values: scanning not available for the installation (callers may skip polling); scanning disabled for this repository; scan in progress (poll again); approved; blocked | Read |

Client behavior built on this operation (companion CLI):

| Client option | Meaning | Default |
|---|---|---|
| Skip scan | Do not wait for the scan after publishing | off |
| Scan polling timeout | How long the client polls for a terminal status | 5 minutes |
| Delete when malicious | Delete the uploaded zip automatically when the scan flags it as malicious | off |

- Scanning must be enabled for the skills repository (REPO-021 in
  [../repositories.md](../repositories.md)); repositories created automatically for a project
  (PKG-AI-029) have it enabled.
- Scans usually finish within minutes; a "not scanned" status that persists on a repository with
  scanning points to indexing not actually being enabled.
- The same post-publish gate is not run by the CLI for plugins; plugin scanning is described in
  PKG-AI-036.

Interactions: blocking downloads of not-yet-scanned artifacts (SEC-062), scan status in general
(SEC-047), policies on top of the scan (PKG-AI-030).

### PKG-AI-008 · Signed evidence for skills and plugins
Priority: **Later**

- A publisher can attach a signed evidence attestation to a published skill or plugin version,
  signed with a PGP private key and identified by a key alias (DIST-044 to DIST-047 in
  [../distribution-and-lifecycle.md](../distribution-and-lifecycle.md)).
- On install, the client verifies the evidence with public keys stored in the system (IAM-046 in
  [../identity-and-access.md](../identity-and-access.md)); the installer needs no local keys.
- Verification failure: an interactive client asks whether to continue; a non-interactive client
  (CI or quiet mode) fails, unless the user explicitly downgrades the failure to a warning. A
  warning about an unsigned skill is informational and does not block an install the scan status
  permits.
- For plugins, verification happens when signing keys are configured in the system.

### PKG-AI-009 · Skill installation targets and client workflow
Priority: **P2**

The companion CLI installs skills into the directories where agents look for them. The server
capabilities it relies on are listed at the end.

| Agent | Project-scoped location | Global location |
|---|---|---|
| Claude Code | `.claude/skills` | `~/.claude/skills` |
| Cursor | `.cursor/skills` | `~/.cursor/skills` |
| GitHub Copilot | `.github/skills` | `~/.copilot/skills` |
| Windsurf | `.windsurf/skills` | `~/.codeium/windsurf/skills` |
| Codex | `.codex/skills` | `~/.codex/skills` |
| Agent-neutral location | `.agents/skills` | `~/.agents/skills` |

- Install modes: project-scoped for one or more agents (below a project directory, default the
  current directory), global for one agent, or a direct path (`<dir>/<slug>`, the base directory
  must exist). Users can add or override agent locations in a local client configuration; names
  match case-insensitively.
- Version resolution: `latest` or no version means the greatest semantic version in the
  repository.
- Publish: the version comes from the `version` field of the `SKILL.md` frontmatter or an explicit
  override. When it is missing, an interactive client asks; a non-interactive client takes the next
  minor version after the latest published one, or `0.1.0` when none exists.
- Update: compares installed and target versions, skips targets already current unless forced,
  downloads once and copies to all targets, and restores the previous install from a backup when
  a target fails. A small install record per installed skill (source repository, version, agent,
  scope, project directory) drives updates and update checks.
- Update check on installed skills reports current, behind, ahead or unknown.
- Delete removes one version from the repository, without confirmation; a dry run is available.
- Repository selection: an explicit argument, then an environment variable, then auto-discovery:
  the client lists the local skills repositories; with one it uses it, with several it asks, and a
  non-interactive client fails listing the candidates.
- Search: the registry's full-text search across all discoverable skills repositories, or a
  property search on the skill name.

Server capabilities used: listing repositories by format and class (REPO-057 in
[../repositories.md](../repositories.md)), the registry operations PKG-AI-004 to PKG-AI-006,
property search (SRCH-007 in [../search-and-query.md](../search-and-query.md)), item deletion
(ART-034 in [../artifacts.md](../artifacts.md)), and build metadata (PKG-AI-017).

## Agent plugins

### PKG-AI-010 · Agent plugin repositories
Priority: **P2**

An agent plugin is a zip bundle that packages several agent extensions together: skills, commands,
sub-agents, hooks, MCP server configuration and assets, for Claude Code, Cursor, Codex and VS Code.
An agent plugin repository stores plugin zips, indexes their manifests and serves marketplace
index documents (PKG-AI-012).

- Classes: local only; proxying plugin zips through remote repositories is not supported.
- Storage path, which also defines identity:

  ```text
  <name>/<version>/<name>-<version>.zip
  test-plugin/1.1.0/test-plugin-1.1.0.zip   # example
  ```

- The system uses the folder path as plugin name and version for search and marketplace entries,
  not the values in `plugin.json`. An upload whose manifest disagrees with the path may succeed.
- Plugin names follow `^[a-z0-9][a-z0-9-]*$`.
- Uploading a name and version that already exists succeeds only with the overwrite permission;
  otherwise it fails.
- After an upload the system extracts the manifests, records which agents the plugin targets as
  item properties, and rebuilds the marketplace indexes at the repository root.
- There is no index of all versions of a plugin; marketplace indexes list only the latest version
  per plugin. Enumerating all versions needs a metadata query (EVT-075 in
  [../events-and-extensibility.md](../events-and-extensibility.md)).

Creation: system administrator or project administrator; UI location: administration,
repositories, create repository, local, agent plugins format.

### PKG-AI-011 · Plugin bundle layouts
Priority: **P2**

The system accepts two layouts, also combined in one zip.

| Layout | Manifest | Recognized for |
|---|---|---|
| Agent-specific layout | `plugin.json` inside an agent folder: `.claude-plugin/`, `.cursor-plugin/`, `.codex-plugin/`; the manifest is the only file allowed inside that folder | The agent named by each folder found |
| [Agent Plugins](https://agent-plugins.org/) open standard 1.0.0 | `plugin.json` at the plugin root with a `name` and `$schema` set to `https://agent-plugins.org/schemas/1.0.0/plugin.schema.json` ([specification](https://github.com/agentplugins/agent-plugins-spec)) | VS Code and Cursor (and the agent-neutral set) |

Bundle structure in the agent-specific layout:

```text
my-plugin/
├── .claude-plugin/plugin.json   # one folder per targeted agent
├── skills/<skill-name>/SKILL.md # YAML frontmatter (name, description) and body
├── commands/                    # Markdown command files
├── agents/                      # Markdown sub-agent files
├── hooks/hooks.json
├── .mcp.json                    # MCP server configuration
├── assets/                      # icons, logos, screenshots
└── README.md
```

A standard-layout plugin has `plugin.json`, `skills/` and `mcp.json` at its root.

Validation (whether the publishing client or the server enforces it is unspecified): `plugin.json`
needs `name`, `version` and `description`, and a bundle with nothing but its manifest is refused;
it must contain a skill, a hook, an agent or an MCP server as well.

Open: either a `.vscode-plugin/` folder is a further agent-specific layout, or VS Code has no
agent-specific index and reads only the root `plugin.json`; the spec decides.

### PKG-AI-012 · Marketplace index documents
Priority: **P2**

The system keeps index documents at the repository root, rebuilt after every upload:

| Index | Contents | Plugin source in an entry |
|---|---|---|
| Unified index | Every plugin, whatever the agent | zip |
| Claude Code index | Plugins with a `.claude-plugin/plugin.json`; Claude Code's marketplace format | `url` pointing at the plugin zip, which Claude Code downloads |
| Cursor index | Plugins with `.cursor-plugin/plugin.json` or a conforming root `plugin.json` | zip |
| Codex index | Plugins with `.codex-plugin/plugin.json` | zip |
| VS Code index | Plugins with a conforming root `plugin.json` (standard layout only) | zip |

Indexing rules:

- An agent-specific manifest puts the plugin into that agent's index and the unified index.
- A conforming root `plugin.json` puts it into the VS Code, Cursor and unified indexes.
- A zip with both is indexed into every matching index; for the Cursor index an existing
  `.cursor-plugin/plugin.json` wins over the root manifest.
- Each index lists one entry per plugin name: the latest semantic version in the repository,
  whatever layout that version uses. Older versions stay stored but unlisted. Example: if the
  Claude Code index lists a plugin at 1.0.0 and a later upload adds 2.0.0 with only
  a root `plugin.json` or only a Cursor manifest, the Cursor and unified indexes list 2.0.0. What
  the Claude Code index lists afterwards is unspecified.
- Agent-specific layout plugins for VS Code are installed from the unified index.

| Operation | Inputs | Output | Required privilege |
|---|---|---|---|
| Get an index | Repository key, index name | The index document | Read |

Limits: the index structure may change while the format is beta. Whether
deleting a version rebuilds the indexes is unspecified. The index file names are the
implementation's choice.

### PKG-AI-013 · Native marketplace connection for Claude Code
Priority: **P2**

Claude Code can use a plugin repository as a native plugin marketplace:

```bash
claude plugin marketplace add "https://<user>:${TOKEN}@repo.example.com/plugins/plugins-local/<claude-code-index>"   # example
```

or in Claude Code under plugins, marketplaces, add marketplace, with the same URL.

- Downloads of plugin zips authenticate through a `.netrc` entry (`machine`, `login`,
  `password` with the token) in `~/.netrc` (Windows: `%USERPROFILE%\_netrc`), restricted with
  `chmod 600`.
- The user name must not contain `@`: the client cannot handle it in the URL. Users whose user
  name is an email address need another user name.
- Claude Code loads the marketplace document in a blocking call that gives up after roughly ten
  seconds; if the response is slow or fails, its plugin discovery view simply shows nothing.
- After `/reload-plugins`, users browse plugins in `/plugins` or run
  `claude plugin install <name>`; installed plugins update on the next marketplace fetch.
- The marketplace name is usually the repository key; Claude Code caches plugins under
  `~/.claude/plugins/cache/<marketplace-name>/`.

Edge cases: installing plugins of the same repository both natively and through the companion CLI
(PKG-AI-014) writes the same cache path and disables plugins installed earlier; use one install
method per repository. Open: either Cursor, Codex and VS Code have no equivalent native connection
and install through the companion CLI, or Codex gets a native project marketplace as well; the spec
decides.

### PKG-AI-014 · Plugin publish and install workflow
Priority: **P2**

The companion CLI publishes plugins (it is the recommended path because it validates manifests and
builds the zip) and installs them for agents without a native marketplace connection.

Publish:

- Accepts a folder with one agent-specific folder or several of them (any combination, Claude Code
  not required); the user chooses which agents to include. The slug comes from the first agent manifest found; an
  ordered list of manifest paths to check can be configured.
- Version: explicit override, then `plugin.json`, then `1.0.0`. Before upload the client rewrites
  the top-level `version` in every agent manifest to that version (local files change). Differing
  `name` values across manifests are not rejected.
- Version collision: an interactive client offers overwrite, new version or abort; a
  non-interactive client fails with "version already exists".
- Optional build name and number (both or neither) record the publish in build metadata
  (PKG-AI-017).

Install for an agent:

| Version argument | Behavior |
|---|---|
| omitted | Take the version listed for the plugin in that agent's index (unified index for VS Code); all requested agents must resolve to the same version |
| `latest` | Greatest semantic version in the repository |
| exact | Used if it exists; otherwise an interactive client offers the available versions, a non-interactive one fails listing them. Installs even if the plugin is not in the agent's index, and may fail if that version is incompatible with the agent |

| Agent | Install location (global scope only for built-in agents) | After install |
|---|---|---|
| Claude Code | A subfolder of `~/.claude/plugins/local/`, keyed by repository key and plugin name | Writes a local marketplace and runs Claude Code's plugin commands if `claude` is on `PATH`; otherwise reports a warning with manual steps |
| Codex | A subfolder of `~/.agents/plugins/local/`, keyed by repository key and plugin name | Same with Codex's plugin commands |
| Cursor | `~/.cursor/plugins/local/<name>` | None; Cursor discovers the folder |
| VS Code | `~/.copilot/installed-plugins/<repo-key>/<name>` | None; VS Code discovers the folder and reads the root `plugin.json` |

- Custom agents with project and global directories can be configured; only custom agents allow
  project scope. A direct path install puts the plugin at `<dir>/<name>`.
- Update one plugin or all plugins the CLI installed for given agents (confirmation for "all"
  unless non-interactive), with dry run, force and automatic restore from a backup on failure.
- List a repository (latest version per plugin, sorted by update time or downloads) or installed
  plugins with an update check (current, behind, ahead, unknown).
- Search matches the plugin name through property search; a query without `*` is wrapped as
  `*query*`. Search uses no repository auto-discovery; without a repository it searches all.
- Delete removes one version without confirmation; a dry run is available.
- Repository selection for publish, install, update and delete works as for skills (PKG-AI-009).

## Agent packages (APM)

### PKG-AI-015 · Agent package repositories for APM
Priority: **P2**

An agent package is a versioned zip in the format of Microsoft's
[Agent Package Manager](https://microsoft.github.io/apm/): agent configuration organized as
primitives (skills, prompts, instructions, agents, hooks, commands, MCP servers) under an `.apm/`
directory, authored once and compiled by `apm compile` into each tool's layout. An agent package
repository is a private APM registry for publishing, discovering and installing such packages.

- Supported client: `apm` from version 0.23.0.
- Classes: local and federated. Remote, virtual and chained remote repositories and replication are
  not supported.
- Storage path, which also defines identity:

  ```text
  <owner>/<name>/<name>-<version>.zip
  acme/my-pkg/my-pkg-1.2.3.zip   # example
  ```

  The version is the `version` field of `apm.yml`. Subfolders are not supported.
- A `.zip` upload at any other path is refused, on local and federated repositories alike. Other
  archive formats, `.tar.gz` included, are not agent packages.
- Metadata is indexed from `apm.yml` in the package, or from `apm.lock.yaml` when `apm.yml` is
  absent.

Client configuration (native APM; written to `.apm/config.json` of the project, or to the user's
`~/.apm/config.json`):

```bash
apm experimental enable registries
apm config set registry.apm-local.url https://repo.example.com/apm/apm-local/   # example
apm config set registry.apm-local.token "${TOKEN}"
apm config set registry.apm-local.default true
```

- With a default registry, dependency references need no registry name.
- Publish: `apm publish --package <owner>/<name>` (with `--registry <name>` when no default is set
  or several registries are configured).
- Install: every install from this registry names a version, as an exact semantic version or a
  range (`apm install acme/my-pkg#1.0.0`, `#^1.2.3`), on the command line or under
  `dependencies.apm` in `apm.yml`.
- Organization-wide enforcement uses APM's own policy file in the organization's GitHub
  repository (`.github/apm-policy.yml`), which can require that dependencies come from a named
  registry and block others; this is the only way to make the repository the organization's
  default APM source.

Creation: system administrator or project administrator; UI location: administration,
repositories, create repository, local, agent packages format. The client setup dialog shows the
configuration with the user's token (UI-021 in [../ui-inventory.md](../ui-inventory.md)).

### PKG-AI-016 · APM v1 registry protocol
Priority: **P2**

Local agent package repositories answer APM v1 compatible endpoints relative to the repository's
registry base URL:

| Operation | Method and path | Inputs | Output | Required privilege |
|---|---|---|---|---|
| List versions | `GET v1/packages/<owner>/<name>/versions` | Owner, name | The package's versions | Read |
| Download | `GET v1/packages/<owner>/<name>/versions/<version>/download` | Owner, name, version | The package zip | Read |
| Publish | `PUT v1/packages/<owner>/<name>/versions/<version>` | Zip body with `Content-Type: application/zip` | Stored package | Deploy |
| Search | `GET v1/search` | Query parameters `q`, `type`, `tag`, `limit`, `offset` | Matching packages | Read |

Response bodies follow the APM registry protocol and are otherwise unspecified.
Authentication is the token configured in APM.

### PKG-AI-017 · Build metadata for agent assets
Priority: **P2**

The companion CLI can record agent assets in build metadata (BLD-003 and BLD-004 in
[../builds-and-ci.md](../builds-and-ci.md)):

- APM install with a build name and number (both required together): after a successful install,
  the client records the dependencies listed in `apm.lock.yaml`, registry-sourced ones only. No
  dependencies means no lockfile and no record; a relocated module root also relocates the
  lockfile. Dry runs record nothing.
- APM publish with a build name and number: records the published zip and its checksum.
- Module type for APM is `apm`; the module name can be overridden.
- Skill and plugin publishes accept a build name and number as well; the module name defaults to
  the slug.
- The client wraps the native `apm` command and injects credentials for the single process
  without writing them to disk; other `apm` subcommands pass through unchanged. Recording happens
  locally; publishing the build record to the system is a separate step.

## IDE extension marketplaces

### PKG-AI-018 · VS Code compatible extension gallery proxy
Priority: **P2**

A remote repository that proxies and caches an extension gallery for VS Code and editors built on
it (Cursor, Kiro, Windsurf, which is now named Devin), so the editors install extensions only
through the repository manager. Purpose: control over the extensions
developers use, caching and scanning of extensions, and blocking unapproved ones (PKG-AI-021).

- Classes: remote only. Local and virtual repositories are not supported.
- Proxies galleries that serve VSIX packages:

  | Upstream gallery | URL |
  |---|---|
  | Visual Studio Marketplace | `https://marketplace.visualstudio.com/_apis/public/gallery` |
  | Open VSX (used by Kiro) | `https://open-vsx.org/vscode/gallery` |
  | Cursor marketplace | `https://marketplace.cursorapi.com/_apis/public/gallery` |
  | Windsurf / Devin marketplace | `https://marketplace.windsurf.com/vscode/gallery` |

| Setting | Meaning | Default |
|---|---|---|
| Upstream URL | One of the galleries above | unspecified (the form shows a value to verify) |

Client configuration: the editor's `product.json` is edited so that `extensionsGallery.serviceUrl`
points at the repository's gallery path, `<repository URL>/_apis/public/gallery`, optionally
followed by `/<reference token>` (PKG-AI-019). Then all editor instances are restarted.

| Editor | macOS `product.json` folder | Windows `product.json` folder |
|---|---|---|
| VS Code | `/Applications/Visual Studio Code.app/Contents/Resources/app` | `%USERPROFILE%\AppData\Local\Programs\Microsoft VS Code\resources\app` |
| Cursor | `/Applications/Cursor.app/Contents/Resources/app` | `%USERPROFILE%\AppData\Local\Programs\Cursor\resources\app` |
| Windsurf / Devin | `/Applications/Windsurf.app/Contents/Resources/app` | `%USERPROFILE%\AppData\Local\Programs\Windsurf\resources\app` |
| Kiro | `/Applications/Kiro.app/Contents/Resources/app` | `%USERPROFILE%\AppData\Local\Programs\Kiro\resources\app` |

- Automatic editor updates overwrite `product.json` and restore the default gallery; they must be
  disabled (`"update.mode": "none"` in the user `settings.json`). Back up `product.json` first;
  editing it needs write permission on the file and all its parent folders.
- The companion CLI can do all three steps (disable updates, make the file writable, set the
  gallery URL without a token), for one of the four editors, from a repository key or a full URL,
  with an override for a custom `product.json` location.
- Install: natively in the editor's extension view, or with the editor's CLI
  (`code`, `cursor`, `kiro`, `surf`): `--install-extension <publisher>.<name>`, optionally
  `@<version>`, `--force` to re-download the latest version.

Limits:

- The editors cannot send authentication headers to a gallery. The system therefore handles all
  gallery requests anonymously and read-only.
- The `ExtensionGalleryServiceUrl` policy setting of VS Code is not supported.
- With Open VSX or another non-Microsoft upstream, VS Code reports that it cannot verify the
  extension signature, because signature verification is tied to the Visual Studio Marketplace;
  users must choose to install without verification.

### PKG-AI-019 · Read-only reference token in the gallery URL
Priority: **P2**

Because editors cannot authenticate to a gallery, the gallery URL may carry a token as its last
path segment: `<repository URL>/_apis/public/gallery/<reference token>`.

- The token is a read-only, scoped reference token (TOK-004 in
  [../access-tokens.md](../access-tokens.md)) generated by the client setup dialog for the
  repository (UI-021 in [../ui-inventory.md](../ui-inventory.md)). It is not an identity token.
- It does not authenticate the request; it attributes the developer's requests in the upstream
  curation audit log (SEC-032 in
  [../security-scanning-and-curation.md](../security-scanning-and-curation.md)).
- Without the token the gallery works the same, without attribution.

Security note: a token in a URL path ends up in editor configuration, proxy and access logs. The
read-only scope and its use for attribution only limit the damage; the repository is readable
anonymously anyway.

### PKG-AI-020 · JetBrains plugin marketplace proxy
Priority: **P2**

A remote repository that proxies and caches the public JetBrains plugin marketplace for JetBrains
IDEs (IntelliJ IDEA, PyCharm and others), so developers install only vetted plugins.

- Classes: remote only.

| Setting | Meaning | Default |
|---|---|---|
| Upstream URL | The JetBrains plugin marketplace | `https://plugins.jetbrains.com/` |

Client configuration: in the IDE, Help, Edit Custom Properties, add

```properties
idea.plugins.host=https://repo.example.com/jetbrains/jetbrains-remote   # example
```

then restart every IDE instance. Plugins are browsed and installed in Settings, Plugins as usual.
The client setup dialog shows the snippet.

Limits: the IDEs cannot authenticate to a custom plugin host, so every request is anonymous and
the repository is read-only.

Open: the setup either modifies the IDE's `product.json` or uses the custom properties file above;
the properties file is the consistent choice, and the spec decides.

### PKG-AI-021 · Curation of IDE extensions and plugins
Priority: **Later**

Both IDE proxies are offered as part of upstream curation (SEC-007 and SEC-008 in
[../security-scanning-and-curation.md](../security-scanning-and-curation.md)): policies approve or
block extensions and plugins, cached extensions are scanned, and an install of a blocked extension
fails in the editor with an error. Interaction: attribution through the reference token
(PKG-AI-019).

## MCP server registry

### PKG-AI-022 · MCP server registry with project-scoped approval
Priority: **Later**

A catalog of the MCP servers an organization has approved for its AI agents, with approval per
project. Every MCP server is blocked by default; an administrator allows a server for a project
and defines which of its tools agents may call (PKG-AI-025). Agents of project members can then
discover and add the server, through a client-side policy proxy (PKG-AI-026).

Server kinds:

| Kind | What it is | Resolution | Governance |
|---|---|---|---|
| Public local server | An executable package (npm or Python) run on the developer machine | Fetched and cached through the system's repositories | Tool policies, project scoping; packages scanned and versioned |
| Public remote server | An HTTP or SSE endpoint | Referenced by URL | Tool policies, project scoping; only approved URLs are reachable through the proxy |
| Custom local server | A private npm or PyPI package in a local repository (PKG-AI-023) | As public local | As public local |
| Custom remote server | A private HTTPS endpoint (PKG-AI-024) | By URL | As public remote |

- Public servers are indexed from the [official MCP registry](https://registry.modelcontextprotocol.io/)
  and shown by category (local, remote, custom). A server not found there is registered as a
  custom server.
- Server details shown before approval: general information; configuration (environment
  variables such as API keys, runtime arguments); an AI-generated list of the tools the server is
  predicted to expose (a suggestion, not a manifest); licenses and malicious status; the projects
  where it is already allowed.
- Allowing: one project per allow action (repeat for more projects); choose the tool policy;
  configure environment variables (each can be marked secret, which masks it) and runtime
  arguments; save. The server then appears in the registry for that project.
- Removing ("restricting") a server from a project revokes developer access immediately.
- Registry listing: filter by name or type, sort by name or last update.
- Install: on a registry entry, for a chosen project, an install action shows per-agent setup
  instructions for the client-side proxy (PKG-AI-026) and the prompt to give the agent; the proxy
  setup is needed once per agent.
- Docker-based MCP servers are supported neither from the public index nor as custom servers.
- A system-wide administrative switch enables the registry; until it is active, the install
  options for agents are not offered.

Required privilege: project administrator with the permission to allow AI assets in the project.

Interactions: the system's own MCP endpoint for AI assistants is a separate feature (EVT-077 in
[../events-and-extensibility.md](../events-and-extensibility.md)); it is the one MCP server that
needs no registry approval.

### PKG-AI-023 · Register a local custom MCP server
Priority: **Later**

Registers a privately developed MCP server package, stored in a local repository, as an approved
server for a project. Registration links the stored binary to the registry entry.

Prerequisites: the package is published to a local npm or PyPI repository with the native client;
the artifact is self-contained, including all runtime dependencies; the repository is assigned to
the target project (IAM-057 in [../identity-and-access.md](../identity-and-access.md)).

| Input | Meaning | Required |
|---|---|---|
| Project | Target project key | yes |
| Repository | Local repository holding the package | yes |
| Artifact path | Full path of the package, starting with the repository key | yes |
| README path | Markdown README in the repository | no |
| Description | Purpose and capabilities of the server | yes |
| Version | Version of the server | yes |
| Environment variables | List of name, description, format, required flag, secret flag | no |
| Runtime arguments | List of command-line flags with name, description, format, type and default value | no |

| Operation | Inputs | Output | Required privilege |
|---|---|---|---|
| Register local custom server | The inputs above, JSON, bearer token | Registry entry | Permission to allow AI assets in the project and read on the repository |
| Unregister | Server, project | Entry, metadata and tool policies removed; agents disconnected; the stored package is kept | Same |

Validation errors: missing mandatory fields; artifact not found; insufficient permissions; virtual
repository given (local repositories only); repository belongs to another project.

UI location: AI asset registry, add asset, MCP server, "local" tab: project, repository and
artifact, then tool policy, then optional runtime settings, then README path. Unregistering is the
"block" action on the server in the selected project.

### PKG-AI-024 · Register a remote custom MCP server
Priority: **Later**

Registers an external MCP endpoint that is not stored in the system.

| Input | Meaning | Rules |
|---|---|---|
| Project | Target project key | required |
| Name | Unique identifier in the catalog | `^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$` |
| Display name, description | Shown to developers | — |
| Endpoints | At least one: URL (HTTPS recommended) and transport | transport is SSE or Streamable HTTP |
| Header schema per endpoint | Headers the proxy sends: name, description, format, required flag, secret flag | header names unique, case-insensitive |

- Version, environment variables and runtime arguments are forbidden for remote registrations.
- Only the header schema is stored, never header values. Developers enter values when connecting
  through the proxy; required ones must be supplied, secret ones are masked.
- Servers that need OAuth or single sign-on are signed into through a browser flow in the client
  instead of static headers; the upstream must support OAuth dynamic client registration, manual
  client ID and secret are not supported.
- The system must reach the endpoint over the network (for what purpose is unspecified).

Required privilege: permission to allow or to manage AI assets in the project.

UI location: AI asset registry, add asset, MCP server, "remote" tab: connection details, tool
policy, headers ("add header").

### PKG-AI-025 · MCP tool policies
Priority: **Later**

Per server and project, the administrator decides which tools an agent may call:

| Mode | Behavior |
|---|---|
| Allow all tools | Approves every current tool and every tool a later server version adds |
| Allow list | Only matching tools are allowed; everything else is blocked |
| Deny list | Matching tools are blocked; everything else is allowed |

- Patterns are exact tool names or regular expressions, for example `^get_.*` (allow list: only
  tools starting with `get_`) or `.*delete.*` (deny list: every tool containing `delete`).
- Regular expressions use the RE2 engine, which rejects backtracking constructs, lookaround and
  backreferences, to rule out regular-expression denial of service.
- Policies can be edited at any time (registry, server, project, edit); changes reach the clients'
  proxies dynamically and grant or revoke access immediately. A tool can be withdrawn without
  unregistering the server.

Required privilege: project administrator.

### PKG-AI-026 · Client-side policy proxy for MCP servers
Priority: **Later**

Enforcement happens on the developer machine: a local stdio proxy sits in front of every approved
MCP server, one proxy instance per server, between it and the agent. The proxy is a client
component; the system supplies the approved-server list, the configurations and the policies it
applies.

- The agent sees only the servers approved for the active project and can list, inspect, add,
  remove and reconfigure them conversationally; switching the project syncs the server list and
  policies and asks before removing servers not approved in the new project.
- Every tool call is checked against the tool policy.
- Local servers are fetched as packages through the system's repositories and run with the
  locally installed runtime (Node.js, Python or Docker); the proxy does not install runtimes.
- For remote servers the proxy attaches the developer-supplied header values to every request.
  Secret values are entered through a terminal command, never in the chat.
- OAuth servers: before sign-in the proxy exposes one placeholder tool; calling it opens the
  browser for consent, then the proxy replaces it with the real tools and emits the MCP
  `tools/list_changed` notification (clients that do not refresh need a new session). The proxy
  refreshes tokens, serializes concurrent sign-ins and asks again when a refresh token expires.
  The editor's own MCP sign-in action has no effect on proxied servers, because it cannot reach
  the upstream through the stdio proxy.
- Servers run with the permissions of the invoking user; they cannot exceed them, and tool use is
  auditable per user.
- Credential lookup of the proxy: an explicit server argument, then a URL-and-token pair of
  environment variables (both required), then the companion CLI's single or default configured
  server.
- The proxy itself is distributed as an npm package; it can be fetched through a remote npm
  repository of the system where the public source is blocked.

### PKG-AI-027 · Organization-wide lockdown of agent clients
Priority: **Later**

To keep developers on governed MCP servers and plugin marketplaces, administrators lock the agents
with each agent vendor's own organization-level mechanism. The system's administration area
generates the configuration for each supported agent, prefilled with its host name and server
name; the administrator copies it into the agent vendor's console or device management. Every
allowlist names the same three entries: the system's own MCP URL, the proxy's start command pinned
to one server name, and the same command with the extra argument the proxy uses when an
integration plugin starts it (matching is exact, so both command forms are needed). With several
instances, each one adds its own set of entries and must have the registry enabled.

| Agent | Mechanism | Governs | Notes |
|---|---|---|---|
| Claude Code | Organization managed settings, applied in the agent vendor's admin console | MCP servers and plugin marketplaces | See the table below. Required Claude Code version: open, either 2.1.143 or 2.1.193 |
| Cursor | Team MCP allowlist in the team dashboard (URL and command entries) | MCP servers | Needs Cursor's enterprise plan; applies at the next session start |
| VS Code | Enterprise policy `ChatAllowedMcpServers`, pushed by device management (macOS configuration profile or Windows registry script) | MCP servers | Also writes GitHub Copilot's `allowedMcpServers` for delegated Copilot sessions and disables Claude and Codex delegation (`Claude3PIntegration`, `Codex3PIntegration`); needs VS Code 1.133 or later, older versions ignore it silently; read only at startup, so a full restart is required; does not reach the Copilot cloud agent |

| Claude Code managed setting | Purpose |
|---|---|
| `allowManagedMcpServersOnly` | Only the managed MCP allowlist counts; users cannot widen it, but deny entries from every scope still apply |
| `allowedMcpServers` | Exact list of allowed servers: the system's MCP URL and the proxy's start commands |
| `strictKnownMarketplaces` with a `hostPattern` | Only plugin marketplaces on the system's host are allowed |
| `extraKnownMarketplaces`, `enabledPlugins` | Register and enable the approved integration plugin for everyone |
| `disableSideloadFlags` | Blocks command-line flags that would bypass the marketplace restriction |

- Claude Code's machine-level `managed-mcp.json` bypasses the allowlist (from Claude Code 2.1.259
  on), so complete enforcement also removes write access to that file for developer accounts; a
  file that is present but empty stops the proxy from loading.
- The proxy's npm source in these entries defaults to an externally hosted registry (PKG-AI-035);
  on restricted networks it is replaced by a remote npm repository of the system (PKG-AI-026).

## Governance of AI assets

### PKG-AI-028 · AI asset catalog
Priority: **Later**

A UI area that brings AI assets under governance:

| Area | Purpose |
|---|---|
| Marketplace | Explore external assets (models, MCP servers) and add them |
| Registry | Assets approved for the organization and its projects; add assets |
| Unmanaged asset detection | AI models found in repositories that nobody approved; allow, block or dismiss each |

- Asset types: models (external APIs, open-source packages such as Hugging Face models, custom
  models), MCP servers, skills, plugins; agents are planned.
- Skills tab: name, latest version, downloads, origin. Skill details per project: name linked to
  the stored item, version, dates, scan status linked to the results, the rendered `SKILL.md`, and
  the bundle's file tree.
- Plugins tab: name, version, scan status, description, files; details add the README and which
  agents the plugin supports.
- Agents query a project-scoped discovery index; new publishes usually appear within seconds,
  sometimes up to a minute.

### PKG-AI-029 · Project-scoped registries with automatic repository provisioning
Priority: **Later**

- Skills and plugins are scoped to projects (IAM-051 in
  [../identity-and-access.md](../identity-and-access.md)); discovery shows only the active
  project's assets.
- The first publish to a project creates a dedicated local repository automatically:
  `<project>-skills-local` for skills, `<project>-plugins-local` for plugins; for skills, a numeric
  suffix `-<n>` is appended when the name is taken. Scanning is enabled on it. Administrators may
  rename it. For plugins any user can trigger provisioning, while uploading still requires
  deploy permission; for skills, deploy permission on the project's skills repository is
  sufficient to publish.
- Each project gets its own plugin marketplace (for Claude Code and Codex) listing only the
  plugins allowed for that project; it is named after the project with a fixed prefix. There is
  no marketplace aggregated across projects; developers register one per project.
- Marketplace entries may carry namespaced names (repository short name plus plugin name), and the
  Claude Code install identifier combines plugin, repository and marketplace
  (`<plugin>--<repo-key>@<marketplace>`); the name in `plugin.json` is then not the install name.
- The client resolves the active project from an environment variable, the single project the
  user belongs to, or a picker.

### PKG-AI-030 · Skill policies
Priority: **Later**

Each skill policy combines a scope, a rule and an action (allow or block). Policies sit on top of
the scan gate (PKG-AI-007): the scan decides whether a skill can be installed, the policies decide
whether it may run in a given project. One violated policy is enough to block the skill in that
project.

| Rule type | Action | Condition |
|---|---|---|
| Manual allow-list | Allow | Lists skills by name, version and repository path. Never overrides an active block from another policy |
| Block malicious | Block | The scan flagged the skill as malicious |
| Block vulnerable components above a threshold | Block | One of the skill's components has a CVE in the chosen severity band: critical 9.0 to 10.0, high 7.0 to 8.9, medium 4.0 to 6.9, low 0.0 to 3.9; combined with "skip if not applicable" (applicability analysis, SEC-081 in [../security-scanning-and-curation.md](../security-scanning-and-curation.md)) or "ignore applicability": eight variants |

| Setting | Meaning | Default |
|---|---|---|
| Name, description | Description optional | — |
| Asset type | For example skills | — |
| Scope | Global (organization) or one or more projects | — |
| Rule, action | As above | — |
| Projects that may use skills right away, even new or not yet published ones | Administrative override in the AI settings; shown as a flagged override on the skill's policy tab | none |

- Policies take effect immediately and apply to all subsequent evaluations; deleting a policy stops
  its enforcement, other policies still apply.
- Enforcement happens when an agent invokes a skill; the agent integration shows the skill name,
  the project and the names of the violated policies. Scan-in-progress and missing-role states are
  reported with their own messages, not as violations.
- Privileges: project administrators manage policies of their projects; global policies need a
  global administrator.
- UI: an AI policies list (name, scope, rule, action, details), a create wizard (name, scope, rule,
  action), and a policies tab on each skill per project.

Edge cases:

- Some agents (Cursor is named) load a skill's full content into the chat context as soon as it is
  available, regardless of its status; a new chat is needed to keep a blocked skill's content out
  of context.
- Blocking unscanned skills is a use of policies, and an example block message cites such a
  policy ("no completed scan"), but no rule type for it is specified.
- Scan-based rule types cannot be created while AI-based scanning is not activated (PKG-AI-034);
  a manual allow-list works without any scan.

### PKG-AI-031 · Skill waivers
Priority: **Later**

A waiver is a time-limited exception for a skill blocked by policies. One waiver covers every
policy that blocks the skill, for one project, repository and version.

| Operation | Who | Effect |
|---|---|---|
| Create request | Any user (in the UI, from the skill, or through the agent) | Requires an expiration date and a justification; the request is pending and the skill stays blocked |
| Cancel request | Any user (create and cancel are granted to all users) | Pending request becomes canceled |
| Review (approve or reject) | Administrator | Requires a decision and an expiration date; a rejected skill can be requested again |
| Revoke | Administrator | Approved waiver becomes revoked |
| View an approved waiver | unspecified | Details with links to the skill (waived version selected) and the repository item |

- Statuses: pending, approved, rejected, expired, revoked, canceled.
- A second active waiver for the same project, repository and version is rejected with a message
  naming the existing waiver's ID.
- UI: waiver requests page with summary counters (all, pending, approved, expired, rejected),
  search by waiver ID, scope or asset name, sortable and filterable columns.

Interactions: waivers for curation and scan policies in general (SEC-028 to SEC-030 in
[../security-scanning-and-curation.md](../security-scanning-and-curation.md); DIST-060 in
[../distribution-and-lifecycle.md](../distribution-and-lifecycle.md)).

### PKG-AI-032 · Roles for unscanned skills
Priority: **Later**

Three capabilities govern skills that are not stored or not scanned yet ("unknown" skills):

| Capability | Effect |
|---|---|
| Read skill artifacts | Needed to read any skill bundle; granted by the project's developer role. AI-specific roles do not include it |
| Submit unknown skills for evaluation | An AI-asset developer role: using an unknown skill from the local machine triggers a scan and policy evaluation |
| Use unknown skills immediately | An AI-asset governor role: no scan and no policy check |

A member holding neither AI role has to publish an unknown skill and wait for a successful scan
before using it. Interactions: project roles (IAM-055 in
[../identity-and-access.md](../identity-and-access.md)).

### PKG-AI-033 · AI asset scanner with operator-supplied model credentials
Priority: **Later**

The AI-based scanner for agent assets (PKG-AI-007) calls a large language model. On a self-hosted
installation the administrator must configure a provider and credentials; without them no new
scans run.

| Setting | Meaning | Default |
|---|---|---|
| Model | One of the qualified provider and model combinations | none |
| Credentials | Anthropic: API key. OpenAI: API key. Google Vertex AI: the full service-account key JSON (the project is taken from it) and the region `global` | none |

- Qualified combinations: Anthropic Claude Opus 4.8, OpenAI GPT 5.6 Sol, Google
  Gemini 3.1 Pro preview on Vertex AI (global location only). Arbitrary providers or models are not
  supported.
- Vertex AI preparation: switch on the Vertex AI API in the Google Cloud project, give the service
  account Google's "Vertex AI User" role, and create a JSON key for it.
- Saving sends one minimal request to the model to validate credentials and model access; on
  failure nothing is saved and the error is shown. A successful save does not prove that content
  filters will let scan requests through.
- Credentials are stored encrypted and never displayed again.
- Changes apply to new scans only; existing results stay. While an edited configuration is being
  validated, the previous one stays active. Deleting the configuration stops new scans on a
  self-hosted installation.
- Provider content filters and guardrails can block scan requests, because scanned content may
  look malicious; the scan then fails and the reason is logged. A dedicated provider project
  or key with adjusted filters is recommended.
- Results: findings and a verdict that feed scan policies and can lead to blocking (PKG-AI-030).

Required privilege: system administrator. UI location: administration, AI configuration, AI asset
scanner.

Open: on self-hosted installations either the scanner requires the operator-supplied credentials
above, or a self-hosted scanner is not supported yet; the spec decides.

### PKG-AI-036 · Plugin scanning and allowed-plugin status
Priority: **Later**

Plugins get a scan after upload but, unlike skills (PKG-AI-007), no post-publish gate in the
publishing client.

- A published plugin appears in the AI asset catalog's plugin registry (PKG-AI-028) right away; the
  scan runs automatically and its result is shown there, usually within a few minutes.
- Scan status values: clean, suspicious, malicious, unscanned. A plugin stays unscanned when the
  repository is not indexed for scanning (REPO-021 in [../repositories.md](../repositories.md)) or
  AI-based scanning is not activated (PKG-AI-034). Every agent plugin repository should be
  indexed; repositories provisioned for a project (PKG-AI-029) are indexed and scanned
  automatically.
- Discovery through the agent integration returns only plugins that are allowed for the active
  project, with name, version, description and status; the project marketplace likewise lists only
  the plugins the project's policies allow. Install through the agent integration checks the
  allowed status first.
- Registry view: one card per plugin with name, version, scan status, description and files.

Edge cases: which policy rules decide "allowed" for plugins is unspecified; the skill policy
rule types (PKG-AI-030) are defined for skills only.

## Out of scope

### PKG-AI-034 · Hosted-service activation of AI features
Priority: **Out of scope**

Activation and license management of AI features in a hosted service: acceptance of AI service
terms before AI-based scanning runs (skills and plugins stay unscanned otherwise, and scan-based
policy rules cannot be created), preview toggles in a hosted account portal (without the
skill-enforcement toggle, policies exist but never block), activation through support, hosted-only
availability of skill and plugin scanning, and seat- and volume-based fallback to an operator-run
model.

### PKG-AI-035 · Separately distributed agent integrations
Priority: **Out of scope**

Prebuilt IDE and agent plugins, catalog skills and an initialization command for agents, and a
hosted npm source for the client-side policy proxy, all distributed apart from the repository
manager.

### PKG-AI-037 · Command-output compression for coding agents
Priority: **Out of scope**

A client-side wrapper for the shell commands that coding agents run shortens repetitive output
before it reaches the agent's context, keeps failures, summaries and hints, and keeps the full
output retrievable. Further parts: a local codebase index for
structural questions, custom filter rules, organization-wide rollout through device management,
use in CI jobs, and telemetry export over OTLP. It has no repository-side function.

## Notes for implementers

- Plugin and skill identity comes from the storage path, not from the manifest. Publish tools
  rewrite manifests to match; a server that trusts the manifest instead will list a plugin under a
  different name than clients install.
- Marketplace indexes list only the latest semantic version per plugin, chosen across layouts.
  Publishing an older version must not replace a newer index entry, and per-agent indexes can
  legitimately disagree.
- Claude Code fetches a marketplace synchronously with a timeout of about ten seconds; serve
  indexes as precomputed documents, not computed per request.
- Credentials embedded in marketplace URLs and `.netrc` break on user names containing `@`; make
  sure a token works with a plain user name.
- Editors and JetBrains IDEs cannot authenticate to galleries; anonymous read on those
  repositories is a requirement, and anything in the gallery URL is visible in logs.
- Editor auto-updates silently revert `product.json`; client setup instructions must include
  disabling updates, or the gallery connection falls back to the public marketplace after the next
  editor update.
- The skill discovery document is the only anonymous skills endpoint; everything else needs
  credentials, including listing.
- Slugs are lowercase with digits and hyphens; enforce the pattern on publish, because clients
  build install paths and file names from it.
- "Not scanned" means installable on a repository without scanning and "wait" on one with
  scanning; a gate that cannot tell the two apart blocks installs forever.
- Tool policy regular expressions are evaluated on every MCP tool call; restrict them to a linear
  time engine such as RE2.
