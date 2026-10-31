# Security scanning and curation

This page inventories what a full-featured repository manager offers around software supply-chain
security, at capability level: the package risk data that every decision is based on; upstream
curation, which evaluates a package at the remote-repository boundary before it reaches a client
or the cache; scanning of stored content (indexing on upload and on cache, scan policies, scan
scopes, violations); policy-based download blocking, including blocking of content that has not
been scanned yet; exceptions (waivers on curation policies, ignore rules on violations); software
bills of materials (SBOM) import and export; license compliance; reports and searches over scan
results; and the deeper analyses a scanning engine can add. Remote-repository mechanics live in
[remote-repositories.md](remote-repositories.md), the per-repository indexing switch is REPO-021 in
[repositories.md](repositories.md), the scan-related roles are IAM-008 in
[identity-and-access.md](identity-and-access.md), and the curation and scan-status events are
EVT-014 and EVT-015 in [events-and-extensibility.md](events-and-extensibility.md).

## Package risk data

### SEC-001 · Package risk data source
Priority: **MVP**

Upstream curation and scanning both decide against a database of facts per package and per package
version that is maintained outside the request path and updated continuously. It merges public
and software-vendor security advisories, open-source vulnerability, malicious package and license
data, enriched by the data provider's own research (which can also re-rate the
severity of a CVE). Upstream curation cannot evaluate anything without it: when the data source is
unreachable, the curation health check (SEC-007) reports the failure.

<details>
<summary>Facts the evaluators read from the data source</summary>

| Fact | Used by |
|---|---|
| Known vulnerabilities per version: identifier (CVE or a provider-specific identifier), CVSS base score and severity, fixed versions | Vulnerability conditions (SEC-013), security rules (SEC-051) |
| EPSS score per CVE | Relaxation of vulnerability conditions (SEC-013) |
| Whether a CVE is listed in the CISA Known Exploited Vulnerabilities catalog | Security rules (SEC-051), scan result badges (SEC-049) |
| Malicious flag per package version | Malicious package condition (SEC-012), security rules (SEC-051) |
| Declared and detected licenses, custom license assignments | License conditions (SEC-016), license rules (SEC-052) |
| Version list, release date of each version, latest version | Package age conditions (SEC-014), compliant version fallback (SEC-025) |
| End-of-life marker per version | End-of-life condition (SEC-015) |
| OpenSSF Scorecard results of the source project | Project health condition (SEC-017) |
| Official-image status of Docker Hub images | Image provenance condition (SEC-017) |
| SHA-256 checksum per version | Package integrity condition (SEC-019) |
| Labels on package versions | Label list conditions (SEC-018), waivers (SEC-028) |
| Copyright statements and full license texts | License attribution report (SEC-071) |
| Maintenance signals: commits and committers per year, release cadence, number of newer versions | Operational risk rules (SEC-053) |

</details>

Ecosystem identification: a remote repository is matched to an ecosystem of the data source by its
configured upstream URL. Recognised upstream URLs include, for example,
`https://registry.npmjs.org` (npm), `https://www.nuget.org` and `https://api.nuget.org` (NuGet),
`https://files.pythonhosted.org` (PyPI), `https://proxy.golang.org` (Go),
`https://repo1.maven.org/maven2` and several other public Maven hosts (Maven, Gradle, sbt),
`https://registry-1.docker.io` (Docker Hub), `https://rubygems.org`, `https://index.crates.io` and
`https://static.crates.io` (Cargo), `https://repo.anaconda.com/pkgs/main` and
`https://conda.anaconda.org/conda-forge` (Conda), `https://open-vsx.org` (editor extensions),
`https://huggingface.co`, `https://cran.r-project.org`, `https://pub.dev`, Conan Center,
`https://github.com` and `https://bitbucket.org` (Composer sources), `https://dhi.io` (Docker
Hardened Images), `https://api.ngc.nvidia.com` (NVIDIA NIM), and OS package mirrors (Alpine,
Debian, Ubuntu, NodeSource, the PostgreSQL APT repository and others). Some upstreams are matched
to a sub-ecosystem with its own advisory data (for example a hardened Maven library host). A remote
repository whose upstream is not recognised is *uncovered*; its packages can only be evaluated on
demand (SEC-039).

Performance: an evaluation takes about 100 milliseconds per package request on average.

Edge cases: a package version published very recently may not be in the data source yet; this is
the pending-data state (SEC-022).

### SEC-002 · Online synchronization of the vulnerability database
Priority: **MVP**

In online mode (the default), the system polls the data provider for updates about every hour and
applies them incrementally, without operator action. The data provider can shorten the interval
during major incidents. Each applied update triggers continuous re-evaluation of stored content
(SEC-048). Outbound access to the provider's endpoints must be allowed.

| Setting | Meaning | Default |
|---|---|---|
| Synchronization mode | Online (automatic) or offline (manual, SEC-003). | online |

Operations:

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| Get synchronization status | none | State of the current or last synchronization run | administrator |
| Start synchronization | none | Starts an online run unless one is already running; returns its status | administrator |

UI location: administration, scanner settings, database synchronization (last update time and
result of the last run).

### SEC-003 · Offline synchronization of the vulnerability database
Priority: **MVP**

For air-gapped installations: an update package is downloaded with the companion command-line
tool on a connected machine, carried to the isolated installation and applied from the
administration UI, which then verifies the result. After switching to offline mode the UI shows
the download command to use. Offline mode is meant only for installations whose network access is
restricted, and offline updates should be applied often, since detection is only as current as
the last applied package. Continuous re-evaluation (SEC-048) runs after an update package is
applied.

Unspecified: package format, size, whether partial (incremental) offline packages exist.

### SEC-004 · Malicious package intelligence
Priority: **MVP**

A malicious package carries intentionally harmful code or behavior. The registry consumes a
malicious flag per package version from the data source (SEC-001) and acts on it in curation
(SEC-012) and in scan rules (SEC-051); when a package is newly flagged, stored content that
contains it is re-evaluated (SEC-048).

How the data provider produces the flag:

- Automated scanners score every new or updated package of the continuously covered registries.
  A very high score marks the package malicious automatically and it is disclosed to the
  registry's maintainers; it appears in the data within 2 to 4 hours of publication. A somewhat
  high score sends the package to manual review, with a result within 1 to 3 days. A low score
  counts as clean.
