# Chef cookbook format

This page catalogs what a repository manager does to serve Chef cookbooks: local, remote and virtual
repositories that each behave like a Chef *Supermarket* (the cookbook site Chef tooling talks to),
cookbook metadata calculation, publishing and installing with Knife, dependency resolution with
Berkshelf, and authentication. Chef uses the word "repository" for a workstation folder of Chef
data; on this page "repository" always means a repository of the system, and each Chef repository
of the system is a Supermarket in Chef's terminology. The Supermarket API itself is Chef's; where an
entry derives behavior from that API, it says so. Throughout this page,
`<format-url>` stands for a repository's Chef endpoint URL (the Supermarket URL clients are
configured with) and `https://repo.example.com` for the system's base URL; the concrete path layout
is the implementation's choice. `chef-local`, `chef-remote` and `chef-virtual` are example
repository keys.

## Repositories

### PKG-CHEF-001 · Chef repositories in all three repository classes
Priority: **P2**

Chef is a repository format for local, remote and virtual repositories (REPO-006 in
[../repositories.md](../repositories.md)):

- local repositories host an organization's own cookbooks; the system computes their metadata
  (PKG-CHEF-003);
- remote repositories proxy and cache a Supermarket such as `https://supermarket.chef.io`
  (PKG-CHEF-004);
- virtual repositories merge local and remote Chef repositories behind one Supermarket URL
  (PKG-CHEF-005).

Supported tools: Knife and Berkshelf from Chef Workstation. Permissions can be granted per
repository and path like for any format (IAM-019 in
[../identity-and-access.md](../identity-and-access.md)).

Creating a repository requires the system administrator or a project administrator role. UI
location: administration, repositories, create repository, choose the class and the Chef format
(UI-031 in [../ui-inventory.md](../ui-inventory.md)).

### PKG-CHEF-002 · Supermarket endpoint per repository
Priority: **P2**

Every Chef repository answers the Supermarket API under its Chef endpoint URL. All Knife and
Berkshelf commands use that URL, not the plain file URL of the repository.

