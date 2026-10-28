# Ansible collection format

This page catalogs what a repository manager does to serve Ansible content to `ansible-galaxy`:
local, remote and virtual repositories for collections, a subset of the Galaxy v3 API per
repository, publishing and installing collections, installing legacy roles through remote
repositories, metadata calculation, and proxying Ansible Galaxy and Red Hat Ansible Automation Hub.
The Galaxy v3 API is Ansible's
([Galaxy v3 API](https://docs.ansible.com/projects/galaxy-ng/en/latest/community/api_v3.html)); where
an entry relies on it rather than on the described system, it says so. Throughout this page,
`<repo-url>` stands for the URL under which a repository's files are served by path,
`<format-url>` for the repository's Ansible endpoint URL (the Galaxy server URL clients are
configured with) and `https://repo.example.com` for the system's base URL; the concrete path layout
is the implementation's choice. `ansible-local`, `ansible-remote` and `ansible-virtual` are example
repository keys.

## Repositories

### PKG-ANSIBLE-001 · Ansible repositories in all three repository classes
Priority: **P2**

Ansible is a repository format for local, remote and virtual repositories (REPO-006 in
[../repositories.md](../repositories.md)):

- local repositories host an organization's own collections; roles cannot be published to them
  (PKG-ANSIBLE-007);
- remote repositories proxy and cache public collections from Ansible Galaxy (PKG-ANSIBLE-009) or
  Red Hat Ansible Automation Hub (PKG-ANSIBLE-010), and resolve roles;
- virtual repositories merge local and remote Ansible repositories behind one Galaxy server URL
  (PKG-ANSIBLE-011).

Supported clients: ansible-core 2.17 and later, the Ansible community package 10.x and later.

Interactions: the federated repository form offers a signing key pair selector for Ansible (shared
with Alpine); no signing is specified for Ansible, so what such a key would sign is
unspecified (federation is outside this page's scope, see
[../replication-and-federation.md](../replication-and-federation.md)).

Creating a repository requires the system administrator or a project administrator role. UI
location: administration, repositories, create repository, choose the class and the Ansible format
(UI-031 in [../ui-inventory.md](../ui-inventory.md)).

### PKG-ANSIBLE-002 · Galaxy v3 API per repository
Priority: **P2**

Every Ansible repository answers a subset of the Galaxy v3 API relative to its Ansible endpoint URL
(`<format-url>/`). Optional query parameters such as pagination and namespace filters behave as in
Galaxy v3.

| Operation | Path below the endpoint URL | Inputs | Output |
|---|---|---|---|
| Get collection | `api/v3/collections/<namespace>/<name>` | namespace, collection name | metadata of one collection |
| List collection versions | `api/v3/collections/<namespace>/<name>/versions` | namespace, name; `limit`, `offset` | paginated list of versions |
| Get collection version | `api/v3/collections/<namespace>/<name>/versions/<version>` | namespace, name, semantic version | metadata of one version, including the fields needed to download it |

- Required privilege: unspecified; read on the repository (IAM-022 in
  [../identity-and-access.md](../identity-and-access.md)) is the natural mapping. Example requests
  authenticate with user name and token as HTTP Basic credentials.
- Trailing slashes: clients may or may not append `/`; retrying with or without it is advised,
  which implies the system should accept both.
- Implicit client requirements (Galaxy behavior): `ansible-galaxy` first reads the API root to
  discover the available API versions, publishes through the collection upload endpoint and then
  polls an import task, and installs by following the download URL from the version metadata.
  Publishing and installing work, so these endpoints exist even though the table lists only the
  three read operations.

## Collections

### PKG-ANSIBLE-003 · Publish collections with ansible-galaxy
Priority: **P2**

```sh
ansible-galaxy collection publish geerlingguy-k8s-1.0.0.tar.gz
ansible-galaxy collection publish geerlingguy-k8s-1.0.0.tar.gz -s <format-url> --token=<token>
```

- The first form uses the server configured in `ansible.cfg` (PKG-ANSIBLE-012); the second names the
  server and token explicitly and overrides `ansible.cfg` ("server explicit").
- The target may be a virtual repository, which forwards to its default deployment
  repository (REPO-038 in [../repositories.md](../repositories.md)).
- Required permission: deploy on the target repository (IAM-022).

### PKG-ANSIBLE-004 · Deploy collection archives through the UI or plain HTTP
Priority: **P2**

- UI: the deploy dialog uploads a collection archive (UI-013 in
  [../ui-inventory.md](../ui-inventory.md)).
- API: `PUT <repo-url>/<path>/<collection-archive>.tar.gz` like any artifact; deploy by checksum and
  archive deploy also work (ART-004, ART-005 in [../artifacts.md](../artifacts.md)).

### PKG-ANSIBLE-005 · Install collections
Priority: **P2**

```sh
ansible-galaxy collection install <namespace>.<name>
ansible-galaxy collection install <namespace>.<name> -s <format-url> --token=<token>
```

The first form uses `ansible.cfg`; the server-explicit form bypasses it, for example when
`ansible.cfg` points at another registry. Collections resolve from local, remote and virtual
repositories. Some examples pass an archive file name to the install command; the standard form
names the collection.

### PKG-ANSIBLE-006 · Collection metadata calculation and recalculation
Priority: **P2**

The system calculates metadata for collections to support artifact management and search (SRCH-050
in [../search-and-query.md](../search-and-query.md)). Which fields are extracted, when the
calculation runs and how it relates to the Galaxy API answers (PKG-ANSIBLE-002) are unspecified.

A local repository's index can be recalculated on demand: administration, repositories, local, the
repository's row menu, "recalculate index" (REPO-009 in [../repositories.md](../repositories.md)).
Required privilege: administrator. No API operation is specified.

## Roles

### PKG-ANSIBLE-007 · Install legacy roles through remote repositories
Priority: **P2**

Roles are the older Ansible packaging, still installable:

```sh
ansible-galaxy role install <namespace>.<role>
ansible-galaxy role install <namespace>.<role> -s <format-url> --token=<token>
```

- Roles resolve only through remote repositories.
- Roles cannot be published to local repositories; they have to be wrapped into collections first.
- Installing roles from Git repositories through a remote repository is not supported.
- Credentials: either in `~/.netrc` (PKG-ANSIBLE-013) or as an explicit token.
- The role download protocol (Galaxy's older role API) is unspecified.

## Remote and virtual repositories

### PKG-ANSIBLE-008 · No browsing of uncached remote content
Priority: **P2**

The artifact browser of an Ansible remote repository shows only what has been pulled into the
cache, not the content of the upstream registry (ART-044 in [../artifacts.md](../artifacts.md)).

### PKG-ANSIBLE-009 · Remote repositories for Ansible Galaxy
Priority: **P2**

A remote repository proxies public collections from Ansible Galaxy and caches them (RMT-001 in
[../remote-repositories.md](../remote-repositories.md)). The pre-filled upstream URL and the caching
of collection metadata are unspecified for Ansible; the general metadata retrieval cache period
applies (RMT-018). The general switch between storing fetched content and passing it through
without a cache does not apply to Ansible remote repositories (RMT-017); whether they always store
content is unspecified.

### PKG-ANSIBLE-010 · Proxy Red Hat Ansible Automation Hub
Priority: **P2**

A remote repository can proxy Red Hat Ansible Automation Hub, which serves certified and validated
collections behind Red Hat single sign-on. The upstream credential is a long-lived *offline token*
(a refresh token issued by Red Hat SSO); the system exchanges it at the SSO token endpoint for
short-lived access tokens and renews them automatically when talking to the upstream.

| Setting | Value for Automation Hub | Default |
|---|---|---|
| URL | `https://console.redhat.com/api/automation-hub/` | — |
| Password / access token | The offline token from the Red Hat API token page; stored as a secret. | unspecified (optional) |
| Token authentication | On: the credential is used for token-based authentication instead of HTTP Basic (RMT-006). | off |
| Token endpoint URL ("auth URL") | `https://sso.redhat.com/auth/realms/redhat-external/protocol/openid-connect/token` | unspecified |

- Prerequisite: an active Red Hat account with Automation Hub access.
- The token endpoint URL is a remote-repository setting not specified for other formats; how the
  exchange request is built (grant type, client identifier) is unspecified. Since an
  offline token is a refresh token and the URL is an OpenID Connect token endpoint, the exchange is
  presumably the OAuth 2.0 refresh-token grant.
- UI: remote repository settings, basic tab.

### PKG-ANSIBLE-011 · Virtual Ansible repositories
Priority: **P2**

A virtual repository serves local and remote Ansible repositories through one Galaxy server URL; it
is the recommended client target and accepts publishes through its default deployment repository
(PKG-ANSIBLE-003). Merge rules for the same collection version in several members are unspecified
(general rules: REPO-035 in [../repositories.md](../repositories.md)).

## Client setup

### PKG-ANSIBLE-012 · ansible.cfg configuration for collections
Priority: **P2**

The client setup instructions (UI-021, UI-026 in [../ui-inventory.md](../ui-inventory.md)) render:

```ini
[galaxy]
server_list = ansible-virtual

[galaxy_server.ansible-virtual]
url=<format-url>
token=<token>
```

- `server_list` can name several servers, comma-separated (for example a virtual and a local
  repository); `ansible-galaxy` tries them in the listed order (client behavior).
- The token is an access token of the system (TOK-001 in [../access-tokens.md](../access-tokens.md));
  `ansible-galaxy` sends it in an `Authorization` header (Galaxy client behavior; the exact scheme
  the system must accept is unspecified).
- Anonymous access works when it is enabled (AUTHN-036 in
  [../authentication-providers.md](../authentication-providers.md)).

### PKG-ANSIBLE-013 · Credentials for role installation
Priority: **P2**

Role installation reads credentials from `~/.netrc` (file mode 600):

```text
machine repo.example.com login <user> password <password-or-identity-token>
```

The secret is either the user's password or an identity token (TOK-005 in
[../access-tokens.md](../access-tokens.md)); the system must accept both as HTTP Basic password.
The client setup template also varies the keyword in front of the secret with the kind of secret
(a separate keyword for an identity token). The netrc format only defines `password` for the secret,
so a client's netrc parser is unlikely to read another keyword; client setup instructions should
always write `password`.

## Notes for implementers

- `ansible-galaxy` needs more than the three listed read endpoints: API-version discovery at
  the API root, collection upload with import-task polling, and artifact download URLs
  (PKG-ANSIBLE-002).
- Accept collection API paths with and without a trailing slash (PKG-ANSIBLE-002).
- Download URLs inside version metadata must be absolute and reachable with the same credentials,
  including through virtual repositories.
- Roles work only through remote repositories; publishing a role to a local repository must fail
  with a clear message pointing to collections (PKG-ANSIBLE-007).
- Automation Hub upstreams need a refresh-token exchange before every expiry of the short-lived
  access token; a failed exchange should surface as an upstream authentication error, not as
  missing collections (PKG-ANSIBLE-010).
- Remote repositories show only cached content in the browser (PKG-ANSIBLE-008); listing must not
  pretend to mirror the upstream.