- Open-source malicious-package feeds, such as the
  [OpenSSF malicious-packages repository](https://github.com/ossf/malicious-packages), and a
  daily review of third-party disclosures add further entries.
- Code patterns searched for: obfuscation, generated domain names, dynamic code evaluation,
  download-and-execute, reverse or bind shells, reading sensitive files (say `/etc/shadow` or a
  browser's stored passwords), harvesting of environment variables or personal
  data, cryptomining, use of known exfiltration services.
- Metadata patterns: dependency confusion (for example unusually high version numbers),
  typosquatting (names close to a popular package), code that runs at install time.

| Registry | Coverage |
|---|---|
| npm, PyPI, Open VSX | Continuous: every new package is polled and scanned. |
| Hugging Face | Continuous, triggered by push notifications from the registry. |
| NuGet, Maven, RubyGems, crates.io, Go modules, Packagist | Well-known only: known malicious packages are listed, no continuous scanning. |
| Others (for example CocoaPods) | Not covered. |

Edge cases: a package flagged after it was cached stays in the cache; whether it is still served
depends on enforcement on cached packages (SEC-023) or on a scan policy with download blocking
(SEC-061).

### SEC-005 · Package labels
Priority: **P2**

A label is a named, described group of package versions used to build allow lists and block lists
and to waive policies in bulk. Any package version can carry several labels.

- Private labels are created by the organisation; public labels are predefined by the data
  provider, read-only and applied automatically (for example to mark packages of a known attack
  campaign).
- Label management screen: list all labels; create a label (name, description); edit a label;
  open a label to see its packages with the associated versions and the last-edited and
  last-updated times; add packages and versions; remove single versions or a whole package from
  the label. A label can also be added from a package version's detail view (pick an existing
  label or create one). Removing a label from a version does not delete the label.
- Automation: an API can create and delete labels, assign and remove them on package versions,
  and query packages by label (through a GraphQL query interface).

Interactions: label list conditions (SEC-018), label waivers and approved waiver requests stored as
labels (SEC-028, SEC-029), affected-resource search by label (SEC-074), label distribution to
other sites (SEC-040).

### SEC-006 · Custom licenses
Priority: **P2**

Organisations define licenses the detection does not know (internal terms, proprietary
agreements) and assign them to packages, so curation and scan policies enforce them and SBOMs
show them.

<details>
<summary>Custom license definition</summary>

| Field | Meaning | Default |
|---|---|---|
| Name | Unique identifier; may contain the special characters of SPDX license expressions. A second license with the same name is rejected. | none (required) |
| Full text | The legal text; rich text and Markdown allowed. | none (required) |
| Category | One value of a predefined list, for example Permissive, Copyleft, Proprietary. | none (required) |
| Expiration date | End of a time-limited entitlement; afterwards the license is no longer enforced. | none |
| Full name, description | Long name and description (scanner-side license form). | empty |
| Aliases | Alternative names; an alias must be unique across all licenses. | empty |
| References | Fully qualified references to components that use the license (scanner-side license form). | empty |

</details>

Assignment:

- From the license list to many packages or versions at once, or from a package version's
  licenses view.
- Scope: one version, a version range, or all versions of the package.
- Mode: *add* (the custom license coexists with the detected licenses) or *replace* (it overrides
  them; the UI still shows that a detected license was overridden).
- Several licenses on one package are combined with *and* (all apply) or *or* (any one applies).
- The same license cannot be assigned twice to one package.

Expiry: warnings 90, 60, 30 and 10 days before the expiration date; expiring licenses are
highlighted in the license list and on the package. An expired license stays visible for audit,
is marked expired and is no longer enforced.

Evaluation order: if a package has a custom assignment, policies use it with its mode; otherwise
the detected license is used. SBOM exports show a custom license next to (add) or instead of
(replace) the detected one; changes appear in later exports.

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| List custom licenses | none | Licenses with name, created date, creator, last modified, expiration, number of associated packages | read access to the license data |
| Get custom license | name | The license | read access |
| Create custom license | name, text, category | The license | administrator |
| Update custom license | name; changed fields | The license | administrator |
| Delete custom license | name | Fails while the license is still assigned to packages | administrator |
| Assign a known license to a component | component identifier, license | Assignment; the license must already exist | unspecified |

UI location: package intelligence, license settings (SEC-090); scanner administration, license list.

### SEC-090 · Package intelligence browser
Priority: **P2**

A UI over the package risk data (SEC-001) that works independently of what the registry stores.
Teams use it to vet a package before requesting it, to understand a curation block and to pick a
version for a waiver request (SEC-029).

- Search for packages and for vulnerabilities; vulnerability entries carry the data provider's
  enrichment.
- Package detail: vulnerabilities of the package itself and those it inherits through its
  dependencies, with mitigation notes; dependencies as a table (direct and indirect) and as a
  graph; licenses; the OpenSSF Scorecard result; labels (SEC-005).
- Side-by-side comparison of several packages or versions by vulnerabilities, dependencies and
  licenses.
- License review request: a user flags a missing or wrong license and is notified once the data
  provider has corrected it. That is a service run by the data provider; for a self-hosted
  repository manager the local equivalent is a custom license assignment (SEC-006).
- Management entry points for labels (SEC-005) and custom licenses (SEC-006). Results from
  on-demand evaluation appear in a separate tab (SEC-039).
- Bulk queries through a GraphQL interface.

## Upstream curation: setup and policies

### SEC-007 · Upstream curation service
Priority: **P1**

Upstream curation evaluates every package requested through a curated remote repository against
the curation policies that apply to it. Without enforcement on cached packages (SEC-023) the
evaluation happens on a cache miss, before the system fetches the package from the upstream;
with it, cache hits are evaluated too. Upstream curation governs the remote-repository boundary only; local
repositories are protected by scanning (SEC-042 onwards).

Outcomes of one evaluation:

| Outcome | Meaning |
|---|---|
| Approved | No applicable policy blocks; the package is served. |
| Bypassed | A policy matched, but a relaxation (SEC-011) let the package through; recorded among approved events. |
| Blocked | At least one blocking policy matched; the client receives a refusal with a reason (SEC-021). |
| Dry run | A dry-run policy matched; the package is served and the match is recorded. |
| Not inspected | No policy applied to the request. |

| Setting | Meaning | Default |
|---|---|---|
| Curation enabled | Global switch for the curation service. | unspecified |

Health check: an operation returns one entry per component the curation service depends on, each
either OK or an error text. The only check with meaning here is whether the curation evaluator can
reach the risk data source (SEC-001). The remaining checks concern entitlement plumbing, both an
entitlement service and its client inside the repository process, and are out of scope (SEC-088).

UI location: administration, curation, general.

Interactions: remote repository request path, RMT-001 in
[remote-repositories.md](remote-repositories.md); permissions on remote repositories, IAM-023 in
[identity-and-access.md](identity-and-access.md).

### SEC-008 · Curated remote repositories
Priority: **P1**

An administrator chooses which remote repositories curation protects.

- Connect individual repositories (a toggle per row), connect all repositories currently listed
  for a package type (bulk action), or connect a package type for *current and future*
  repositories, which also connects every remote repository of that type created later.
  Individual repositories of a connected package type can still be disconnected, singly or in
  bulk.
- A connected repository can be governed, but is governed only once a policy scope covers it
  (SEC-010).
- Disconnecting a repository takes it out of every policy; a policy that covered only that
  repository becomes inactive (SEC-009). Disconnections trigger an email to the configured list
  (SEC-034).
- Prerequisite: only remote repositories that cache content can be curated; a cache period of at
  least 30 days is recommended.
- Overview: counts of connected and not-connected repositories and a trend graph of connections
  over time (the trend needs two months of data before it shows), then a table of package types
  with their repositories.
- Inside a project context the connection toggles are read-only; changes are made globally.

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| List remote repositories with curation state | none | Every remote repository with its curation information; an empty list when no remote repository exists | unspecified |

Connecting and disconnecting are UI actions; an API for them is unspecified.
Connection changes are restricted to the curation administrator (SEC-035).

UI location: administration, curation settings, remote repositories.

Edge cases: a remote repository that points at a curated remote repository of another instance
(a chained instance, see [remote-repositories.md](remote-repositories.md)) must forward the
requesting user's identity for pass-through to work (SEC-024).

### SEC-009 · Curation policy
Priority: **P1**

A curation policy binds exactly one condition to a scope and an action. It is created in a
wizard with the steps name, scope, condition, waivers, actions and notifications, and sharing.

<details>
<summary>Curation policy settings</summary>

| Setting | Meaning | Default |
|---|---|---|
| Name | At most 50 characters, no special symbols. | none (required) |
| Enabled | Whether the policy is evaluated. | unspecified |
| Scope | Which requests the policy governs (SEC-010). | none (required) |
| Condition | One built-in or custom condition (SEC-011). | none (required) |
| Waivers | Package, version or label exceptions (SEC-028). | none |
| Action | *Block* or *dry run* (SEC-021). | unspecified |
| Block for pending packages | Block while the package is not yet in the data source (SEC-022). Block action only. | unspecified for new policies |
| Block for on-demand pending packages | Block while an on-demand scan is pending (SEC-022, SEC-039). Block action only. | unspecified for new policies |
| Enforce policy on cached packages | Also evaluate cache hits (SEC-023). | off |
| Waiver requests | *Not allowed*, *manually approved* or *automatically approved* (SEC-029). | unspecified |
| Decision owners | Groups that decide waiver requests; required for manual approval. | none |
| Maximum waiver duration | 1 to 365 days; empty means waivers are permanent (SEC-030). | none |
| Notification emails | Addresses notified about this policy's events. | none |
| Share with other sites | Distribute the policy to follower sites (SEC-040). | off |

</details>

Behavior:

- Every policy is evaluated independently. A package is blocked when any blocking policy blocks
  it; one package can be blocked by several policies at once, each with its own decision
  owners.
- The policy list has two tabs: *active* (policies that cover at least one repository) and
  *inactive* (policies that cover none). Each row shows name, scope, condition, action and source
  (local or received from another site). Row actions: edit (same wizard), delete (with
  confirmation). An inactive policy cannot be revived.
- Policies received from another site are partly read-only (SEC-040).

UI location: curation, policies management.

Interactions: policy-change events, EVT-014 in
[events-and-extensibility.md](events-and-extensibility.md).

### SEC-010 · Curation policy scope
Priority: **P1**

| Scope | Covers |
|---|---|
| Organization-wide | All current and future curated repositories. Repositories, groups and projects can be excluded. |
| Specific projects | Curated repositories that belong to the selected projects. Repositories shared into a project are not covered. |
| Specific remote repositories | The selected curated remote repositories. |
| Package types | All curated repositories of the selected package types. |
| Selected groups | Requests by users who belong to at least one selected group. Requires enforcement on cached packages to be enabled globally (SEC-023). |

Rules:

- Group exclusion beats group inclusion: a user in both an included and an excluded group is not
  subject to the policy.
- A project-scoped policy is saved only if at least one selected project owns a curated repository
  that is not merely shared into it.
- On an organization-wide policy, excluding a project also excludes the repositories shared into
  that project.
- If the project lookup fails during a download, project-scoped policies do not apply to that
  repository; organization-wide policies still apply and their project exclusions are ignored for
  that download.

Edge cases: with compliant version fallback (SEC-025), group scopes and enforcement on cached
packages combined for npm, a compliant version fetched for one group can be served to other
groups from the cache; each request is still evaluated against the requesting user's policies and
blocked where they conflict.

### SEC-011 · Curation condition model
Priority: **P1**

A condition is the test a policy applies to a requested package version. Conditions fall into
three categories: *security*, *legal* and *operational*. Built-in conditions are ready to use;
custom conditions are built from templates with parameters (SEC-020). Some conditions offer
*relaxations*: extra parameters that let a matching package through when it also meets a
mitigating criterion, so broad policies produce fewer false positives. A package let through by a
relaxation is recorded as bypassed.

<details>
<summary>Built-in conditions</summary>

| Category | Condition | Blocks | Custom variant | Relaxations | Package types | Entry |
|---|---|---|---|---|---|---|
| Security | Malicious package | Versions flagged malicious. | no | none | all | SEC-012 |
| Security | Vulnerability with CVSS score | Versions with a known CVE, any CVSS score combination. | yes | EPSS score; allow if no fixed version exists; allow if the CVE already exists in the organisation's repositories | all except Docker | SEC-013 |
| Security | Vulnerable to a specific CVE | Versions affected by a configured CVE identifier. | yes | none | all except Docker | SEC-013 |
| Security | OpenSSF Scorecard | Packages failing one or more selected Scorecard checks. | yes | disregard if no score exists | all except Docker | SEC-017 |
| Security | Block packages on labels | Packages on a blocked list (SEC-005). | yes | none | all except Docker | SEC-018 |
| Security | Allow only packages on labels | Every package not on an allowed list. | yes | none | all except Docker | SEC-018 |
| Security | Package integrity violation | Cached versions whose SHA-256 differs from the reference. | yes | skip if data is missing | listed in SEC-019 | SEC-019 |
| Legal | License block list | Versions with a license on the list (custom licenses included). | yes | allow if one or more of the package's licenses is not on the list | all except Docker | SEC-016 |
| Legal | License allow list | Versions with a license not on the list (custom licenses included). | yes | allow if at least one of the package's licenses is on the list | all except Docker | SEC-016 |
| Legal | No identified license | Versions without a license, or whose license cannot be identified. | no | none | all except Docker | SEC-016 |
| Operational | Image is not an official Docker Hub image | Docker Hub images without official-image status. | no | none | Docker | SEC-017 |
| Operational | Aged version, no newer version | Versions older than 2 years for which the package has no later release. | no | none | all except Docker | SEC-014 |
| Operational | Aged version, newer version available | Versions whose release lies over 180 days before the release of the package's newest version. | no | none | all except Docker | SEC-014 |
| Operational | Immature version | Versions younger than a number of days. | yes | allow if the version fixes a CVE present in the organisation's repositories | all except Docker | SEC-014 |
| Operational | Single version only | Packages that have only one version. | no | none | all except Docker | SEC-015 |
| Operational | End-of-life version | Versions marked end-of-life. | no | none | npm, PyPI, NuGet, RubyGems, Composer, Cargo | SEC-015 |

</details>

Built-in conditions cannot be edited or deleted. Each policy references exactly one condition, so
combining criteria means several policies or one custom condition with relaxations.

### SEC-012 · Malicious package condition
Priority: **MVP**

Blocks any version of a third-party package that the data source flags as malicious (SEC-004).
Built-in only, no relaxation, all package types.

- It is the only condition that also blocks pass-through traffic (SEC-024).
- Typical rollout: a malicious-package policy in block mode first, other conditions in dry-run
  mode until their impact is known.
- On-demand evaluation (SEC-039) also evaluates maliciousness for uncovered packages.

### SEC-013 · Vulnerability conditions
Priority: **P1**

Two families:

1. *Vulnerability with CVSS score*: blocks versions with a known CVE according to its CVSS score.
   The built-in variant matches every score combination; the custom template sets a CVSS score
   range. Relaxations: take the EPSS score into account; allow when no fixed version exists;
   allow when the same CVE already exists in the organisation's repositories (the template calls
   this a cache dependency).
2. *Vulnerable to a specific CVE*: blocks versions affected by one configured CVE identifier.

Both apply to all package types except Docker in the regular flow. On-demand evaluation (SEC-039)
also covers Docker images, but counts only vulnerabilities that carry a CVE identifier.

Unspecified: the exact EPSS threshold semantics, which CVSS version is used when several exist
(the scores are NVD CVSS scores).

### SEC-014 · Package age conditions
Priority: **P1**

| Condition | Rule | Parameters |
|---|---|---|
| Immature version | Blocks a version whose release date is fewer than N days ago. | Built-in variants *permissive* (2 days), *moderate* (14 days), *strict* (30 days); a custom condition sets any N. Relaxation: let the version through if it fixes a CVE (above a CVSS score) that exists in the organisation's repositories. |
| Aged version, no newer version | Blocks a version older than 2 years when the package has published nothing after it (a sign of an unmaintained package). | none |
| Aged version, newer version available | Blocks a version whose release date lies over 180 days before that of the package's newest version. | none |

Release dates come from the data source. For uncovered packages (SEC-039) the stored item's
creation date is used, which reflects the upstream release date only when the upstream delivers
it; otherwise it falls back to the local download time, which can produce false immaturity
blocks.

Interactions: compliant version fallback (SEC-025) makes an immaturity policy practical, because
clients resolving a range receive the newest version that is old enough instead of an error.

### SEC-015 · Single-version and end-of-life conditions
Priority: **P2**

- *Single version only*: blocks packages that have exactly one published version (often immature
  or abandoned). Built-in only, all types except Docker.
- *End-of-life version*: blocks versions the data source marks end-of-life (typical causes: an
  archived upstream project, a release line without further support). The block message names the
  reason and recommends upgrading. Built-in only, no relaxation, cannot serve as a template for a
  custom condition; to allow a specific end-of-life package or version, waive it (SEC-028). Package
  types: npm, PyPI, NuGet, RubyGems, Composer, Cargo. Not evaluated for uncovered packages
  (SEC-039).

### SEC-016 · License conditions
Priority: **P2**

| Condition | Rule | Relaxation |
|---|---|---|
| License block list | Blocks versions carrying a listed license. | Allow if at least one of the package's licenses is not on the list. |
| License allow list | Blocks versions carrying a license that is not listed. | Allow if at least one of the package's licenses is on the list. |
| No identified license | Blocks versions with a missing or unidentifiable license. | none |

Custom licenses (SEC-006) are offered in the license lists and evaluated with their add or replace
mode. The list conditions are available as the custom template *allowed and banned lists for
licenses*. Package types: all except Docker in the regular flow; on-demand evaluation (SEC-039)
also evaluates licenses of Docker images.

### SEC-017 · Project health and image provenance conditions
Priority: **P2**