- Clients reach the API under `<format-url>/api/v1` and fetch a cookbook index from the repository
  (Berkshelf's "fetching cookbook index" step).
- The individual endpoints are unspecified. The client operations (PKG-CHEF-006 to PKG-CHEF-008)
  map to Supermarket API calls for listing, searching, showing and downloading
  cookbooks, sharing (uploading) a cookbook with a category, and unsharing (deleting) a cookbook
  version; Berkshelf additionally reads the dependency index of all cookbooks (Supermarket's
  `universe`). An implementation must follow the Supermarket API as the Chef clients use it.

### PKG-CHEF-003 · Cookbook metadata calculation
Priority: **P2**

The system calculates metadata for cookbooks hosted in local repositories, so they can be listed,
searched and resolved through the Supermarket API. Which fields are extracted, when the calculation
runs and whether a manual recalculation exists are unspecified.

### PKG-CHEF-004 · Remote Chef repositories
Priority: **P2**

A remote Chef repository proxies an upstream Supermarket (for example `https://supermarket.chef.io`)
and caches the cookbooks clients request (RMT-001 in
[../remote-repositories.md](../remote-repositories.md)). Caching of the upstream's index and
cookbook metadata follows the general metadata retrieval cache period (RMT-018); a Chef-specific
value is unspecified.

### PKG-CHEF-005 · Virtual Chef repositories
Priority: **P2**

A virtual Chef repository merges the cookbook indexes of its local and remote members behind one
Supermarket URL (REPO-033 in [../repositories.md](../repositories.md)).

| Setting | Meaning | Default |
|---|---|---|
| Metadata cache period | Seconds merged metadata is reused before the members are checked again; 0 disables caching (REPO-037). | 7200 (the general virtual-repository default, which applies to Chef) |

Example: Berkshelf resolves through a virtual repository (PKG-CHEF-010). Merge rules for the same
cookbook version in several members are unspecified (general rules: REPO-035).

## Client operations

### PKG-CHEF-006 · Publish cookbooks
Priority: **P2**

- Knife: `knife supermarket share <cookbook> [<category>]` uploads a cookbook to the configured
  Supermarket. The optional category (for example "Web Servers", "Databases") organizes cookbooks.
  With the authentication plugin of PKG-CHEF-009, the equivalent wrapper command is used.
- UI: the deploy dialog uploads a cookbook archive to a Chef repository (UI-013 in
  [../ui-inventory.md](../ui-inventory.md)).
- Required permission: deploy on the target repository (IAM-022 in
  [../identity-and-access.md](../identity-and-access.md)).
- Where the system stores a shared cookbook (path layout) is unspecified.

### PKG-CHEF-007 · Install, download and inspect cookbooks
Priority: **P2**

The Knife Supermarket commands work against the repository: install (latest or a given version),
download, list, search and show. Example with the authentication plugin's wrapper (PKG-CHEF-009):
install `nginx` in its latest version or in version `2.3.0`.

### PKG-CHEF-008 · Unshare a cookbook version
Priority: **P2**

The Knife unshare command (available as a wrapper of the authentication plugin, PKG-CHEF-009)
removes a cookbook version from the Supermarket. The required permission is unspecified; delete on
the stored cookbook (IAM-022) is the natural mapping. Whether unshare is supported on remote and
virtual repositories is unspecified.

## Client setup and authentication

### PKG-CHEF-009 · Knife configuration and authentication
Priority: **P2**

Knife is pointed at the repository by setting its default Supermarket in `~/.chef/config.rb` (or the
older `~/.chef/knife.rb`, which Knife reads when `config.rb` is missing; `--config` overrides the
location):

```ruby
# anonymous
knife[:supermarket_site] = '<format-url>'
# authenticated: credentials as user information in the same URL
knife[:supermarket_site] = 'https://<user>:<token>@<host>/<path-of-format-url>'
```

- Anonymous form: the first line; works when anonymous access is enabled (AUTHN-036 in
  [../authentication-providers.md](../authentication-providers.md)). Anonymous access is allowed
  for Chef repositories by default; turning it off is needed to trace
  which user does what.
- Authenticated form: credentials embedded in the URL. Plain Knife cannot send HTTP Basic
  credentials (it only signs requests with RSA keys), so authenticated access needs an additional
  Knife plugin, distributed as a Ruby gem and installed with `chef gem install`. The plugin adds wrapper commands for download,
  install, list, search, share, show and unshare that send Basic credentials taken from the URL.
- The password part can be an access token (TOK-034 in [../access-tokens.md](../access-tokens.md))
  or an encrypted password (IAM-035 in [../identity-and-access.md](../identity-and-access.md)).
- UI: client setup dialog, Chef instructions (UI-021, UI-026 in
  [../ui-inventory.md](../ui-inventory.md)).

### PKG-CHEF-010 · Berkshelf configuration
Priority: **P2**

Berkshelf resolves cookbook dependencies against the repository instead of only the public
Supermarket:

- the `Berksfile` names the repository's Chef endpoint URL as its default source, using
  Berkshelf's source type for repository managers;
- the token goes into `~/.chef/config.rb` under Berkshelf's API-key setting for that source type;
- running `berks` in the cookbook directory resolves and downloads dependencies through the
  repository.

Berkshelf 6.1.0 and later support token authentication; earlier versions only anonymous access.
Berkshelf is in maintenance-only mode ([Berkshelf](https://github.com/berkshelf/berkshelf)).

## Notes for implementers

- Clients use the Chef endpoint URL, never the plain file URL; every cookbook operation goes
  through the Supermarket API (PKG-CHEF-002).
- Berkshelf reads the complete dependency index of a repository before resolving; for virtual
  repositories this index merges all members and must stay fast (PKG-CHEF-005).
- Knife without the plugin cannot authenticate with Basic credentials; disabling anonymous access
  breaks plain Knife clients (PKG-CHEF-009).
- Credentials embedded in `config.rb` URLs are stored in plain text on workstations; recommend
  tokens with read-only scope for resolution.
- Shared cookbooks carry a category; keep it as metadata so list and search return it
  (PKG-CHEF-006).
