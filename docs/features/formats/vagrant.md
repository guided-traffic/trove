# Vagrant box format

This page catalogs what a repository manager does to host Vagrant boxes (packaged base images of
virtual machines) for the `vagrant` client: local repositories that store `.box` files with name,
provider and version properties, a box endpoint that `vagrant box add` resolves against, and
client authentication. The client and box concepts are HashiCorp's
([Vagrant documentation](https://developer.hashicorp.com/vagrant/docs)); where an entry relies on
Vagrant client behavior, it says so. Throughout this page,
`<repo-url>` stands for the URL under which a repository's files are served by path,
`<format-url>` for the repository's Vagrant endpoint URL, and `https://repo.example.com` for the
system's base URL; the concrete path layout is the implementation's choice. `vagrant-local` is an
example repository key.

## Repositories

### PKG-VAGRANT-001 · Local Vagrant repositories
Priority: **P2**

Vagrant boxes are hosted in local repositories only; remote and virtual Vagrant repositories are
not supported, and the repository configuration lists Vagrant among the local (and federated)
formats only.

- Boxes can be stored at any path and organized freely; access is controlled per path with the
  normal permission model (IAM-019, IAM-021 in
  [../identity-and-access.md](../identity-and-access.md)).
- Priority resolution (REPO-018 in [../repositories.md](../repositories.md)) does not apply to
  Vagrant repositories.
- Local repositories calculate metadata for boxes; what is calculated is unspecified beyond the
  box endpoint of PKG-VAGRANT-003.

Creating a repository requires the system administrator or a project administrator role. UI
location: administration, repositories, create repository, local, Vagrant format (UI-031 in
[../ui-inventory.md](../ui-inventory.md)).

## Boxes

### PKG-VAGRANT-002 · Deploy a box with name, provider and version
Priority: **P2**

A `.box` file is deployed with a plain HTTP `PUT` to any path in the repository. The request must
attach three properties as matrix parameters (ART-003 in [../artifacts.md](../artifacts.md)):

| Property | Meaning | Example |
|---|---|---|
| Box name | Name under which clients request the box. | `precise64` |
| Box provider | Virtualization provider the box is built for ([Vagrant providers](https://developer.hashicorp.com/vagrant/docs/providers)). | `virtualbox` |
| Box version | Box version ([box versioning](https://developer.hashicorp.com/vagrant/docs/boxes/versioning)). | `1.0.0` |

```text
PUT <repo-url>/precise64-virtualbox-1.0.0.box;<name-property>=precise64;<provider-property>=virtualbox;<version-property>=1.0.0
```

- The three properties are mandatory; the property keys are the implementation's choice. Further
  properties can be added the same way.
- What happens when one of the three is missing (rejection or a box that never resolves) is
  unspecified.
- Required permission: deploy on the target path (IAM-022 in
  [../identity-and-access.md](../identity-and-access.md)).
- UI: the deploy dialog (UI-013 in [../ui-inventory.md](../ui-inventory.md)); the properties are
  typed into the target path after the file name.

### PKG-VAGRANT-003 · Box endpoint for `vagrant box add`
Priority: **P2**

The repository's Vagrant endpoint resolves a box by name:

```sh
vagrant box add <format-url>/precise64
vagrant box add <format-url>/precise64 --provider virtualbox --box-version 1.0.0
```

- The box is found by its name property; `--provider` and `--box-version` select among boxes with
  the same name.
- Other properties can also narrow the selection (property-constrained resolution, ART-012 in
  [../artifacts.md](../artifacts.md)).
- The response format is unspecified. The Vagrant client expects, for a URL
  that is not a direct `.box` download, a JSON box catalog with the box name and a list of
  versions, each with its providers and their download URLs (Vagrant documentation); the catalog
  must be built from the properties of all boxes with that name.

## Client setup

### PKG-VAGRANT-004 · Client authentication and server URL
Priority: **P2**

The client setup instructions (UI-021, UI-026 in [../ui-inventory.md](../ui-inventory.md)) set two
environment variables that the Vagrant client reads:

```sh
export VAGRANT_SERVER_URL=<format-url>
export ATLAS_TOKEN=<token>
```

- `VAGRANT_SERVER_URL` makes short box names resolve against the repository; `ATLAS_TOKEN` carries
  an access token (TOK-001 in [../access-tokens.md](../access-tokens.md)).
- How the client transmits the token (query parameter or request header) is unspecified and
  depends on the Vagrant version; an implementation must verify it against the
  clients it supports.
- Anonymous access works when it is enabled (AUTHN-036 in
  [../authentication-providers.md](../authentication-providers.md)); then no token is needed.
- Deploys use HTTP Basic credentials with `curl` (PKG-VAGRANT-002).

## Notes for implementers

- Boxes are located by properties, not by path; a box deployed without the three mandatory
  properties is stored but cannot be resolved by name (PKG-VAGRANT-002).
- The catalog for one box name aggregates every stored file with that name across all paths of
  the repository, grouped by version and provider (PKG-VAGRANT-003).
- Download URLs inside the catalog must be reachable with the same credentials the client used for
  the catalog.
- Version ordering must follow Vagrant's version semantics so that `vagrant box add` without
  `--box-version` picks the newest box.