- *OpenSSF Scorecard*: blocks packages whose source project fails one or more selected
  [OpenSSF Scorecard](https://github.com/ossf/scorecard) checks. Custom template available.
  Relaxation: disregard the condition when no score exists. Not supported for uncovered
  packages (SEC-039). All types except Docker.
- *Not an official Docker Hub image*: blocks Docker Hub images that do not have Docker's
  official-image status. Built-in only.

Maintainer signals beyond these (commit and committer counts, release cadence) are available only
as scan rules (SEC-053).

### SEC-018 · Label list conditions
Priority: **P2**

- *Block packages on labels*: a block list built from labels (SEC-005).
- *Allow only packages on labels*: an allow list; every package version without one of the
  selected labels is blocked.
- Custom template *allowed and banned lists for packages*; a related template *block specific
  package versions* targets named versions directly.

All types except Docker.

### SEC-019 · Package integrity condition
Priority: **P2**

Compares the SHA-256 checksum computed for a cached package with the SHA-256 the data source holds
for that version, to catch content that was altered in transit or upstream.

- Evaluated only for a package that the remote repository has cached and whose SHA-256 the
  repository can supply.
  A policy using this condition can be saved only when enforcement on cached packages is enabled
  both globally and on the policy (SEC-023).
- The built-in condition has *skip if data is missing* on: when the data source has no checksum,
  the check is skipped. A custom condition can turn this off, so unverifiable packages are blocked.
- On mismatch the client receives a message that the integrity check failed; the audit event
  shows both checksums.
- Not evaluated during compliant version fallback (SEC-025), which has no cached checksum.

Package types: npm, PyPI, Maven, Go, NuGet, Conan, RubyGems, Gradle, sbt, Cargo, Conda, editor
extensions, Alpine, Dart pub, Composer, CRAN.

### SEC-020 · Custom conditions
Priority: **P2**

A custom condition is one template plus parameter values. Templates include:
OpenSSF Scorecard; allowed and banned lists for packages; block specific package versions; allowed
and banned lists for licenses; vulnerability by CVSS score range; version maturity; vulnerability
to a specific CVE; package integrity. The set grows over time. Each template defines its
parameters, which are required or optional, and its relaxations.

Creating: pick a template, fill in its fields and relaxations, save; the condition appears under
custom conditions and can be referenced by policies. Custom conditions can be edited at any time
(name and parameter values) and deleted.

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| List condition templates | none | Templates with their parameters | unspecified |
| List conditions | pagination, filters | Built-in and custom conditions | unspecified |
| Get condition | condition id | Condition with parameter values | unspecified |
| Create custom condition | template id, name, parameter values | Condition | curation administrator or policy manager (SEC-035) |
| Update custom condition | condition id, name and/or parameter values | Condition; built-in conditions are refused | curation administrator |
| Delete custom condition | condition id | Built-in conditions are refused | curation administrator |

The read privileges are unspecified: the role table (SEC-035) denies the policy viewer any access to
conditions in the UI, so a viewer may not be able to list them.

UI location: curation settings, conditions.

Unspecified: whether a condition referenced by a policy can be deleted.

## Upstream curation: enforcement behavior

### SEC-021 · Block and dry-run actions
Priority: **P1**

- *Block*: the download is refused with HTTP 403 and a human-readable reason. Native clients show
  it differently: npm reports `E403` with the reason, Maven and Gradle report a failed transfer with
  status 403, pip, `dotnet` and the others report 403. When compliant version fallback (SEC-025)
  removes versions from metadata, some requests fail earlier with "no matching version" instead.
- *Dry run*: the package is served normally; the match is recorded in the dry-run view of the audit
  log (SEC-032) and can be emailed (SEC-034). Dry run exists to measure a policy's impact before
  switching it to block. A dry-run policy never blocks, and it always skips packages in a pending
  state (SEC-022).

Edge cases: one blocked request can be caused by several policies; the audit event and the
blocked-package event list all of them with their condition, category and dry-run flag.

### SEC-022 · Pending-data handling
Priority: **P1**

A package version can be requested before the data source knows it. Two pending states exist:

| State | Meaning | Typical duration |
|---|---|---|
| Pending data-source update | The data source has not ingested the version yet (common for versions published minutes ago). | Up to four hours, usually two to three. |
| Pending on-demand data | An on-demand scan of an uncovered package is still running (SEC-039). | Until the scan finishes; see SEC-039 for timeouts. |

Each blocking policy decides separately for each state:

- *Block* (checkbox selected): missing data counts as a violation of this policy; the package stays
  blocked until its data exists.
- *Skip* (checkbox cleared): this policy is left out while data is missing; the remaining policies
  are still evaluated.

Rules:

- Policies decide independently. When some policies block a pending package and others skip it,
  only the blocking ones appear as the cause, and a waiver request is routed to their decision
  owners.
- Only a block in the *pending data-source update* state can be waived (SEC-031); an on-demand
  pending block cannot.
- Both checkboxes exist only for the block action; dry-run policies always skip.
- Migration from a single global setting: these per-policy controls replace it, and its values
  map as follows: *always allow* becomes skip; *always block* (the former default) and *allow if
  no blocking policy on the remote* both become block; dry-run policies become skip.

Recommended handling: wait for the data (or pin an already known version), let selected policies
skip pending packages, or keep blocking and use waivers.

### SEC-023 · Enforcement on cached packages
Priority: **P1**

Without it, curation evaluates only packages fetched from the upstream; a package already in the
remote repository's cache keeps being served even if its risk changed later (new CVE, newly
flagged malicious, waiver expired). With it, cache hits are evaluated too.

Two levels:

| Setting | Level | Meaning | Default |
|---|---|---|---|
| Curation for cached packages | Global | Makes the per-policy option available; enforces nothing by itself. | off (opt-in) |
| Enforce policy on cached packages | Policy | Applies this policy to cache hits as well. | off (opt-in) |

Required by: group scopes (SEC-010), time-limited waivers (SEC-030, enabling a duration switches
the policy option on), the package integrity condition (SEC-019) and on-demand evaluation
(SEC-039).

Audit: every event records its origin: fetched from the upstream into the cache, served from the
cache, or simulated by a dependency pre-check (SEC-038). The origin is filterable.

UI location: administration, curation, general (advanced coverage); policy wizard, actions and
notifications.

### SEC-024 · Pass-through evaluation mode
Priority: **P2**

Some build tools (for example Maven) must download every dependency, including those that
violate policies, to audit a project. In pass-through mode the remote repository streams such
packages to the client without storing them in the cache, so evaluation does not populate the
cache with violating content.

- Only policies built on the malicious-package condition (SEC-012) can block pass-through
  traffic; all other conditions (vulnerability, license, operational) are bypassed in this mode.
- Enabled by default when the repository's package type is connected to curation; can be switched
  off per remote repository (a switch at the end of the remote repository settings).
- Works when the remote repository talks to curation directly and when a remote repository of
  another instance (a chained instance) sits in front; the downstream remote repository then needs
  its "forward user context to curation" option on.
- Some package types need pass-through for the dependency pre-check (SEC-038) to work.

| Setting | Scope | Meaning | Default |
|---|---|---|---|
| Curation pass-through | Remote repository | Stream audited packages without caching them. | on once the package type is connected |
| Forward user context to curation | Remote repository pointing at another instance | Pass the requesting user to the upstream instance's curation. | unspecified |

### SEC-025 · Compliant version fallback
Priority: **P1**

When a client resolves a version range and the version it would pick is blocked, the build does not
fail: the system steers the client to the newest version that no policy blocks.

How it works:

1. A client asks for version-discovery metadata (an npm package document, `maven-metadata.xml`,
   a PyPI simple or JSON index page, a NuGet v3 version list, a Go version list, a RubyGems compact
   index entry, Composer v2 metadata, a Cargo sparse-index entry, Conda repository data, an Alpine
   index, editor extension metadata).
2. The system takes the versions, evaluates them against every active policy of the repository
   (security, license, operational), starting from the newest, and removes blocked versions from
   the metadata until it reaches the first compliant version.
3. The client resolves its range against the filtered list and so picks the highest compliant
   version, for direct and transitive dependencies alike.
4. The developer is not told that a different version was delivered; the audit log (SEC-033)
   records the inspection and the selected version.
5. If a removed version is later re-evaluated and passes, requests for it succeed normally.

| Setting | Meaning | Default |
|---|---|---|
| Compliant version fallback | Global switch in the curation general settings. | off for most formats; on for npm (enabled by default to protect against malicious metadata attacks) |

Limits and edge cases:

- An exact or locked version is never substituted: lock files, exact pins and direct file URLs
  bypass version discovery and fail with 403 if the version is blocked.
- Older blocked versions below the first compliant version stay in the metadata, so listings can
  still show blocked versions; downloading them fails with 403.
- When every version is blocked, behavior differs per format: for npm the package document is not
  returned at all and the audit records version *All* (a waiver can then be requested for a chosen
  version, SEC-031); for Maven the full metadata is kept and the download is blocked.
- The package integrity condition (SEC-019) is not evaluated in this flow.

| Package type | Support |
|---|---|
| npm, PyPI, Maven, Gradle, sbt, NuGet (v3 feeds only), RubyGems, Composer (v2 only), Go, Cargo, Alpine | full |
| Conda | partial: only the immaturity condition, only without its CVE relaxation; sharded repository data not supported |
| Editor extensions (Open VSX) | partial |
| Go modules that publish only pseudo-versions | not supported |

### SEC-026 · Compliant version fallback per client
Priority: **P1**

What native clients observe when the newest version is blocked and fallback is on. The npm and
NuGet rows matter for trove's first formats; the rest is reference.

<details>
<summary>npm (npm and Yarn clients)</summary>

| Client action | Fallback applies | Observed result |
|---|---|---|
| `npm install <pkg>`, `npm install <pkg>@<range>`, `npm update`, `npm outdated`, `npm dedupe`, `npm install --package-lock-only` without a lock file | yes | Highest compliant version installed or shown; no error. |
| `npm install <pkg>@latest` when `latest` points at a blocked version | yes | The `latest` dist-tag is recomputed to the newest compliant version. |
| `npm install <pkg>@next` (or any non-`latest` dist-tag) pointing at a removed version | no | Other dist-tags are not recomputed; resolution fails with `ETARGET`. |
| Exact version that was removed from the document | no | `ETARGET` (no matching version). |
| Exact version that is blocked but still listed (older than the first compliant version) | no | Download fails with `E403` and the block reason. |
| Range whose only candidates are blocked | no | `ETARGET`. |
| Range that covers only older versions below the first compliant one (never checked by the filter) | no | Resolution succeeds on a blocked version; its download fails with `E403`. |
| `npm ci` with a lock-file version never compliant | no | `ETARGET`. |
| `npm ci` with a lock-file version that became blocked | no | Download fails with `E403`. |
| Pre-release exact version, tarball URL, `npm pack <pkg>` | no | Treated as exact; `E403` if blocked. |
| `npm view <pkg> versions` | yes | Recent blocked versions are missing; older blocked ones can remain. |

npm fetches the whole package document in one request; filtering that document is what makes
blocked versions look nonexistent.

</details>

<details>
<summary>NuGet (dotnet CLI; behavior with other NuGet clients is unspecified)</summary>

| Client action | Fallback applies | Observed result |
|---|---|---|
| `dotnet add package <id>` | yes | Highest compliant version installed. |
| `dotnet add package <id> --version <v>` | yes | `--version` means "at least `<v>`"; if the newest version is blocked the client reports NU1102; an older blocked version yields 403. An exact pin needs the bracket form `[<v>]`. |
| `dotnet restore` without a lock file | yes | Next compliant version. |
| `dotnet restore` with a lock file | no | Pinned versions downloaded; blocked ones fail with 403. |
| `dotnet package search` | no | Search results are not filtered; installing a blocked result fails with 403. |

Only NuGet v3 feeds are supported.

</details>

<details>
<summary>Other formats</summary>

| Format | Behavior |
|---|---|
| PyPI (pip, uv) | Unpinned and range requirements resolve to the next compliant version; `==` pins fail if blocked; `pip install --no-deps` filters only the main package, so transitive downloads can still fail with 403; `uv sync` uses locked versions unless `--upgrade` is given. Simple and JSON index both filtered. |
| Maven | Ranges, `RELEASE` and dynamic versions resolve to the highest compliant version (with `RELEASE` this can be a pre-release). Exact versions skip `maven-metadata.xml` and fail with 403. If the newest version in a range is clean, an older blocked version in the range is not filtered and can be chosen, then fails with 403. If nothing in a range is compliant, all are removed and resolution fails before any download. |
| Gradle | Dynamic versions (`1.+`, `latest.release`) and plugin versions are filtered; fixed versions and strict lock mode bypass filtering (403 on blocked). Curated Gradle plugins need a virtual repository with the Maven Central remote first and the plugin remote below it, because the client follows redirects to Maven Central for binaries. |
| sbt | Like Maven: dynamic versions and ranges filtered; exact versions fail on download. |
| Go | A plain `go get <module>` lands on the newest compliant version; with an explicit `@<v>` naming the blocked newest version the client reports the package as not found, and 403 when every version is blocked; `go get -u` does not upgrade when the current version is compliant and only the newest is blocked; `go mod download` fetches pinned versions (403 if blocked). |
| RubyGems | `gem install` and Bundler resolve to compliant versions; `gem update` discovers versions from an unfiltered list and installs from the filtered compact index, so it can report a misleading resolution error while enforcement is correct. |
| Composer | v2 only, Packagist metadata with GitHub and Bitbucket sources; with a lock file downloads continue and blocked ones fail with 403; `composer search` is not filtered. |
| Cargo | New resolutions pin the compliant version; an explicitly requested blocked version is reported as nonexistent; `cargo search` is not filtered; `cargo vendor` bypasses the repository. |
| Conda | Only the immaturity condition; `conda search` shows compliant versions only. |
| Alpine | Applies only through a virtual repository whose member is the Alpine remote. A blocked version is replaced in the index only by a compliant version already in the cache; without one it stays listed and its download is blocked. Only malicious and immaturity conditions are applied, and only to packages changed in the past 99 days (older entries pass unfiltered but stay subject to per-file download blocking); the edge branch and the newer v3 index format are not filtered at all. |
| Editor extensions (Open VSX) | Blocked newer versions disappear from version lists; background updates move only to the highest allowed version. |

