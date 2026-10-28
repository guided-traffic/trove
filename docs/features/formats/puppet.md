# Puppet module format

This page catalogs what a repository manager does to serve Puppet modules: local, remote and
virtual repositories that answer the Puppet Forge v3 API, free placement of module archives,
metadata calculation, the Forge operations exposed for automation, and the configuration of the
`puppet`, r10k and librarian-puppet clients (including a reverse-proxy workaround for old Puppet
clients). The Forge API is Puppet's ([Puppet Forge API](https://forgeapi.puppet.com/)); entries whose
behavior follows from it say so. Throughout this page,
`<repo-url>` stands for the URL under which a repository's files are served by path,
`<format-url>` for the repository's Puppet endpoint URL (the Forge URL clients are configured with)
and `https://repo.example.com` for the system's base URL; the concrete path layout is the
implementation's choice. `puppet-local`, `puppet-remote` and `puppet-virtual` are example
repository keys.

## Repositories

### PKG-PUPPET-001 · Puppet repositories in all three repository classes
Priority: **P2**

Puppet is a repository format for local, remote and virtual repositories (REPO-006 in
[../repositories.md](../repositories.md)):

- local repositories host an organization's own modules; the system computes their metadata
  (PKG-PUPPET-004);
- remote repositories proxy and cache a Forge such as `https://forgeapi.puppet.com/`
  (PKG-PUPPET-006);
- virtual repositories merge local and remote Puppet repositories behind one Forge URL
  (PKG-PUPPET-007).

Supported clients: Puppet 4.9.2 and later (or 5.x and later) work directly. Puppet up to 4.9.1
cannot use a Forge URL with a path prefix and needs a reverse proxy (PKG-PUPPET-011).

Creating a repository requires the system administrator or a project administrator role. UI
location: administration, repositories, create repository, choose the class and the Puppet format
(UI-031 in [../ui-inventory.md](../ui-inventory.md)).

### PKG-PUPPET-002 · Forge v3 API per repository
Priority: **P2**

Every Puppet repository answers the Forge v3 API under its Puppet endpoint URL
(`<format-url>/v3/...`). The Puppet CLI, r10k and librarian-puppet use it; the operations below are
also offered for automation.

| Operation | Inputs | Output | Required privilege |
|---|---|---|---|
| List modules | repository key | paginated result: total, limit, offset, links to current, next and previous page, and the list of modules | unspecified (401/403 responses) |
| Get module | repository key; module identifier `<owner>-<module>` | module information; 404 if unknown | unspecified (401/403 responses) |
| List releases | repository key | paginated list of releases (module versions), same envelope | unspecified (401/403 responses) |
| Get release | repository key; release identifier `<owner>-<module>-<version>` | release information; 404 if unknown | unspecified (401/403 responses) |

- No query parameters are specified for the two list operations, although their results are
  paginated; the Forge v3 API defines paging (`limit`, `offset`) and filters such as the module
  of a release list, and clients send them, so an implementation must accept them.
- The pagination fields must not be returned as a flat envelope: Forge v3 nests them in a
  pagination object. The response shapes must follow the Forge v3 API as the clients parse it.
- Release downloads (the module archive) go through the Forge file endpoint (Forge API), which is
  not among the operations above.
- The required privilege is unspecified beyond 401/403 responses; read permission on the
  repository (IAM-022 in [../identity-and-access.md](../identity-and-access.md)) is the natural
  mapping.

### PKG-PUPPET-003 · Free layout and module archive deploy
Priority: **P2**

- A module archive is the `.tar.gz` file produced by `puppet module build`.
- It can be deployed to any path of a local repository; the repository layout is free (for
  example `<module>/<module>-<version>.tar.gz`). Permissions can follow the chosen
  layout (IAM-021).
- Deploy with a plain HTTP `PUT` to `<repo-url>/<path>/<file>.tar.gz`, or with the UI deploy dialog
  (UI-013 in [../ui-inventory.md](../ui-inventory.md)). Applies to local repositories only.
- Required permission: deploy on the target path (IAM-022).
- Module identity (owner, name, version) is read from the archive's metadata, not from the path.

### PKG-PUPPET-004 · Module metadata calculation and properties
Priority: **P2**

The system calculates metadata for modules in local repositories and annotates every deployed or
cached module with two properties, module name and module version (property keys are the
implementation's choice). They support property search for Puppet packages (SRCH-007 in
[../search-and-query.md](../search-and-query.md)). Whether a manual recalculation operation exists
is unspecified.

### PKG-PUPPET-005 · Asynchronous search availability
Priority: **P2**

A newly published module is not necessarily visible to search and listing at once:

- local repositories compute the search index asynchronously after a publish;
- a virtual repository inherits that delay from its local members;
- a remote repository finds a new upstream module only after the metadata retrieval cache period
  has expired (RMT-018 in [../remote-repositories.md](../remote-repositories.md)).

### PKG-PUPPET-006 · Remote Puppet repositories
Priority: **P2**

A remote Puppet repository proxies a Forge (for example `https://forgeapi.puppet.com/`) and caches
the modules clients request (RMT-001 in [../remote-repositories.md](../remote-repositories.md)).
Cached modules get the name and version properties (PKG-PUPPET-004).

### PKG-PUPPET-007 · Virtual Puppet repositories
Priority: **P2**

A virtual repository serves several Puppet repositories through one Forge URL. This removes a
limitation of the Puppet client, which talks to one module repository at a time. The recommended
client configuration points at a virtual repository. Merge rules for the same release in several
members are unspecified (general rules: REPO-035 in [../repositories.md](../repositories.md)).

## Client setup

### PKG-PUPPET-008 · Puppet CLI configuration
Priority: **P2**

The client setup instructions (UI-021, UI-026 in [../ui-inventory.md](../ui-inventory.md)) set the
module repository in `puppet.conf`:

```ini
[main]
module_repository=https://<user>:<token>@<host>/<path-of-format-url>
```

- `puppet module install <owner>-<module>` and `puppet module search` then use the repository.
- `--module_repository=<url>` on the command line overrides the setting for one command.
- Credentials are embedded in the URL as HTTP Basic user information; the password part can be an
  access token (TOK-034 in [../access-tokens.md](../access-tokens.md)).
- The Puppet client keeps caches of downloaded modules and of the Forge's JSON responses; clearing
  both before switching to the repository is recommended, so that no cached response from the
  public Forge remains.

### PKG-PUPPET-009 · r10k configuration
Priority: **P2**

r10k fetches environments and modules from the repository when `r10k.yaml` sets the Forge base URL:

```yaml
forge:
  baseurl: '<format-url>'
```

Then `r10k puppetfile install` installs the modules of the `Puppetfile`.

### PKG-PUPPET-010 · librarian-puppet configuration
Priority: **P2**

librarian-puppet uses the repository when the `Puppetfile` names it as Forge:

```ruby
forge "<format-url>"
mod 'puppetlabs-mysql', '3.7.0'
```

It must be run with `librarian-puppet install --no-use-v1-api`, because the repository serves only
the v3 API.

### PKG-PUPPET-011 · Reverse-proxy workaround for old Puppet clients
Priority: **Later**

Puppet up to 4.9.1 sends Forge requests to `/v3/...` at the root of the configured host and ignores
any path. To serve such clients, a reverse proxy in front of the system rewrites `/v3/*` to
`<format-url>/v3/*` of one repository (typically a virtual one). Two arrangements are possible:

| Arrangement | How the repository is chosen | Client setting |
|---|---|---|
| Port per repository | A dedicated listener port forwards `/v3/*` to one fixed repository. | `--module_repository http://<proxy-host>:<port>` |
| Subdomain per repository | The first label of the host name is the repository key (`<repo-key>.<proxy-domain>`), and `/v3/*` is rewritten to that repository. | `--module_repository http://<repo-key>.<proxy-domain>` |

Both can be built with NGINX or Apache HTTP Server. The proxy must also tell the system its
externally visible base URL, so that URLs in responses point back through the proxy (the proxy
sets the per-request base-URL override, OPS-052 in
[../operations-and-monitoring.md](../operations-and-monitoring.md); the configured external base
URL is OPS-051).

### PKG-PUPPET-012 · Anonymous and authenticated access
Priority: **P2**

Anonymous access is allowed for Puppet repositories by default; turning it off (AUTHN-036 in
[../authentication-providers.md](../authentication-providers.md)) is needed to trace which user
does what, after which every client must send credentials (PKG-PUPPET-008).

## Notes for implementers

- Clients call `<format-url>/v3/...`; the plain file URL of the repository is only for deploys
  (PKG-PUPPET-002, PKG-PUPPET-003).
- Puppet clients up to 4.9.1 drop the path of the configured URL; supporting them requires a
  reverse proxy or host-based routing (PKG-PUPPET-011).
- librarian-puppet falls back to a v1 API unless told not to; the repository serves only v3
  (PKG-PUPPET-010).
- Response shapes must match the real Forge v3 API (nested pagination, release objects with file
  URIs), not a flat shape (PKG-PUPPET-002).
- Search visibility of new modules is asynchronous; publish-then-install pipelines must tolerate
  a short delay (PKG-PUPPET-005).
- Stale client caches from the public Forge cause confusing version results after switching to the
  repository (PKG-PUPPET-008).
