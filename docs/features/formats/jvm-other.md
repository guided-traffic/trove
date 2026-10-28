# Gradle, Ivy, sbt and Eclipse P2 formats

This page catalogs the JVM ecosystem formats next to Maven: Gradle, Apache Ivy and sbt, which are
separate format choices built on the Maven repository machinery and share its settings, and
Eclipse P2, the provisioning format of the Eclipse IDE and of Tycho builds, which the system
supports through remote and virtual repositories only. Maven itself, including the shared
release/snapshot, checksum and metadata behavior these formats inherit, is on
[maven.md](maven.md). Where behavior comes from the build tools' own documentation, the entry links
it ([Gradle module metadata](https://docs.gradle.org/current/userguide/publishing_gradle_module_metadata.html),
[sbt proxy repositories](https://www.scala-sbt.org/1.x/docs/Proxy-Repositories.html),
[Eclipse P2](https://help.eclipse.org/latest/index.jsp?topic=%2Forg.eclipse.platform.doc.isv%2Fguide%2Fp2_overview.htm)).

Throughout this page, `https://repo.example.com/<format>/<repo-key>/` stands for the base URL of
one repository (REPO-005 in [../repositories.md](../repositories.md)); `gradle-local`,
`gradle-virtual`, `sbt-maven-remote`, `sbt-ivy-remote`, `p2-virtual` are example repository keys;
`${REPO_TOKEN}` is an access token.

## Maven family

### PKG-JVM-001 · Maven-family formats and their shared settings
Priority: **P1**

Maven, Gradle, Ivy and sbt are four format choices that all support local, remote and virtual
repositories (REPO-006 in [../repositories.md](../repositories.md)) and share one block of
format-specific settings:

| Class | Shared settings | Entries |
|---|---|---|
| Local | deployment checksum policy, snapshot naming behavior, maximum unique snapshots, handle releases, handle snapshots, suppress POM consistency check | PKG-MAVEN-003, PKG-MAVEN-005, PKG-MAVEN-006, PKG-MAVEN-010, PKG-MAVEN-011 in [maven.md](maven.md) |
| Remote | upstream checksum policy, maximum unique snapshots, eagerly fetch jars, eagerly fetch sources, suppress POM consistency check, handle releases, handle snapshots; "list remote artifacts" only for Maven | PKG-MAVEN-017 to PKG-MAVEN-020 |
| Virtual | POM repository-reference cleanup, signing key pair; force authentication only for Maven | PKG-MAVEN-023, PKG-MAVEN-025, PKG-MAVEN-026 |

Differences between the four:

| Aspect | Maven | Gradle | Ivy | sbt |
|---|---|---|---|---|
| Layout chosen at creation | Maven 2 | Maven 2 | unspecified: the create form proposes a layout matching the format, without naming it for Ivy; the configuration API defaults to Maven 2 "where applicable" | the default sbt layout unless another is chosen (PKG-JVM-013) |
| Priority resolution on local repositories (REPO-018) | yes | yes | not supported | yes |
| CDN download redirect (REPO-020) | local and remote | local and remote | not offered | not offered |
| Scanner indexing (REPO-021) | yes | yes | yes | yes |
| Lead file for package retention (RET-009) | `.pom` | `.pom` | not listed | `.pom`, `.xml` |
| Federated repositories | yes | yes | yes | yes |

- Federation supports every format except a Terraform state backend; the federated form has a
  Maven/Gradle/Ivy/sbt section with the shared settings (see
  [../replication-and-federation.md](../replication-and-federation.md)).
- Open: the signing key pair applies either to all four, as the UI shows it, or to Maven only, as
  the configuration-as-code form has it; the spec decides.
- Maven coordinate search works on repositories whose layout carries Maven-style coordinates, which
  includes Gradle, Ivy and sbt layouts (SRCH-006 in [../search-and-query.md](../search-and-query.md)).

## Gradle

### PKG-JVM-002 · Gradle repositories
Priority: **P1**

- Gradle is a format for local, remote and virtual repositories, and for federated ones.
- Gradle local repositories use the Maven 2 layout and the Maven-family settings (PKG-JVM-001);
  the system calculates metadata for their artifacts as for Maven (PKG-MAVEN-014 in
  [maven.md](maven.md)).
- Gradle remote repositories proxy Maven registries such as `https://repo1.maven.org/maven2/`.
- One Gradle client can resolve and publish with the Maven layout, the Ivy layout or both, and can
  use any repository whose layout is a Maven or Ivy layout: Gradle, Maven, Ivy and sbt
  repositories alike.
- Publishing from Gradle uses Gradle's `maven-publish` or `ivy-publish` plugin against a local
  repository (or a virtual repository with a default deployment repository, REPO-038).

Creating a repository requires the system administrator or a project administrator. UI location:
administration, repositories, create repository, choose the class, then the Gradle format.

Interactions: Gradle builds can also run through the companion CLI (PKG-JVM-006) or a build-tool
plugin (PKG-JVM-007).

### PKG-JVM-003 · Gradle client setup instructions
Priority: **P1**

The client setup dialog (UI-021 in [../ui-inventory.md](../ui-inventory.md)) for a Gradle
repository generates a build-script snippet and a `gradle.properties` file, with the user's
credentials or a newly generated token (UI-022).

On the configure tab, three roles are configured separately: plugin resolution, library
resolution and library publication. For each role:

| Field | Meaning |
|---|---|
| Use Maven / Use Ivy | Which descriptor convention the generated script uses against the chosen repository; one or both. |
| Repository | The repository for this role. |
| Repository layout | The layout of the chosen repository; only offered when Ivy is selected. |

| Selection | Generated script | Use when | Layout field |
|---|---|---|---|
| Use Maven (default) | Maven 2 paths, POMs and Maven coordinates | builds with `maven-publish`; repositories in the Maven 2 layout, which Gradle and Maven repositories normally have | not available |
| Use Ivy | Ivy patterns, `ivy.xml` and Ivy paths | builds with `ivy-publish`; repositories in an Ivy layout | the repository's layout |
| Both | Maven 2 layout and Ivy patterns against the same repository | builds that publish or resolve POMs and `ivy.xml` side by side | the repository's layout |

- A "Gradle version" field selects the Gradle version the snippet is generated for; a "generate
  settings" action renders the files; both files can be downloaded and are added to the project.
- Nothing checks that the selected convention and layout fit the repository: an inconsistent
  choice still yields a snippet, and the build then looks for files at the wrong paths.

Unspecified: the list of Gradle versions offered and how the snippet differs between them.

### PKG-JVM-004 · Gradle cache layout
Priority: **P2**

The system ships a bundled "Gradle" layout, modeled on Gradle's own cache layout (REPO-047 in
[../repositories.md](../repositories.md)); it is not the layout a Gradle repository gets by
default (PKG-JVM-002).

| Part | Pattern (descriptive notation, REPO-048) |
|---|---|
| Artifact | `<org-dotted>/<module>/<base-rev>(-<folder-integ-rev>)/<module>-<base-rev>(-<file-integ-rev>)(-<classifier>).<ext>` |
| Descriptor | `<org-dotted>/<module>/ivy-<base-rev>(-<file-integ-rev>).xml` |

Example: `com.example/http-client/4.2.0/http-client-4.2.0.jar` and
`com.example/http-client/ivy-4.2.0.xml`. The layout's integration revision expressions are
unspecified.

### PKG-JVM-005 · Gradle module metadata files
Priority: **P1**

From Gradle's documentation ([publishing guide](https://docs.gradle.org/current/userguide/publishing_gradle_module_metadata.html),
[format specification](https://github.com/gradle/gradle/blob/master/platforms/documentation/docs/src/docs/design/gradle-module-metadata-latest-specification.md)):

- Gradle's `maven-publish` and `ivy-publish` plugins publish a JSON module metadata file alongside
  the POM or `ivy.xml`, not instead of it. In a Maven repository the file has the extension
  `module`, for example `http-client-4.2.0.module` next to `http-client-4.2.0.pom`.
- The POM or `ivy.xml` carries a marker comment that tells Gradle consumers a module metadata file
  exists; Gradle reads the descriptor first and fetches the module file only when the marker is
  there. When the module file is present, Gradle ignores the POM's content.
- Gradle's documentation names "the repository rejects the metadata file" as a reason to switch its
  publication off.

A Gradle-compatible repository has to accept, store and serve module metadata files like any
other artifact of the module, with checksum files, and keep them consistent with the POM (deletion,
snapshot naming, maximum unique snapshots); how the system treats them is unspecified.

### PKG-JVM-006 · Wrapping Gradle with the companion CLI
Priority: **P2**

The companion command-line client can run Gradle with resolution and deployment routed to
configured repositories and collect a build metadata record (BLD-039 to BLD-041 in
[../builds-and-ci.md](../builds-and-ci.md)). Gradle-specific parts:

<details>
<summary>Per-project Gradle configuration of the wrapper</summary>

| Setting | Meaning | Default |
|---|---|---|
| Global | Apply to all Gradle projects on the machine; stored in the user's home directory instead of the project | off |
| Resolution connection, resolution repository | Server profile and repository to resolve from | none |
| Deployment connection, deployment repository | Server profile and repository to deploy to | none |
| Build already applies the build-tool plugin | Avoids applying the plugin (PKG-JVM-007) twice | off |
| Use Gradle wrapper | Run the project's `gradlew` | off |
| Deploy Maven descriptors | Publish POMs | on |
| Deploy Ivy descriptors | Publish `ivy.xml` files | on |
| Ivy descriptor pattern | Ivy pattern for descriptors | `[organization]/[module]/ivy-[revision].xml` |
| Ivy artifact pattern | Ivy pattern for artifacts | `[organization]/[module]/[revision]/[artifact]-[revision](-[classifier]).[ext]` |

</details>

- Run options: build name and number, project, module, upload threads (default 3), server
  profile, scan before upload with a choice of output format.
- The configuration step does not check that a Gradle project exists in the directory; run without
  arguments in a non-interactive shell it fails, so CI passes every value explicitly.
- Wrapped mode injects the integration through a Gradle init script in the user's Gradle home.
  Native mode (BLD-041) injects nothing; Gradle must be configured by the user (PKG-JVM-003), and
  build metadata is still collected.
- When a build deploys nothing, the usual cause is that no publication is selected for the
  build-tool plugin's publish task.
- Prerequisites: Gradle 6.0 or later, JDK 11 or later.

### PKG-JVM-007 · Build-tool plugin for Gradle
Priority: **Later**

A Gradle plugin that collects the build's publications, deploys their artifacts and publishes the
build metadata record (BLD-033 in [../builds-and-ci.md](../builds-and-ci.md)).

- Needs `maven-publish`, `ivy-publish` or both, and at least one publication; a plugin block alone
  fails because Gradle offers no publishing extension then.
- Each project gets a publish task; a deploy task on the root project uploads artifacts and the
  record after all projects. Aggregator projects can be skipped.
- Targets: one repository, or separate release and snapshot repositories selected by the version;
  the target must be a Maven or Gradle local repository; deploy and build-publish permissions are
  needed. Uploads run in parallel (default 3 workers).
- Version pairing: the current plugin generation needs Gradle 8 or later (aimed at
  Gradle 9, which needs Java 17); the previous generation covers Gradle 6.8.1 to 8; an older
  generation with a different configuration language is legacy and should be migrated by
  recording publication names, release and snapshot repository keys, build name and number, and
  comparing artifact paths and build records after a trial run against a non-production
  repository.

## Ivy

### PKG-JVM-008 · Ivy repositories
Priority: **P2**

- Ivy is a format for local, remote and virtual repositories with the Maven-family settings
  (PKG-JVM-001). Federation: see PKG-JVM-001.
- Ivy builds are treated as legacy: existing Ant and Ivy builds are maintained,
  new integrations should use Maven or Gradle.
- Recommended setup: resolve through a virtual repository, publish to a local repository.
- Not offered for Ivy: priority resolution on local repositories, CDN download redirect.

An existing Ivy build normally consists of:

| File | Role |
|---|---|
| `ivysettings.xml` | Resolver definitions and credentials |
| `ivy.xml` | Module descriptor and dependencies |
| `build.xml` | Ant targets that call Ivy (resolve, retrieve, publish to a named resolver) |

Troubleshooting: dependencies that do not resolve, or files published
under unexpected paths, mean the resolver's artifact and Ivy patterns do not match the
repository's layout; `401` or `403` on publish means missing credentials or missing deploy
permission. Credentials belong outside source-controlled XML.

### PKG-JVM-009 · Ivy layout and integration revisions
Priority: **P2**

The bundled Ivy layout (the default Ivy layout, REPO-047):

| Part | Value (descriptive notation, REPO-048) |
|---|---|
| Artifact pattern | `<org-dotted>/<module>/<base-rev>(-<folder-integ-rev>)/<type>s/<module>-<base-rev>(-<file-integ-rev>)(-<classifier>).<ext>` |
| Folder integration revision | usually equal to the file integration revision: a 14-digit timestamp, expression `\d{14}` |
| File integration revision | a 14-digit timestamp, expression `\d{14}` (example version `5.1-20101202161531`) |

Example: `com.example/http-client/4.2.0/jars/http-client-4.2.0.jar`. The descriptor pattern
of the bundled Ivy layout is unspecified.

Because the Ivy layout defines both integration revision expressions, downloads through Ivy-layout
repositories can use the reserved latest-integration and latest-release tokens for non-Maven
layouts; "newest" is then decided alphabetically unless the system-wide creation-time switch is set
(ART-013 in [../artifacts.md](../artifacts.md)). Mapping the module fields to Ivy terms: organization,
module, revision; the values to compare when moving a build to Maven or Gradle are organization,
module, revision, artifact name and descriptor paths.

### PKG-JVM-010 · Ivy dynamic revisions against remote repositories
Priority: **P2**

Ivy resolves dynamic revisions (such as `latest.integration` or version ranges) by listing the
versions available upstream. Through a remote repository this works only when the remote
repository lists upstream folder items (RMT-038 in
[../remote-repositories.md](../remote-repositories.md)); remote Ivy version lookups are the case
that needs it. Listings are cached for the metadata retrieval cache
period (RMT-018). The listing switch is off by default. Within the Maven family the remote settings
form offers its browsing counterpart for Maven only (PKG-MAVEN-020 in [maven.md](maven.md)); how an
administrator turns listing on for an Ivy remote repository in the UI is unspecified, the
configuration interfaces carry the switch for every remote repository.

The version list search finds integration versions in non-Maven layouts only when the layout's
artifact pattern has the base-revision token followed by the file-integration-revision token with
only literals between them; its remote lookup uses folder listings for non-Maven layouts
(SRCH-016 in [../search-and-query.md](../search-and-query.md)).

### PKG-JVM-011 · Ivy settings in the client setup instructions
Priority: **P2**

The client setup dialog can generate build-tool settings for Ivy as it does for Maven and Gradle;
an administrator can turn the generated file into a personalised download (PKG-MAVEN-030 in
[maven.md](maven.md)). The content of the generated Ivy settings is unspecified.

## sbt

### PKG-JVM-012 · sbt repositories
Priority: **P2**

- sbt (the Scala and Java build tool) is a format for local, remote and virtual repositories with
  the Maven-family settings (PKG-JVM-001). Federation: see PKG-JVM-001.
- Local repositories store the organization's sbt artifacts; the system calculates metadata for
  them. The layout choice matters (PKG-JVM-013).
- Remote and virtual repositories proxy and aggregate Maven and Ivy upstreams; Maven and Ivy
  upstreams belong in separate repositories (PKG-JVM-015).
- Not offered for sbt: CDN download redirect.

Creating a repository requires the system administrator or a project administrator. UI location:
administration, repositories, create repository, choose the class, then the sbt format.

### PKG-JVM-013 · sbt layouts
Priority: **P2**

| Layout | Use | Notes |
|---|---|---|
| Default sbt layout | applied automatically when no other layout is chosen | Not supported by cleanup and archive policies, and not suitable for resolving packages during those runs; not recommended. |
| Maven 2 layout | sbt projects that publish Maven-style | Recommended. |
| sbt Ivy layout | sbt projects that publish Ivy-style | Recommended. Its patterns are unspecified. |

- Repositories created with the default sbt layout should be switched to one of the recommended
  layouts.
- After changing a repository's layout, the index must be recalculated (REPO-009 in
  [../repositories.md](../repositories.md)). The recalculation indexes the packages already stored
  and calculates their metadata, which resolution performance, cleanup and archiving depend on.
- Package retention policies cannot see packages whose layout does not yield name and version
  (RET-009 in [../retention-and-cleanup.md](../retention-and-cleanup.md)).

### PKG-JVM-014 · sbt client configuration
Priority: **P2**

Client instructions:

Proxy repositories for sbt itself, its plugins and all builds, in `~/.sbt/repositories`:

```text
[repositories]
local
company-ivy: https://repo.example.com/sbt/sbt-ivy-remote/, [organization]/[module]/(scala_[scalaVersion]/)(sbt_[sbtVersion]/)[revision]/[type]s/[artifact](-[classifier]).[ext]
company-maven: https://repo.example.com/sbt/sbt-maven-remote/
```

- The Ivy pattern must match exactly, or sbt plugins do not resolve
  ([sbt proxy repositories](https://www.scala-sbt.org/1.x/docs/Proxy-Repositories.html)).
- The launcher option `-Dsbt.override.build.repos=true` makes sbt ignore resolvers declared in
  builds and use only the repositories file; it can be set in the launcher script or the global
  sbt options file. sbt's default is false.

Resolution and publishing in `build.sbt`:

```scala
resolvers += "company" at "https://repo.example.com/sbt/sbt-virtual/"
publishTo := Some("Repository realm" at "https://repo.example.com/sbt/sbt-maven-local")
credentials += Credentials("Repository realm", "repo.example.com", "jdoe", sys.env("REPO_TOKEN"))
```

- The credentials entry takes the host name without scheme.
- The snippets use the same realm string in `publishTo` and in the credentials entry.
  Clients that select credentials by realm, as Ivy does, need the realm in the system's
  authentication challenge to equal that string. Which realm the system announces is unspecified.
- A token is the recommended credential.
- The client setup dialog offers these snippets with values filled in (UI-021 in
  [../ui-inventory.md](../ui-inventory.md)).

### PKG-JVM-015 · Separate Maven and Ivy proxies for sbt
Priority: **P2**

- Maven upstreams and Ivy upstreams are proxied by separate remote (or virtual) repositories;
  mixing both in one virtual repository is discouraged, also by sbt's documentation.
- Reason (sbt documentation): even builds that never use Ivy need an Ivy proxy, because sbt
  resolves its own plugins from an Ivy-layout repository with a custom pattern that encodes Scala
  and sbt versions (PKG-JVM-014). sbt's documentation names
  `https://repo.scala-sbt.org/scalasbt/sbt-plugin-releases` as that upstream and warns that a
  proxy must pass such non-Maven paths through instead of rejecting them.

### PKG-JVM-016 · Publishing sbt snapshots
Priority: **P2**

- Release: `publishTo` points at the repository, `sbt publish` deploys.
- Snapshot: the snippet appends a matrix parameter carrying the current time in
  milliseconds to the `publishTo` URL, so each snapshot publication carries its publication time as
  a property (matrix parameters, ART-003 in [../artifacts.md](../artifacts.md)):

```scala
publishTo := Some("Repository realm" at
  "https://repo.example.com/sbt/sbt-maven-local;<time-property>=" + System.currentTimeMillis())
```

`<time-property>` stands for the property name the system's client setup uses. Unspecified: whether the system uses the
property for anything (for example snapshot ordering or cleanup), and how snapshot naming behavior
(PKG-MAVEN-010) applies to Ivy-style sbt snapshots.

## Eclipse P2

### PKG-JVM-017 · P2 repositories: remote and virtual only
Priority: **P2**

P2 is the provisioning technology Eclipse uses for plug-ins and features; Eclipse, Tycho and other
P2 clients consume it. The system proxies and caches P2 repositories and aggregates their metadata.

- Classes: remote and virtual. There is no P2 local repository class (REPO-006).
- Local content reaches P2 clients through a P2 virtual repository that includes a local
  repository of another format, typically Maven or generic (PKG-JVM-019).
- The system calculates P2 metadata for P2 artifacts and packages hosted in local Maven or
  generic repositories; how and when is unspecified.
- Recommended: one P2 virtual repository per Eclipse release in use (for example separate ones for
  two Eclipse releases used side by side).
- Eclipse versions older than 3.6 may not work.
- Not offered for P2: CDN download redirect, scanner indexing.

Creating a repository requires the system administrator or a project administrator. UI location:
administration, repositories, create repository, class remote or virtual, then the P2 format.

### PKG-JVM-018 · Remote P2 repository URL
Priority: **P2**

| Setting | Meaning | Default |
|---|---|---|
| Upstream URL | URL of the location that holds the upstream's P2 metadata files (`content.jar`, `artifacts.jar`, `compositeContent.xml` and the like). If the metadata files are inside an archive, the URL ends in `!/` after the archive name, for example `https://downloads.example.org/sdk.zip!/`. | none |
| List remote folder items | Browsing lists upstream folder items not cached yet; needed for resolution that depends on remote folder content. Listings are cached for the metadata retrieval cache period. | off |

The remaining remote settings are those of every remote repository
([../remote-repositories.md](../remote-repositories.md)). How the system reads archive-embedded
metadata (`!/` URLs) is unspecified beyond the URL form. Any P2 update site can be the upstream,
including the update sites of third-party IDE plug-ins.

### PKG-JVM-019 · P2 virtual repository composition
Priority: **P2**

P2 virtual repositories select members differently from all other formats (REPO-044 in
[../repositories.md](../repositories.md)):

| Setting | Meaning | Default |
|---|---|---|
| Local repository | A local repository to include, typically Maven or generic. | none |
| Path suffix | Sub-path inside that local repository where the P2 metadata files (content, artifacts, composite content) lie; empty means the repository root. For a P2 site deployed as one archive, the archive's root, for example `eclipse-repository.zip!`, with or without a trailing `/` (which form is canonical is unspecified). | empty |
| P2 repository URL | URL of an upstream P2 repository. | none |
| Force authentication | See PKG-JVM-022. | off |

- When a P2 URL is added, the system analyzes the upstream P2 metadata and works out which remote
  repositories are needed, because one P2 site can aggregate content from several hosts. In this
  flow the system takes care of the remote repositories itself, one per upstream base URL
  (PKG-JVM-020); whether it asks the administrator to confirm them is unspecified.
- Several URLs can be listed; the configuration-as-code form carries a list of upstream URLs per
  P2 virtual repository.
- Tycho builds deployed to a local repository as one archive are exposed through the path suffix.

Unspecified: whether several local repositories can be included, how the merged P2 metadata is
composed (for example as a composite repository), and how conflicts between members are resolved.

### PKG-JVM-020 · One remote repository per upstream base URL
Priority: **P2**

When several P2 upstreams share one base URL (for example two release trains below
`https://downloads.example.org/releases/`), a P2 virtual repository must contain only one remote
repository for that base URL, with the sub-paths recorded in the virtual repository's definition.

- Remote repositories created by the virtual repository (PKG-JVM-019) follow this rule
  automatically; the virtual repository then lists two URLs but contains a single remote
  repository.
- Administrators who create the remote repositories themselves must create only one per base URL
  and point the virtual repository's sub-paths at it.

### PKG-JVM-021 · P2 metadata aggregation and caching
Priority: **P2**

- Virtual P2 repositories serve metadata aggregated from their members; remote P2 repositories
  cache the upstream metadata under the metadata retrieval cache period (RMT-018).
- The aggregation algorithm, the files the virtual repository serves at its root, and the effect
  of cache invalidation (RMT-026) on P2 metadata are unspecified.

### PKG-JVM-022 · Force authentication for P2
Priority: **P2**

| Setting | Meaning | Default |
|---|---|---|
| Force authentication | With anonymous access enabled, P2 clients are not asked for credentials by default; this setting makes the system request credentials before giving access to the repository. | off |

- UI: the new and edit dialogs of P2 virtual repositories. The repository configuration also
  carries the switch on local and remote repositories (REPO-028 in
  [../repositories.md](../repositories.md)).
- Eclipse can work as a logged-in user or anonymously; anonymous use depends on the global
  anonymous-access setting (IAM-005 in [../identity-and-access.md](../identity-and-access.md)).

### PKG-JVM-023 · Eclipse and Tycho client configuration
Priority: **P2**

- Eclipse: Help, Install New Software, Add; enter a name and the P2 virtual repository's URL;
  Eclipse then queries the system and lists the available software.
- Tycho: in the build's `pom.xml`, a repository with layout `p2` pointing at the P2 virtual
  repository resolves Tycho dependencies through the system:

```xml
<repository>
  <id>p2-virtual</id>
  <layout>p2</layout>
  <url>https://repo.example.com/p2/p2-virtual/</url>
</repository>
```

- The system hosts Tycho build output in local Maven or generic repositories and exposes it
  through a P2 virtual repository (PKG-JVM-019).
- The client setup dialog offers these snippets with values filled in (UI-021 in
  [../ui-inventory.md](../ui-inventory.md)).

### PKG-JVM-024 · P2 in the configuration interfaces
Priority: **P2**

- The repository configuration API's format sets for local, remote, virtual and federated
  repositories do not list P2, although the UI offers P2 remote and virtual repositories (noted
  under REPO-006 in [../repositories.md](../repositories.md)).
- The configuration-as-code form accepts P2 as a remote and as a virtual repository format; a P2
  virtual repository carries a list of upstream URLs there. It has no P2 force-authentication
  switch.
- The repository configuration JSON reference carries a P2 force-authentication switch on local
  and remote repositories; the create schema has it on local repositories only. Neither offers it
  on virtual repositories, where the UI shows it (PKG-JVM-022).
- How a P2 repository is created through the API is therefore unspecified.

## Notes for implementers

- **Gradle reads `.module` files before POMs.** A POM with Gradle's marker comment makes Gradle
  request the `.module` file; a repository that rejects or loses it breaks Gradle consumers while
  Maven consumers keep working (PKG-JVM-005).
- **Ivy-style paths are not Maven paths.** Dotted organizations, `<type>s` folders, `ivy-<rev>.xml`
  descriptors and 14-digit timestamps all have to pass upload validation and layout parsing;
  checks written for Maven paths (POM consistency, Maven snapshot expressions) must not reject them
  (PKG-JVM-004, PKG-JVM-009).
- **sbt needs an Ivy proxy even for Maven-only builds.** sbt itself and its plugins come from an
  Ivy-layout upstream with Scala- and sbt-version folders; a proxy that only understands Maven
  paths answers `404` and they do not resolve (PKG-JVM-015).
- **The default sbt layout is a dead end.** Packages stored under it are invisible to retention
  and archiving; a layout change needs an index recalculation to take effect (PKG-JVM-013).
- **Ivy dynamic revisions need folder listings.** Without upstream folder listing on the remote
  repository, `latest.*` and range lookups find nothing (PKG-JVM-010).
- **Credentials can be matched by realm.** Ivy-based clients pick credentials by realm and host; a
  realm in the authentication challenge that differs from the one in the client setup snippet can
  make publishing fail with `401`. Announce one fixed realm and print exactly that string in the
  snippets (PKG-JVM-014).
- **P2 virtual repositories create remote repositories.** Adding an upstream P2 URL can create
  several remote repositories, one per upstream base URL; creating duplicates by hand for the same
  base URL breaks the virtual repository (PKG-JVM-019, PKG-JVM-020).
- **P2 sites can live inside archives.** Upstream URLs and path suffixes may point into a `.zip`
  with `!`; the system must resolve metadata and artifacts inside the archive (PKG-JVM-018,
  PKG-JVM-019).
- **The Gradle setup snippet is not validated.** A Maven/Ivy selection that does not match the
  repository's layout produces a working-looking snippet that resolves at the wrong paths
  (PKG-JVM-003).