</details>

### SEC-027 · Curated remote repositories inside virtual repositories
Priority: **P1**

Virtual repositories resolve local repositories first, then remote caches, then remote upstreams
(REPO-035 in [repositories.md](repositories.md)). A curated member affects the outcome:

| Virtual repository content | Result for a package that violates a policy and is not stored locally or cached |
|---|---|
| One curated remote repository | 403 |
| One remote repository of another instance that points at a curated remote | 403 |
| Several remote repositories, at least one curated | Members tried in order: found and allowed returns 200; not found (404) moves to the next member; blocked returns 403. |

Recommendations: one remote repository per upstream source inside a virtual repository; curated
remote repositories last in the order; every remote repository in the virtual repository curated,
so no uncurated path to the internet remains.

Edge cases: it is open whether a 403 from a curated member ends resolution or resolution
continues with the next member (ending in 404 if nobody serves the package); the spec decides.
Alpine compliant version fallback (SEC-026) works only through a virtual repository.

## Upstream curation: exceptions

### SEC-028 · Policy waivers
Priority: **P1**

A waiver exempts packages from one policy without changing its condition.

- *Package waiver*: a package, or specific versions of it, added directly to the policy.
- *Label waiver*: a label (SEC-005) attached to the policy; every version carrying the label is
  exempt. Suits bulk exceptions and reuse across policies.
- An approved waiver request (SEC-029) is stored as a label and can be reused or edited.
- Waivers can be permanent or time-limited (SEC-030).
- On a policy received from another site, follower sites may add their own package and label
  waivers; these stay local and survive updates from the controller (SEC-040).

UI location: policy wizard, waivers step.

### SEC-029 · Waiver requests and approval
Priority: **P2**

Per policy, the waiver request mode decides what happens when a developer asks for an exception:

| Mode | Behavior |
|---|---|
| Not allowed | No requests can be made against the policy. |
| Manually approved | The request is created and the decision owners (one or more groups, required) are notified by email; they approve or reject. Policy owner and decision owners need at least view access to the policy and package details. |
| Automatically approved | The request is granted immediately; meant for soft blocks such as license or operational policies. If the policy has a maximum waiver duration, the waiver expires after it. |

Flow:

1. A developer whose download was blocked lists the blocked packages (for example with the
   dependency pre-check, SEC-038) or opens the blocked event in the audit log, selects packages and
   writes a justification.
2. One request can involve several policies, each with its own decision owners (for example a
   legal group and a technical group); each owner decides the policies they own.
3. Approve adds a waiver to the blocking policies; when owners approve the same request with
   different durations, the shortest duration applies.

Waiver management screen:

- Pending requests, with a toggle between "assigned to me" and "all"; a request shows the
  requester, the blocking policies and their owners.
- Closed requests: approved, rejected, automatically approved and expired, with who decided,
  when, the justification and the duration, and a waiver-expiry column.
- CSV export with expiry dates, decision owners and decision history.

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| Create waiver request | package type, name, version, repository, justification | Request; refused unless the policy allows manual or automatic waivers | unspecified (in the flow, the developer whose download was blocked) |
| List waiver requests | filters | Requests with status, requesters, decision owners, affected policies | unspecified |
| Decide waiver request | request id, approve or reject, justification, optional duration | Updated request | member of a decision-owner group of the policies concerned, plus the policy-manager role (SEC-035) |

Interactions: waiver-request events, EVT-014 in
[events-and-extensibility.md](events-and-extensibility.md).

### SEC-030 · Time-limited waivers
Priority: **P2**

| Setting | Scope | Meaning | Default |
|---|---|---|---|
| Maximum waiver duration | Policy | Manual approval: upper bound the decision owners may choose; a time limit becomes mandatory. Automatic approval: the duration given to every waiver. | none (waivers permanent) |

- Range 1 to 365 days. Setting a duration switches on enforcement on cached packages for the
  policy, because an expired waiver must also apply to cached copies (SEC-023).
- When approving, the decision owner enters a number of days and sees the computed expiry date;
  if no limit is required and none is set, the waiver is permanent.
- Expiry statuses: *permanent*; *expiring soon* (within 7 days, with a countdown); *expired*;
  *not applicable* (rejected or blocked requests).
- On expiry the waiver is removed from the policy, enforcement resumes, a cached copy is blocked at
  its next download, the request moves to closed requests as expired, and a new request can be
  filed.

### SEC-031 · Waivers for pending and fully blocked packages
Priority: **P2**

- *Pending data-source update*: a waiver can be requested against the policy that blocked the
  pending package and follows that policy's request mode. Because no version data exists yet, it
  covers the whole package. It is valid only while the package is pending (it is recorded under a
  dedicated pending-waiver label that regular evaluation ignores); once the data arrives the
  policy evaluates normally and may block again, so a lasting exception needs a regular waiver.
  Default duration 2 days; the approver must set an expiry between 1 and 365 days; permanent
  waivers are refused whatever the policy says.
- *Pending on-demand data*: cannot be waived; retry after the scan completes (SEC-039).
- *All versions blocked by compliant version fallback*: the audit event shows version *All*; from
  that event a waiver is requested for one chosen version, picked from suggestions that include
  the versions the fallback evaluated. The request follows the blocking policy's mode.

## Upstream curation: audit and administration

### SEC-032 · Curation audit log
Priority: **P1**

Every curation evaluation and every user action in the curation area is recorded.

- Two inspection views: *downloads* (evaluations of package requests) and *fallback* (compliant
  version inspections, SEC-033).
- Downloads view tabs: *blocked*, *approved* (including bypassed packages), *dry run*, *not
  inspected* (requests that no policy evaluated; this tab is worth monitoring, since such packages
  were never checked).
- Blocked event details: the policies that matched; the reason (policy violation or pending data);
  package information; remediation advice such as fixed versions; a link to related fallback
  activity.
- Search and filters: package name, package version, date or hour range, event origin (SEC-023)
  and further filter fields. Only recorded events are searchable; a package missing from the log
  may still exist in a repository.
- Export: the currently visible, filtered rows of the downloads tabs as CSV.
- Retention: 30 days.
- Event content (from the blocked-package event, EVT-014): package type, name, version and
  upstream URL; reason; curated repository and its project; requesting user's name and email;
  originating repository, server and project when the request came through another instance;
  upstream registry name and URL; matched policies with id, name, dry-run flag, result, condition
  name and category.

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| Export audit events | time range, filters, dry-run flag (default: real events) | Approved and blocked events, each with the real (not dry-run) policies that blocked it or let it through by relaxation | unspecified |

UI location: curation, audit events.

### SEC-033 · Compliant version fallback audit
Priority: **P2**

A row stands for one fallback pass (SEC-025): a single metadata fetch or resolve by a client
against a curated repository, together with every package that pass inspected.

| Result | Meaning |
|---|---|
| All versions compliant | Nothing had to be replaced. |
| Compliant version found | A requested version was blocked and a compliant one was selected. |
| No compliant versions | No compliant alternative existed. |

A version detail shows the policies that hit it. The view is empty (with a pointer to the setting)
while fallback is off. From a downloads event, a link opens the related fallback event.

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| List fallback audit events | filters, pagination | Events with repository and inspected packages and versions | curation policy viewer |
| Get fallback audit version detail | event id, version id | One inspected version with the policies that hit it | curation policy viewer |

Edge cases: CSV export of this view depends on the version; the blocked-package webhook and the
download audit export do not cover fallback inspections.

### SEC-034 · Curation notifications
Priority: **P2**

| Setting | Meaning | Default |
|---|---|---|
| Email requester on blocked events | Emails the authenticated user whose download was blocked. Not possible for anonymous requests. | unspecified |
| Email requester on dry-run events | Emails dry-run matches to the authenticated requester, aggregated hourly. | unspecified |
| Notify list about blocked and dry-run events | Daily aggregated email, sent at 08:00 local time, to additional addresses (for example a security officer). | unspecified |
| Notify list about disconnected repositories | Emails when a repository is disconnected from curation (SEC-008). | unspecified |
| Policy notification emails | Per policy (SEC-009). | none |

One blocked event can stem from several policies. Blocked-package, waiver-request and
policy-change events can also be sent to webhooks (EVT-014 in
[events-and-extensibility.md](events-and-extensibility.md)), for example to open tickets.

UI location: administration, curation, general.

### SEC-035 · Curation roles
Priority: **P1**

| Capability | Curation administrator | Policy manager | Policy viewer |
|---|---|---|---|
| General settings (service switch, notifications, fallback) | yes | no | no |
| Connect and disconnect repositories | yes | no | no |
| Custom conditions | create, edit, delete | create only | no |
| Coverage and usage overview | view | view | view |
| Policies | create, edit, delete | create, edit, delete | view |
| Audit (blocked, approved, dry run, user actions) | view | view | view |

Roles are assigned to users in the user's role settings; the administrator role in this table is
the system administrator. Inside a project context, policies, conditions, general settings and
repository connections are shown for that project but can only be changed globally.

Interactions: scan-related roles, IAM-008 in [identity-and-access.md](identity-and-access.md).

### SEC-036 · Effective policies of a repository
Priority: **P1**

For one curated remote repository, a dialog lists every active curation policy that currently
applies to it, so administrators can see what governs the repository.

UI location: curation settings, remote repositories, row action.

### SEC-037 · Curation management operations
Priority: **P1**

<details>
<summary>Operation catalog</summary>

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| List curation policies | pagination, filters | Policies with their condition inlined | curation policy viewer |
| Create curation policy | name, scope, condition id, action, waivers, waiver-request mode, decision owners, notification settings, pending behavior | Policy; read-only fields (id, creator, updater, timestamps) must not be sent | policy manager |
| Get curation policy | policy id | Full policy, condition inlined | curation policy viewer |
| Update curation policy | policy id; editable fields only (not the object returned by get, since read-only fields are refused) | Policy | policy manager |
| Delete curation policy | policy id | none | policy manager |
| Condition operations | see SEC-020 | | |
| Waiver request operations | see SEC-029 | | |
| Audit exports | see SEC-032, SEC-033 | | |
| List remote repositories with curation state | none | see SEC-008 | unspecified |
| Curation health | none | Status per dependency (SEC-007) | unspecified |

</details>

### SEC-038 · Dependency pre-check
Priority: **P2**

A developer-side tool (the companion command-line tool, see
[builds-and-ci.md](builds-and-ci.md)) resolves a project's dependency tree against the curated
repositories and reports, without installing, which packages curation would block, why, and what
to use instead; it then offers to file waiver requests (SEC-029). The system records these
evaluations as audit events with origin *simulated* (SEC-023). For container images the check can
include packages already cached. Some package types need pass-through mode (SEC-024) for the
check to work.

Unspecified: the server-side interface the tool uses.

### SEC-039 · On-demand evaluation of uncovered packages
Priority: **Later**

Extends curation to remote repositories whose upstream the data source does not cover (internal,
private or niche registries). Because no data exists, the first request for a package triggers a
scan by the scanning engine; the result is stored in a private dataset of the organisation and
serves later requests immediately.

Prerequisites: the repository is indexed for scanning (SEC-042, REPO-021 in
[repositories.md](repositories.md)); curation for cached packages is enabled (SEC-023); the global
on-demand switch is on (it makes uncovered repositories visible; switching it off disconnects
all of them); and the package type (current and future repositories) or the single repository is
connected.

Behavior:

- During the scan the package is *pending on-demand data* (SEC-022); policies block or skip
  according to their setting; no waiver is possible while pending.
- The scan result is kept for one hour. A large artifact (for example a big container image) can
  time out on the first request after the scan; a retry within that hour succeeds from the kept
  result (a few minutes usually suffice); after one hour the result is
  fetched again and the request can time out again. No status shows when the result is ready.
  An automatic retry is built in; a request that still times out is blocked or let through as the
  policies' pending setting decides.
- Content cached before the feature was enabled is scanned on its first on-demand request; new
  downloads are indexed automatically.
- Supported condition kinds: vulnerabilities (CVE-identified only, so counts can be lower than the
  scanner's own), licenses, maliciousness, immaturity (SEC-014 notes the date fallback). Not
  supported: OpenSSF Scorecard, end-of-life.
- Labels and waivers work once a scan result exists.
- Docker repositories already curated are marked on-demand automatically when the feature is
  enabled; existing Docker and organization-wide policies then apply at once.
- UI: on-demand packages carry a tag in search, repository lists and package pages; result
  listings show public and on-demand data in separate tabs, with a link when a package exists in
  both.
- A query interface over the on-demand data requires an administrator token.

### SEC-040 · Curation policy sharing across sites
Priority: **Later**

For organisations running several instances: one *controller* instance is the source of truth and
*follower* instances receive its curation configuration.

- Distributed: package-type connection settings, custom conditions, labels, and the policies an
  administrator opts in per policy. Only policies with an organization-wide or package-type scope
  can be shared.
- Followers may change, locally: excluded repositories, notification emails, decision owners,
  waivers. They cannot change name, enabled state, condition, scope, package types or action, and
  cannot delete a shared policy; it disappears when the controller deletes it.
- The policy list shows whether a policy is local or received.
- Propagation is asynchronous and near real time; a disconnected follower lags until it
  reconnects; a full synchronization reconciles missed updates.

Interactions: multi-site topology in
[replication-and-federation.md](replication-and-federation.md).

### SEC-041 · Redirect endpoint for intercepted public registry traffic
Priority: **P2**

Some organisations force developer traffic to public registries through curation without
reconfiguring clients: a TLS-inspecting network security edge (secure web gateway) answers
requests to public registry hosts with an HTTP 307 redirect to a redirect endpoint of the
repository manager, carrying the original URL as a percent-encoded query parameter. The endpoint
maps the registry to a configured remote repository and answers with a 302 or 307 redirect
whose `Location` points into that remote repository, where curation and audit apply.

- Per registry type, the configuration names the target remote repository. It must be an
  existing remote repository that proxies the public registry directly; virtual repositories and
  remote repositories pointing at another instance are not allowed.
- Supported types: npm, PyPI, Docker, Hugging Face, RubyGems, Cargo, Maven, Go.
- Edge requirements: TLS inspection for the registry hosts; redirect only GET and HEAD (POST, as
  used by `npm audit`, must not be redirected; Hugging Face GET only); exclude the repository
  manager's own host from redirection; workstations must trust the edge's CA.
- Each redirect is logged for verification and audit.
- Redirection does not carry the developer's registry credentials; private or scoped packages that
  need credentials require the client to point at the remote repository directly with a token.
- A pilot rollout per user group and per ecosystem is recommended.

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| Get redirect configuration | none | Configuration per registry type | administrator |
| Get registry configuration | registry type | Configuration | administrator |
| Update registry configuration | registry type, target remote repository, curation link flag | Configuration | administrator |
| Delete registry configuration | registry type | none | administrator |

Unspecified: the semantics of the curation link flag.

## Scanning stored content

### SEC-042 · Indexing scope
Priority: **MVP**

Indexing extracts and stores the component inventory and dependency graph of a resource, which is
what scanning, continuous re-evaluation (SEC-048) and affected-resource search (SEC-074) operate
on. Nothing is indexed automatically; an administrator selects:

| Resource kind | Selection | Notes |
|---|---|---|
| Repositories | Individual local, remote and federated repositories (the per-repository switch is REPO-021 in [repositories.md](repositories.md)). | Formats that can and cannot be indexed are listed under REPO-021. |
| Builds | Build names, or include/exclude patterns (one or the other, not both). | Build records, see [builds-and-ci.md](builds-and-ci.md). |
| Release sets | Release set names (both generations). | See [distribution-and-lifecycle.md](distribution-and-lifecycle.md). |

The indexed-resources screen lists what is indexed, which is also the way to verify a change. A
project administrator holding an "index resources" privilege can manage indexing within the
project.

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| Get repository indexing configuration | optional project | Indexed and non-indexed repositories | administrator, or project index privilege |
| Update repository indexing configuration | list of repositories to index | none | administrator |
| Get repository indexing status | repository | Indexing progress | administrator or index privilege |
| Get, replace, add build indexing configuration | build names (optionally with build repository and project) or patterns | Indexed and non-indexed builds | administrator or index privilege |
| Get, replace, add release set indexing configuration | release set names | Indexed and non-indexed release sets | administrator |

UI location: administration, scanner settings, indexed resources (tabs for repositories, builds,
release sets).

### SEC-043 · Scanning on upload and on cache
Priority: **MVP**

In an indexed repository, every artifact that is uploaded, modified, or (in a remote repository)
cached from the upstream is indexed and scanned. Existing content is not scanned when indexing is
switched on; it needs an explicit trigger (SEC-044) or a scope application (SEC-056).

Per indexed repository:

| Setting | Meaning | Default |
|---|---|---|
| Scan scope | *Scan all artifacts*: every future artifact; *scan by pattern*: only future artifacts under include patterns minus exclude patterns. | unspecified |
| Path patterns | Comma-separated wildcard paths without a leading slash; Ant-style `*`, `**`, `?` (for example `org/apache/**`). At most 10 patterns per repository. | none |
| Scan flag per pattern | Whether new artifacts under the pattern are scanned at all (existing ones are never rescanned by this). | unspecified |
| Retention | See SEC-045. | 90 days |
| Analysis categories | Which additional analyses run on new artifacts (SEC-081 to SEC-083). | unspecified |

Edge cases: by-pattern scope requires a retention value for "all other artifacts" as well.

### SEC-044 · On-demand indexing and reindexing
Priority: **MVP**

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| Index a repository now | repository | Indexing of existing content starts | administrator |
| Scan a path now | repository path | List of artifacts sent for (re)indexing (repository and path each) | unspecified |
| Force reindex | artifacts and/or builds | Reindex of resources with missing component data; the total number of entities per call is capped (value unspecified) | manage scan metadata (IAM-022) |
| Scan an artifact | component identifier (for example a container image reference) | Scan triggered | manage scan metadata |
| Scan a build | build name and number | Scan triggered; build scan results with violations only when the build fails, full findings on request | manage scan metadata |

UI location: indexed resources, repository row menu, *index now*.

### SEC-045 · Scan data retention
Priority: **Later**

Scan data of indexed resources accumulates and costs storage and performance, so it expires.

<details>
<summary>Retention settings</summary>

| Setting | Meaning | Default |
|---|---|---|
| Retention for artifacts of an indexed repository | Days scan data of an artifact is kept after its scan. | 90 days |
| Retention per path pattern | Retention for artifacts under a pattern (SEC-043). | unspecified |
| Retention for all other artifacts | Required when by-pattern scope is used. | unspecified |
| Upper limit for repository retention | Largest value an administrator may set. | 1000 days |
| Maximum path patterns per repository | | 10 |
| Retention for artifacts of non-indexed repositories | Scan data of artifacts in repositories that are not indexed. | 3 days |
| Retention for indexed builds | | 15 days |
| Retention for builds not marked for indexing | | 3 days |
| Disable data cleanup | Turns the cleanup job off. | off |

</details>

- Downloading an artifact restarts its retention period.
- Expired resources drop out of continuous re-evaluation (SEC-048) and searches until scanned
  again.

UI location: indexed resources, select repositories or builds, *configure*.

### SEC-046 · File-name exclusion from indexing
Priority: **Later**

An installation-level list of file names (separated by `|`) that indexing skips. Changing it
requires a restart of the scanner and a forced reindex (SEC-044) of already scanned artifacts.

### SEC-047 · Scan status
Priority: **MVP**

Every scannable resource has an overall scan status and a status per analysis part, each with a
timestamp (RFC 3339).

| Status | Meaning |
|---|---|
| Not supported | The resource type cannot be scanned. |
| Not scanned | No scan has happened. |
| Pending | Queued. |
| Scanning | In progress. |
| Done | Finished. |
| Partial | Some parts finished, others failed or are unsupported. |
| Failed | The scan failed. |

Parts: software composition analysis, applicability analysis (SEC-081), secret and
misconfiguration categories (SEC-082, SEC-083), and policy violation evaluation.

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| Artifact scan status | repository, path | Overall and per-part status; 404 if the artifact does not exist or is not indexed | read |
| Build scan status | build name, number | Status | read |
| Release set scan status | release set identifier | Status | read |

Interactions: terminal statuses produce scan-status events, EVT-015 in
[events-and-extensibility.md](events-and-extensibility.md).

### SEC-048 · Continuous re-evaluation
Priority: **MVP**

Stored content is not scanned once and forgotten. Every change in the vulnerability database (a
new CVE, a changed advisory, a newly flagged malicious package, a change in CISA KEV membership)
becomes an event; the scanner reuses the dependency graph recorded at the original scan, applies
the new data and re-checks only the indexed resources the change affects. Updated findings appear
in the resource's scan results; if a scan scope and policy cover the resource, the policy is
re-evaluated, violations are created or removed, and the policy's actions run (block download,
fail build, block distribution, notify).

| Deployment | When it runs |
|---|---|
| Online synchronization (SEC-002) | With each update, roughly hourly. |
| Offline synchronization (SEC-003) | After an update package is applied. |

Without scan scopes and policies, results are updated but no violations or actions follow.

Limits: only indexed resources within their retention period (SEC-045) are covered.

### SEC-049 · Scan results view
Priority: **MVP**

A scan results list per resource kind (repositories, builds, release sets, packages, Git
repositories); selecting a resource and version opens:

| Tab | Content |
|---|---|
| Summary | Last scan date, downloads, vulnerabilities by severity (critical, high, medium, low), violations by severity and type (security, license, operational risk), most common package types and licenses, runtime-validated risks. |
| Policy violations | Violations of the resource (SEC-057), with fix versions and the option to create an ignore rule (SEC-058). |
| SBOM | Components with versions, licenses and operational risk data (SEC-065). |
| Security issues | Vulnerabilities and malicious packages with details; CVEs in the CISA KEV catalog are marked and show the date added and due date. |
| Remediation | Fix options (SEC-077). |

Filters: severity, component name, only violating findings. Export: SEC-072.

The same scan data appears in the item details pane, ART-041 in [artifacts.md](artifacts.md), and
in the package view, ART-049 in [artifacts.md](artifacts.md).

## Scan policies, scan scopes and violations

### SEC-050 · Scan policy model
Priority: **Later**

A scan policy is a named set of rules of one type; it takes effect only when attached to a scan
scope (SEC-055).

| Field | Meaning | Default |
|---|---|---|
| Name | Unique system-wide, or within a project for a project policy. | none (required) |
| Description | Free text. | empty |
| Type | *Security*, *license* or *operational risk*; decides which criteria a rule may use. | none (required) |
| Rules | One or more; each has a name, a priority (lower number = higher priority), criteria (SEC-051 to SEC-053) and actions (SEC-054). | none (required) |

- Policies are global or belong to a project; a scan scope may use both.
- Update replaces the whole policy; the name in the request path wins over a name in the body.
- A policy assigned to any scan scope cannot be deleted; it must be unassigned first. Deletion
  presumably cleans up the policy's violations; the exact behavior is unspecified.
- One assignment call can attach a policy to several scan scopes; each scope is processed and
  reported separately (the handling of a missing scope is unspecified).

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| Create, get, list, update, delete policy | policy; optional project | Policy or list (with author, created and modified times, assigned scopes) | policy manager to change, policy viewer to read (IAM-008) |
| Assign policy to scan scopes | policy name, scope names | Result per scope | policy manager |

UI location: scanner, scan scopes and policies.

### SEC-051 · Security rule criteria
Priority: **Later**

A security rule matches by one rule kind:

| Rule kind | Criteria |
|---|---|
| Vulnerabilities | One of: minimum severity (critical, high, medium, low, all); CVSS score range (0 to 10); a list of specific vulnerability identifiers. Options: *except when no fix version exists*; *skip vulnerabilities found not applicable* (SEC-081); *skip if the image is not running* (SEC-084); *skip base image* (container images only: findings inherited from the base image do not raise violations under this rule); *only CVEs in the CISA KEV catalog*. |
| Malicious packages | Packages flagged malicious (SEC-004). |
| Package version | Package type, package name, versions: any `(,)`, exact `[v]`, or open, closed and half-open intervals such as `(v1,v2]`. |
| Secrets and misconfigurations | One or more categories (secrets, services, applications, infrastructure as code, malicious code) and a minimum severity (SEC-082, SEC-083). |
| First-party code findings | Static analysis findings (SEC-083). |

Edge cases:

- *Skip base image* needs base-image detection and a prior scan of the base image on the same
  installation; an unscanned base image's components count as the application's own. It applies
  only to violations created afterwards, and only to the rule that has it; a sibling rule without
  the option (or another policy on the same scope) still produces a violation for that component,
  and rules are worked through in priority order. It is not available on package-version,
  suspicious-checksum, static-analysis, secrets and misconfiguration, malicious-package or
  operational-risk rules.
- A change in CISA KEV membership triggers re-evaluation (SEC-048).

### SEC-052 · License rule criteria
Priority: **Later**

| Criterion | Meaning |
|---|---|
| Banned licenses | Components with any of these licenses match. |
| Allowed licenses | Components with a license outside the list match. |
| Allow unknown | Whether components with an unknown license are tolerated. |
| Multi-license permissive | For components with several licenses, judge permissively (one acceptable license suffices). |
| Custom licenses | Custom licenses (SEC-006) appear in the selection; in replace mode they substitute the detected license. |
| Skip base image | As in SEC-051, for container images. |
| Custom severity | Severity given to the resulting violation (critical, high, medium, low); also available on operational risk rules. |

### SEC-053 · Operational risk rules and scoring
Priority: **Later**

Operational risk covers non-security threats to maintainability: end-of-life components,
deprecated components, unmaintained projects, major-version jumps with breaking changes,
abandoned projects. Supported for npm and Maven.

Rule criteria: a minimum risk (high, medium, low), or a custom combination joined by AND or OR of:
end-of-life; fewer than N commits per year (10, 25, 50, 100); more than N newer versions (1 to 5);
fewer than N committers per year (1 to 5); release older than N months (6, 12, 18, 24, 30, 36);
fewer than N releases per year (1 to 5).

<details>
<summary>Risk scoring</summary>

| Factor | Rule |
|---|---|
| End-of-life | true = high, false = none. |
| Version age | months since release divided by 10: ≥ 4 high; > 2 and < 4 medium; > 1 and ≤ 2 low; ≤ 1 none. |
| Number of newer versions | count divided by 2: ≥ 6 high; ≥ 4 and < 6 medium; ≥ 2 and < 4 low; < 2 none. |
| Project health | at least 2 general-availability releases per year (patch releases count) = healthy; at most 1 = unhealthy; no data = healthy. |

Combined severity: end-of-life high gives high; otherwise an unhealthy project gives high;
otherwise the higher of version age and newer-versions count decides (high, medium, low or none).

</details>

### SEC-054 · Scan policy actions
Priority: **Later**

Every matching rule creates a violation; the rule's actions decide what else happens.

| Action | Meaning |
|---|---|
| Invoke webhooks | Named violation webhooks (SEC-059). |
| Create ticket | One issue-tracker ticket per violation (SEC-060). |
| Notify scope recipients | Email the scope's recipients. |
| Notify deployer | Email the user who deployed the artifact. |
| Notify addresses | Email a list of addresses. |
| Fail build | A build scan requested by CI reports failure, with the details of every violation from scopes carrying this action; optional grace period in days. If no scope at all carries a fail-build action, a build scan request always answers "fail", even without findings. |
| Fail pull request | Source-code scans fail the pull request; no grace period. |
| Block download | Refuse downloads of matching artifacts (SEC-061); options: active, block unscanned artifacts (SEC-062), grace period in days. |
| Block release set promotion | Refuse promotion of a matching release set; optional grace period. |
| Block release set distribution | Refuse distribution of a matching release set. |

A grace period gives teams time to fix a violation before the action takes effect.

### SEC-055 · Scan scopes
Priority: **Later**

A scan scope selects resources and attaches policies to them.

<details>
<summary>Scan scope definition</summary>

| Field | Meaning | Default |
|---|---|---|
| Name | 1 to 255 characters, unique. | none (required) |
| Description | Free text. | empty |
| Active | Whether the scope is evaluated. | on if resources are given, otherwise off |
| Resources | Repositories (local or remote), builds, release sets (both generations), projects, Git repositories, or the wildcards all repositories, all builds, all release sets, all projects. | none |
| Resource filters | Per resource: regular expression, path regular expression, package type, MIME type, property key and value, Ant include/exclude patterns, path Ant include/exclude patterns. Which filters apply depends on the resource kind. | none |
| Global filters | Filters applied across all resources. | none |
| Assigned policies | Security, license and operational risk policies, global or project. | none |
| Recipients | Email addresses for scope notifications. | none |
| Ticket creation | Enabled flag, ticket profile, duplicate tickets per version (build, package, release set), mapping of impact-path patterns to ticket profiles, tickets for ignored violations. | unspecified |

</details>

Operations: create (409 if the name exists), get, list, update (full replacement; path name
wins), delete; optional project scoping. Privilege: scan-scope manager (IAM-008); a project
administrator with a "manage security assets" privilege inside the project.

UI location: scanner, scan scopes and policies.

### SEC-056 · Apply a scan scope to existing content
Priority: **Later**

A new scope only evaluates resources at their next scan-triggering event. Applying it to existing
content evaluates already indexed resources immediately: a dialog selects which of the scope's
resources to include and a date range of their last scan.

Limits: not available for scopes defined on all repositories or all builds. Ignore rules created
later (SEC-058) also need this to affect past results.

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| Apply scope on existing content | scope names, date range | Evaluation started | scan-scope manager |

### SEC-057 · Violations
Priority: **Later**

A violation records that a component in a resource matched a rule: severity, type (security,
license, operational risk), summary, the component and its path, impacted artifacts, the scope,
policy and rule, creation time, and applicability where available.

- A central violations view lists all violations of all scopes with filters, and offers ignore
  rules (SEC-058).
- Ignored violations remain stored and listable separately.

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| Search violations | filters (type, severity, scope, resource and more), pagination; a cursor-paginated variant without page-depth limit exists for large sets | Violations | unspecified |
| List ignored violations | objective filters (vulnerabilities, licenses, CVEs, operational risk, secret and misconfiguration findings) and scope filters (policies, scopes, components, artifacts, builds and more) | Ignored violations with the rule that suppressed each | scan-scope manager |

### SEC-058 · Ignore rules
Priority: **Later**

An ignore rule suppresses matching violations: they stay in the database and visible in audit, but
stop triggering notifications, blocks and build failures.

<details>
<summary>Ignore rule definition</summary>

| Field | Meaning | Default |
|---|---|---|
| Objective filter (at least one) | Vulnerability identifiers, CVE identifiers, license names, operational risk identifiers, or secret and misconfiguration findings (by scanner id, category, file path, file path pattern or finding fingerprint). The value "any" matches all of a kind. | none (required) |
| Scope filters | Policies, scan scopes, projects, components (name, optional version), artifacts (name, optional version and path), builds (name, optional version and project), release sets (name, optional version), container layer digests, Git repositories. | none (global) |
| Expiry | Future timestamp (RFC 3339); a past value is rejected; after it the rule expires and stops suppressing. | none (permanent) |
| Notes | Why the rule exists. | empty |

</details>

Behavior:

- A rule applies to future evaluations only; to change past results the scope must be re-applied
  to existing content (SEC-056).
- Deleting a rule is a soft delete; a background job reactivates the violations it suppressed.
- Created from a violation, a scan result row or an affected-resource search row (SEC-074).
- A fingerprint filter must reference an existing finding, otherwise 404.

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| Create ignore rule | definition; optional project (added to the scope filters) | 201 with the rule | scan-scope manager |
| List, get ignore rules | filters, pagination; id | Rules | scan-scope manager |
| Delete ignore rule | id | Soft delete, violations reactivated | scan-scope manager |

### SEC-059 · Violation notifications and webhooks
Priority: **Later**

Violations reach people by email (scope recipients, deployer, address lists, SEC-054), by ticket
(SEC-060) and by violation webhooks. A violation webhook is defined once and referenced by name
from rule actions:

| Field | Meaning | Default |
|---|---|---|
| Name | Identifier referenced by rules. | none (required) |
| URL | Target called on a violation. | none (required) |
| Description | Free text. | empty |
| Use proxy | Send through the configured outbound proxy. | unspecified |
| User name, password | Basic authentication towards the target. | none |
| Custom headers | Extra request headers. | none |

These violation webhooks are separate from the event subscriptions of the platform, which deliver
scan-status events (EVT-015 in [events-and-extensibility.md](events-and-extensibility.md)).

### SEC-060 · Issue tracker integration
Priority: **Later**

Violations can open tickets in an issue tracker (for example Jira): manual or
automatic ticket creation per violation, ticket profiles, assignment and listing of tickets per
violation. Configuration per scope: SEC-055. Operations: create, get status, get details, update
and delete an integration connection.

### SEC-097 · Export of findings to external security dashboards
Priority: **Later**

Scanner findings can be surfaced in security tools that teams already watch:

- **Cloud security hub.** An adapter that the operator deploys into their own cloud account
  (serverless functions, a queue and a key-value table) receives violation webhooks (SEC-059),
  converts each violation into the hub's finding format and imports it into
  [AWS Security Hub](https://aws.amazon.com/security-hub/); findings already imported are updated
  instead of duplicated when the same violation is detected again. Set-up: deploy the adapter,
  accept findings from it in the hub, then define a violation webhook to the adapter's URL with an
  authorization header carrying the adapter's token and a header naming the source host, and
  reference that webhook from a rule action (SEC-054). Covers security and license violations.
  The data does not pass through third-party systems because the adapter runs in the operator's
  account.
- **Code-hosting security tab.** Security and applicability findings appear in the code-scanning
  view of GitHub Advanced Security. Either the companion CLI uploads them during a pipeline scan
  (image scan or build scan) when given a token of the hosting service, or, once enabled through
  the hosting service's app integration, every build scan uploads its results automatically. The
  pull-request bot (SEC-085) uploads its results the same way. Without the hosting service's
  security product the findings stay visible in the system only.

For trove the analogue is GitLab's security dashboard, which ingests findings as report
artifacts of CI jobs; both mechanisms above reduce to "violation event → adapter → external
format", which webhooks (SEC-059, EVT-015) already provide.

**Interactions.** SEC-054, SEC-059, SEC-060.

## Download blocking

### SEC-061 · Block downloads on violation
Priority: **MVP**

When a rule with the block-download action matches an artifact, downloads of that artifact are
refused. The client sees a forbidden response without the policy name; the download status check
(SEC-063) tells which policy and finding caused it.

- Security, license and operational risk rules can all block.
- Continuous re-evaluation (SEC-048) can block an artifact that was downloadable yesterday.
- A grace period delays the block.
- An ignore rule (SEC-058) on the violation lifts the block; the scope for "do not block this
  artifact" is an ignore rule on the artifact.
- Global overrides: SEC-064.
- The block-download action is marked deprecated; date and successor are unspecified. The
  underlying need (refuse downloads of stored content that violates policy) stands regardless.

Interactions: curation blocks at the remote boundary before content enters (SEC-021); this action
blocks content already stored, including local repositories.

### SEC-062 · Block downloads of not-yet-scanned artifacts
Priority: **MVP**

With the *block unscanned artifacts* option on a block-download action, a download of an artifact
in the scope that has not been scanned yet does not proceed immediately: the request waits for the
scan to finish, at most for a configurable timeout.

| Setting | Meaning | Default |
|---|---|---|
| Block unscanned artifacts | Per block-download action. | unspecified |
| Unscanned download timeout | Longest time a download request waits for the scan. | 60 seconds |

- The client's socket timeout must exceed the timeout, or the client disconnects first.
- While scanning is incomplete, failed or partial the download status is *unknown*, and such a
  scope can still block.

Unspecified: whether the request is refused or released when the timeout expires.

### SEC-063 · Download status check
Priority: **MVP**

Answers, without downloading, whether an artifact's download is currently blocked and why. The
artifact must exist; the caller needs the same read permission as for a download.

Inputs: a repository path, a full download URL, or a container image reference by tag or digest
(with optional registry host); optional project.

| Status | Meaning |
|---|---|
| Blocked | At least one matching, non-ignored rule blocks downloads. |
| Allowed | Violation evaluation is complete and nothing blocks. |
| Unknown | Evaluation is unfinished, failed or partial, or the indexed checksum does not match the artifact. |

Output: path, SHA-256 when known, violation-scan status, download status, reason, a link to the
scan results, and the violations ordered by blocking first, then severity, with blocking flag,
severity, finding (for example the CVE), state, violation id, scope, policy and rule. The companion
command-line tool presents this as a table or JSON.

### SEC-064 · Global blocking overrides
Priority: **MVP**

| Setting | Meaning | Default |
|---|---|---|
| Allow download and distribution when the scanner is unavailable | Downloads and release set distributions proceed while the scanning service is down. | unspecified |
| Allow downloads of blocked artifacts | Disables all download blocks system-wide. | unspecified |

UI location: administration, scanner settings, advanced.

## Software bill of materials

### SEC-065 · SBOM generation
Priority: **Later**

For every scanned resource the scanner builds an SBOM: it scans artifacts recursively for source
and binary components, extracts their metadata (licenses, package identifiers, dependencies),
matches each component against the data source using hashes, identifiers, architecture and
distribution information, and stores the result for viewing and export. Custom license
assignments (SEC-006) are reflected.

### SEC-066 · SBOM export
Priority: **Later**

| Format | Versions and encodings |
|---|---|
| [SPDX](https://spdx.dev/) | 2.3 (default): tag-value, JSON, XLSX. 3.0: JSON, XLSX. The API offers JSON and tag-value only; XLSX is a UI export. |
| [CycloneDX](https://cyclonedx.org/) | JSON, XML. Includes vulnerability exploitability (VEX) data: details, exploitability status, analysis. Can include a cryptography bill of materials built from secret-detection findings (certificates, keys and other secrets, SEC-082); requires secret detection to be active. |

- Dependency relationships between components are included in both formats (SEC-068).
- A supplier can also be assigned to resources for SBOM reports, and first-party components can
  be filtered out of a CycloneDX export; the mechanics are unspecified.
- One export request can combine an SBOM with a rendered report (SEC-072); the result is a ZIP.
  At least one content selection is required (otherwise 400), and each selected content needs its
  format (otherwise 400).
- A separate VEX document can be produced next to the SBOM.

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| Export component details | package type, component name, path; content selections (vulnerabilities, violations with or without ignored ones, licenses with or without unknown ones and with license conclusion data, operational risk, secret and misconfiguration categories, static analysis, SPDX, CycloneDX, cryptographic assets, VEX); output formats | ZIP | read |

UI location: scan results list, resource version, *export scan data*, SBOM.

### SEC-067 · SBOM import and aggregation
Priority: **Later**

- Import: uploading a CycloneDX file (`.cdx.json`, `.cdx.xml`) or an SPDX 2.x JSON file
  (`.spdx.json`) into an indexed generic repository makes the scanner index and scan it; it then
  appears in the scan results list, enriched with vulnerability (VEX) and license obligation data.
- Aggregation: an SBOM file added to a scanned artifact (for example inside a container image or
  archive) contributes its components to that artifact's scan results.
- The companion command-line tool can enrich an external SBOM with vulnerability and license data.

### SEC-068 · Dependency graph and transitive SBOM
Priority: **Later**

The scanner reconstructs the full dependency tree (direct, transitive and first-party
relationships) from declarations inside bundled artifacts, that is artifacts that carry their
dependencies: container images, builds, release sets, and SBOM files whose dependency graph is
already resolved. Artifacts that only reference external dependencies are not supported.

| Ecosystem | Source of dependency data |
|---|---|
| Maven | `pom.xml` |
| PyPI | `pyproject.toml`, `PKG-INFO` |
| NuGet | `*.deps.json` |
| Go | metadata embedded in the compiled binary |
| CycloneDX SBOM | `dependsOn` |
| SPDX 2 SBOM | dependency relationships |

Uses: an interactive SBOM tree view and the dependency relationships in SBOM exports (required by
frameworks such as the NTIA minimum elements and BSI TR-03183). Operations: dependency graph of an
artifact and of a build (recursive component tree with package types).

### SEC-091 · Base image detection
Priority: **Later**

For a container image, the scanner separates the components inherited from the base image from
those added in the image's own layers. This attribution feeds the *skip base image* rule option
(SEC-051, SEC-052) and the base-image upgrade group of remediation suggestions (SEC-077). It works
only when the base image was itself scanned on the same installation; otherwise its components
count as the image's own. Detection method unspecified.

## License compliance

### SEC-069 · License detection and management
Priority: **Later**

- Detection identifies licenses of third-party and internal components from scanned content or
  from external license data; custom licenses (SEC-006) are recognised by their text.
- Manual override of a component's licenses and copyrights, from the component details of the SBOM
  view or through the API: add one license to one component or to many components in one request,
  remove one license from a component, or replace the full set of copyright notices of a
  component (the latter needs SBOM support enabled). License names are validated against the
  known-license database before the change is accepted. Every change starts a targeted impact
  analysis: all scanned resources containing the component are found and license rules are
  re-evaluated for them without a full rescan, so violations, scopes and reports reflect the
  correction.
- A license list shows every known license and which scanned artifacts use it.
- License export: component identifiers, versions, license identifiers and links to full license
  texts, as PDF, JSON or CSV.

Interactions: license search, SRCH-015 in [search-and-query.md](search-and-query.md); license
rules (SEC-052); legal reports (SEC-073).

### SEC-070 · License conclusion by priority
Priority: **Later**

For a component with several or conflicting declared licenses, the scanner chooses one *concluded*
license for the SBOM without discarding the declared ones. Each license has a priority (lower is
preferred); the default comes from its category and can be overridden per license (also to rank
licenses inside one category, such as MIT and ISC).

<details>
<summary>Default priority per license category</summary>

| Category | Priority |
|---|---|
| Public domain (for example CC0, Unlicense) | 1 |
| Permissive (MIT, BSD, Apache-2.0) | 2 |
| Weak copyleft (LGPL, MPL, EPL) | 3 |
| Source-available | 4 |
| Free with use restrictions | 4 |
| Strong copyleft (GPL, AGPL) | 5 |
| Commercial | 5 |
| Free proprietary (binary-only freeware) | 5 |
| Patent license | 5 |
| Contributor license agreement | 5 |
| No license stated | 5 |

</details>

Operations: list licenses; set license priority.

### SEC-071 · License attribution report
Priority: **Later**

For one artifact or release, a document of every bundled open-source component together with the
attribution its redistribution demands: component identifier (name, version, type, for example
`npm:lodash@4.17.21`), all
declared licenses, copyright statements extracted from license files, source headers or
manifests, and the unmodified full license text. Required notice files (such as an Apache-2.0
`NOTICE`) are a planned addition.

- Supported for npm, Maven, Go, PyPI, NuGet, Debian and Ubuntu packages; Alpine planned.
- Output format is open: PDF only, PDF and text, or PDF, JSON, CSV and text (the last set through
  the API); the spec decides. In the UI the report is generated from the resource export dialog
  (SEC-072).
- Requires the data source for copyright data.

## Reports, search and analysis of results

### SEC-072 · Scan result export per resource
Priority: **MVP**

From a resource version in the scan results list:

<details>
<summary>Export kinds and their fields</summary>

| Export | Fields |
|---|---|
| Security | CVE id, severity (CVSS based), research-adjusted severity, component path, affected component and version, last update time, applicability. |
| Legal | Component name, licenses, license links, package type, component id, package id, version. |
| Violations | Summary, severity, type, scope, component path, component, creation date, triggering policy, applicability. |
| SBOM | See SEC-066. |
| Operational risk | Component lifecycle and maintenance data (SEC-053). |
| Secrets and misconfigurations | Findings per category (SEC-082, SEC-083). |

</details>

Formats: CSV, JSON or PDF for reports; SBOM formats as in SEC-066.

### SEC-073 · Aggregated security reports
Priority: **Later**

Reports aggregate findings across many resources.

| Report | Content |
|---|---|
| Vulnerabilities | Vulnerabilities across a scope, with dashboards by severity and applicability, a top-10 list, drill-down with remediation guidance. |
| Legal (due diligence) | Components and artifacts with their licenses, including unknown and unrecognised licenses. |
| Violations | Policy violations by type, severity and applicability; top-10 CVE violations. |
| Operational risk | End-of-life status, version age, number of newer versions and similar data per component. |
| Secrets and misconfigurations | Findings, highlighting invoked components. |
| CVE search | Resources affected by given CVEs. |

- A wizard defines the scope (repositories, builds, release sets, projects) and filters (for
  example applicability or production-environment status).
- Scheduling daily, weekly or monthly; sharing by email.
- Generation runs in the background and returns a report id.

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| Generate report (per kind) | name, scope, filters | Report id | report manager (IAM-008); project administrator with security-asset privilege in a project |
| List reports | direction, page number, rows, order (all mandatory) | Reports | report manager |
| Get report details | id | Status, progress, row count, start and end time, kind | report manager |
| Get report content | id, pagination (mandatory) | Rows | report manager |
| Export report | id, format (PDF, JSON, CSV), file name | ZIP | report manager |
| Abort report | id | none | report manager |
| Delete report | id | Report and data removed permanently | report manager |

### SEC-074 · Affected-resource search
Priority: **MVP**

Answers "where is this vulnerability or package used?" across all scanned resources.

| Criterion | Notes |
|---|---|
| Vulnerability id | For example a CVE. |
| Package name | Usable alone. |
| Package type | Narrows a package search to an ecosystem. |
| Package version | Exact, or a bound with `>=`, `>`, `<=`, `<`. |
| Label | Public or private label (SEC-005); runs alone, cannot be combined; an unknown label returns nothing; very large labels are capped. |

Results: resource with link, repository, component and package details, path, last scan date; for
vulnerability searches the counts of active and ignored violations per result, and an ignore-rule
action per row. Export as CSV (with a violation-state column for vulnerability searches).

Limits: only indexed and scanned resources, as of their latest scan.

Related lookups at component level (not resource level):

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| Search resources by vulnerability or package | vulnerability id, or package name with optional type and version | Affected resources (backs the search above) | unspecified |
| Find components by CVE | CVE identifiers | Directly affected components per CVE with name, package type, version | unspecified |
| Find CVEs by component | component identifiers (for example a Maven coordinate) | CVE identifiers per component | unspecified |
| Find component by name | component name | Component known to the data source | read |

### SEC-075 · Scanned-resource search
Priority: **Later**

In the scan results list, an advanced search over scanned resources by issue name, license (or
unknown license), vulnerability severity (high, medium, low, unknown) and date range. Results are
grouped by resource kind, each with the number of resources containing the issue.

### SEC-076 · Summaries, comparison and differences
Priority: **Later**

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| Artifact summary | artifact paths or checksums | Vulnerabilities (with component detail), licenses, operational risks and errors per artifact | read |
| Build summary | build name, number | Same for a build | read |
| Compare artifacts | source and target artifact | Component lists: added (target only), removed (source only) and a third list whose definition is unspecified | read |
| Compare builds | source and target build | Same three component lists as for artifacts | read |
| Build vulnerability difference | two builds | Vulnerabilities added, removed, changed, unchanged, grouped by severity or package | read |
| Release set details | release set, operation (promotion or distribution) | Scan status and violations | read |

UI: a build security overview tracks issues across build versions and compares two versions
(CVEs added and resolved).

### SEC-077 · Remediation suggestions
Priority: **Later**

For a vulnerability in a scanned artifact the system suggests fixes, grouped as direct dependency
upgrade, base-image upgrade, transitive (lock file) upgrade and research suggestion, under the
strategies *best version*, *least vulnerable* and *quickest fix*. Two entry points exist:
CVE-based (the best fix for one vulnerability, deciding between upgrading a direct dependency and
overriding a transitive one along the impact path) and component-based (the upgrades that remove
the most risk overall). Candidate versions are checked with the ecosystem's own resolution rules
(for Maven, dependency management and overrides), semantic versioning and the data source, and a
candidate that introduces other known CVEs is ranked down; suggestions include ready-to-use
configuration snippets and are explicitly best effort. They need the dependency graph (SEC-068).

**Fix pull request on demand.** From one finding with a fix version, the user opens a pull request
in the source repository that produced the artifact: for a package artifact it upgrades the
vulnerable direct dependency in the package descriptor, for a container image the base image in
the Dockerfile. The source repository, branch and commit are taken from version-control
properties on the artifact, which the companion CLI records at upload and build publication. The
pull request is created by a CI workflow in the target repository that the system triggers.
Supported for Go, npm and Yarn, pnpm, Maven, Python (pip, uv, Poetry, Pipenv),
NuGet and Docker, only for direct dependencies and base images, and only for repositories on
GitHub's hosted service. The pull-request bot (SEC-085) opens such pull requests on its own during
commit scans; AI coding assistants can request fixes through the IDE plug-in and the tool server
(EVT-077 in [events-and-extensibility.md](events-and-extensibility.md)).

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| Get remediation options | artifact, vulnerability | Upgrade and fix suggestions | read |

### SEC-078 · Custom issues
Priority: **Later**

An external issue provider can register its own vulnerabilities (custom issues) with the scanner,
update and delete them, and read them back. Identifiers must not use the scanner's reserved prefix
and the provider must not be the built-in provider. Privilege: manage scan metadata (IAM-022 in
[identity-and-access.md](identity-and-access.md)).

### SEC-079 · Scanner audit log
Priority: **Later**

| Column | Meaning |
|---|---|
| Time | When it happened. |
| Correlation id | Groups related events of one operation. |
| Event id | Unique per event. |
| Event subject | Area concerned (resource, policy, configuration, re-evaluation). |
| Event name | Kind of event (for example vulnerability scan, policy update). |
| Triggered by | A user or the system. |

- Two kinds: user events (configuration, policy and scope changes) and system events (scans,
  indexing, synchronization, internal jobs).
- Detail panel with the structured event details (not searchable).
- Search syntax: `"Field": "value"` terms joined with `AND`; values limited to letters, digits,
  spaces and hyphens.
- CSV export of the current view, at most 10,000 records per export.
- Retention 365 days and at most about 25 million records (oldest removed first); both adjustable
  by configuration; removed events are unrecoverable.

### SEC-080 · Scanner operational settings
Priority: **Later**

<details>
<summary>Settings</summary>

| Setting | Meaning | Default |
|---|---|---|
| Scanner enabled | Set when the scanner is installed. | on |
| Allow download and distribution when unavailable | See SEC-064. | unspecified |
| Allow downloads of blocked artifacts | See SEC-064. | unspecified |
| Unscanned download timeout | See SEC-062. | 60 seconds |
| Skip TLS verification | Accept self-signed certificates. | unspecified |
| Mail without TLS | Connect to the mail server without TLS. | unspecified |
| Maximum disk usage | Percentage; above it no packages are downloaded for indexing. | unspecified |
| Monitoring sampling interval | Interval of CPU, disk and restart monitoring. | unspecified |
| Job interval | Interval of node-specific jobs. | unspecified |
| Workers per lane | Worker counts for indexing, persistence, analysis, alerting, re-evaluation and notification; separate counts for new and existing content; restart required. | unspecified |
| Queue message retries | Retries accepted in the message queue. | unspecified |

</details>

Further operations: export selected user-defined scanner settings to an archive and import them
again; liveness, readiness (with an option to bypass cached results) and ping probes without
authentication; version information.

### SEC-092 · Security overview dashboard
Priority: **MVP**

A scanner landing page summarises scanning coverage, secret findings, supply-chain risk and policy
state for the installation. A threat view on it leads into the affected-resource search (SEC-074);
that feed is SEC-094. Widgets and metrics beyond that are unspecified.

### SEC-094 · Threat intelligence feed with impact check
Priority: **Later**

A tab of the scanner's overview page shows a daily updated feed of security posts about new
attacks, CVEs and malicious packages, newest first. Most posts come from the data provider's own
research team, which watches public registries (npm, PyPI, Hugging Face and others), scans new
package versions automatically, investigates suspicious ones and analyses CVEs for real-world
exploitability; external posts are marked as such.

- **Post content.** Source (provider or external), publish date, topic tags (for example an
  ecosystem such as npm), a link to the full advisory or article, optionally a link to a fix, and,
  when the post concerns a known attack, the public label (SEC-005) that groups the affected
  packages and versions.
- **Severity mismatch marker.** Shown when the provider's research rates a CVE differently from
  its published severity; the research rating reflects exploitability, not only the score.
- **Filters.** All, CVE, malicious package.
- **Check impact.** On posts with a searchable CVE or public label, one action opens the
  affected-resource search (SEC-074) for that CVE or label across the installation and lists the
  matching artifacts with their last scan date. Only indexed and scanned artifacts count.

**Limits.** Needs the package intelligence service (SEC-090); when it is unreachable the feed is
empty and shows an error.

**Interactions.** SEC-001, SEC-004, SEC-074, SEC-092.

### SEC-095 · Scanner processing failures and retries
Priority: **Later**

An administration page lists every failure of the scanner's processing so that an administrator
can fix the cause and retry the step.

| Pipeline | When it runs | Steps |
|---|---|---|
| Scanning | A new artifact or build is added | check, index, persist, analysis, alert, notify, write-back of results to the repository manager |
| Impact analysis | New component data arrives (vulnerabilities, licenses), and the update matches a scan scope | analysis, alert, notify, write-back |

| Column of the failure list | Meaning |
|---|---|
| Subject | The artifact being scanned, or for impact analysis the data update (for example a vulnerability or license name) |
| Source | Instance, repository and path of the artifact, or the origin of the data update (database synchronization, a custom issue SEC-078) |
| Step | Pipeline and step that failed |
| Time | When it failed; newest first by default |
| Error | Detailed error message |

A second tab shows the number of retry messages per pipeline step. Retrying from the UI is
presumably possible; its exact controls are unspecified.

**Interactions.** SEC-047, SEC-048, SEC-080, SEC-096.

### SEC-096 · Scanner system status
Priority: **Later**

The scanner reports one overall status, accumulated from the following checks; each can raise a
warning or an error:

| Check | Warning | Error |
|---|---|---|
| Connection to its database | — | no connection |
| Connection to its message broker | — | no connection |
| Connection to the data provider's update server | — | no connection |
| Connection to the repository-manager instances | — | no connection |
| Service restarts | 3 within 5 hours | 50 within 5 hours |
| Average CPU use | 90 % | 95 % |
| Average memory use | 90 % | 95 % |
| Open file handles | 80 % of the maximum | 95 % of the maximum |
| Working-directory disk use | 80 % | 95 % |
| Data-directory disk use | 80 % of the configured maximum | 95 % of the configured maximum |
| Failed processing messages (SEC-095) | more than 0 | more than 100 |

The sampling interval is a scanner setting (SEC-080).

**Interactions.** SEC-080, SEC-095; health probes of the repository manager:
[operations-and-monitoring.md](operations-and-monitoring.md).

## Extended analyses

### SEC-081 · Vulnerability applicability analysis
Priority: **Later**

Decides whether a CVE found in an artifact can actually be exploited there, using scanners per
CVE that combine reachability of the vulnerable function, compile-time options visible in
binaries, configuration files, command-line flags and properties of the runtime environment
(OS, distribution, CPU architecture, bitness, file permissions). Covers source code and binaries,
including container images and fat JARs. Each result carries a justification and evidence (call
chains, direct or transitive, with file paths and lines).

Statuses: not scanned, applicable, not applicable, undetermined, rescan required (a new scanner
exists), not covered (no scanner), technology unsupported, missing context, upgrade required.

Used by the security rule option *skip not applicable* (SEC-051) and in reports. Operations:
trigger and cancel an applicability scan of an artifact, get its status, get results per
vulnerability.

Switching these extended analyses on does not scan existing content: they apply to newly added
artifacts from then on, and existing repositories or single artifacts must be scanned once on
demand (applicability, exposures, secrets; SEC-082, SEC-083).

### SEC-082 · Secret detection in stored artifacts
Priority: **Later**

Finds credentials in source and binary content: structured access tokens and API keys,
high-entropy strings, credentials embedded in URLs, and certificate problems (private keys inside
certificates, expired or self-signed certificates). Optional token validation tests found tokens
against their provider and classifies them as active, inactive, unsupported or unavailable; it
works only against public SaaS endpoints, not self-hosted instances; a self-hosted scanner needs
outbound access to each provider's validation endpoint. Noise reduction skips well-known example
keys, values matching test patterns (such as `xxxx`, `test`, `example`, `123456`), repeated or
sequential characters, keys literally named `key`, documentation directories, third-party package
files, placeholder credentials in URLs and URLs to image files. Custom detectors can be defined
with an RE2 regular expression. Findings can be ignored by detector, finding, file,
artifact, build or release set. Operations: trigger, cancel and query secret and misconfiguration
scans per artifact or repository; list results, details, findings and evidence.

### SEC-083 · Misconfiguration and first-party code analysis
Priority: **Later**

- Infrastructure as code: Terraform modules and stored state files checked for cloud
  misconfigurations.
- Services: configuration of common services inside containers (web servers, proxies, databases).
- Application libraries: insecure use of libraries in Python and Node.js (weak cryptography,
  unsafe execution).
- Static analysis of first-party code, run locally so source code is not uploaded, with cross-file
  data-flow analysis and fingerprint-based ignores.

### SEC-084 · Runtime-aware filtering and runtime monitoring
Priority: **Later**

- *Skip if not running*: a security rule option that suppresses violations (and therefore all
  their actions) for container images not observed running in a connected Kubernetes cluster
  within a look-back window (default 24 hours, allowed 24 hours to 10 days, set at installation
  level). It has no effect on non-container artifacts. Without a connected cluster agent the
  option is inert and violations are raised as usual (fail-safe). It takes effect at the next scan;
  an image that starts running is re-evaluated only at the next scan cycle; the violation list does
  not show what was suppressed.
- Runtime monitoring of clusters (running images, processes, integrity, untrusted registries) is a
  separate system, not a repository function; one line here for completeness.

### SEC-085 · Source-code scanning outside the registry
Priority: **Later**

IDE plug-ins, the companion command-line tool and a pull-request bot scan source code and
dependency manifests before artifacts exist, post results on pull requests and upload them to the
platform, where Git repositories become a scan scope resource kind (SEC-055) with their own
policies, violations and ignore rules. Mentioned for completeness; not a registry function.

### SEC-086 · Patched-package substitution at resolution time
Priority: **Later**

Replaces vulnerable Maven and npm packages with verified patched builds when clients resolve
dependencies, so builds keep working. Patched builds come from rebuilds supplied by the data
provider and from locally supplied patches, plus automations and a record of which request received
which substitute. Capability level only.

### SEC-087 · Malicious model and agent-skill detection
Priority: **Later**

Scanning of machine learning models (for example from Hugging Face) and of AI agent skill
packages for malicious or suspicious behavior, feeding the malicious flag (SEC-004). A separate
policy kind for machine learning models can block or alert on model versions that are not
approved; its criteria are unspecified.

### SEC-093 · Snippet detection
Priority: **Later**

Determines where AI-generated or copied code snippets in first-party source come from and what risk
they carry. It runs through the pull-request bot and the companion command-line tool (SEC-085),
not inside the registry. Capability level only.

### SEC-098 · CI workflow injection detection
Priority: **Later**

A static check of GitHub Actions workflow files (`.github/workflows/*.yml` and `*.yaml`) for the
"pwn request" pattern: a workflow triggered by `pull_request_target`, which runs with the target
repository's privileges and secrets, checks out the pull request's head code (head SHA, head ref
or the pull request merge ref) and so runs code from a fork in a privileged context. A finding is
raised only when both conditions hold and no known mitigation is present: a condition that stops
the workflow, or every job, for pull requests from forks; or a condition that limits it, or every
job, to trusted bot accounts such as Dependabot or Renovate. It runs in the pull-request bot's
commit and pull-request scans (SEC-085) and when a container image that contains such workflow
files is scanned. Capability level only.

The GitLab counterpart, if trove ever builds one, concerns merge-request pipelines from forks
running with the parent project's protected variables (CIOIDC-036 in
[oidc-ci-integration.md](oidc-ci-integration.md)).

## Out of scope

### SEC-088 · Entitlement checks
Priority: **Out of scope**

Gating curation and its health check on an entitlement service and on minimum server versions,
that is on billing, subscriptions and license management of a hosted service. No meaning for a
self-hosted repository manager.

### SEC-089 · Data-provider malicious-package digest
Priority: **Out of scope**

An opt-in daily email from the data provider summarising newly found malicious packages of the
last 24 hours. A notification service of the data provider, not a repository feature.

## Notes for implementers

- Enforcement must sit on the file download, not only on metadata. Lock files, exact pins
  (`npm ci`, `dotnet restore` with a lock file, NuGet `[x.y.z]`, Maven fixed versions) bypass
  version discovery, so a blocked tarball or `.nupkg` must still return 403.
- Compliant version fallback (SEC-025) filters from the newest version down and stops at the first
  compliant one. Older blocked versions stay listed, so clients can still pick them and then fail
  with 403. Code that expects listings to contain only compliant versions breaks on this.
- npm: recompute only the `latest` dist-tag after filtering. Other dist-tags may point at removed
  versions, and clients then fail with `ETARGET`. When every version is blocked, the package
  document is withheld.
- NuGet `--version x` means "x or higher", so fallback applies; only `[x]` is exact. Search
  results are not filtered, so a search hit can fail to install.
- Blocks are HTTP 403 with a readable reason; npm shows the reason to the user. In a virtual
  repository it is open whether a 403 from a curated member stops resolution
  (SEC-027). Pick one behavior and document it.
- Pending data is decided per policy (SEC-022). Dry-run policies never block pending packages.
  A waiver for a pending package covers the whole package, expires after 2 days by default and
  is never permanent.
- Without enforcement on cached packages (SEC-023), anything cached keeps flowing after its risk
  changes. Waiver expiry and group scopes rely on cache enforcement.
- Immaturity needs a trustworthy upstream publish time (SEC-014). Falling back to the local fetch
  time blocks old packages as "new" the first time they are fetched.
- Blocking unscanned downloads (SEC-062) holds the client connection for up to 60 seconds by
  default. Client and ingress timeouts must be longer than that.
- Ignore rules and new scan scopes affect only future evaluations (SEC-056, SEC-058). Without an
  explicit re-application, existing violations and blocks stay as they are.
