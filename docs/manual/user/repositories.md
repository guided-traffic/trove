# Repositories

Everything trove stores lives in a repository, and every repository belongs to a project and has a key, a format and a class. This page explains the local, remote and virtual classes from a user's point of view, how projects and keys become addresses, which formats this version serves, what you see depending on your permissions, and how the repository screen and its How to use guide help you connect a client.

## Repository classes

A repository is local, remote or virtual. An administrator chooses the class when creating the repository; it never changes.

| Class | What it holds | What you do with it |
|---|---|---|
| **Local** | The packages and files your teams publish or upload | Read, and publish or upload where you hold the permission |
| **Remote** | A cache of an upstream registry, filled on demand | Read only; trove fetches from the upstream for you |
| **Virtual** | Nothing of its own: it combines several repositories, its members, under one address | Read through one address; publishing goes to one local member |

### Local repositories

A local repository keeps what is published to it, and a publish is visible as soon as it has returned. For npm, a published version never changes: publishing the same version again is refused, and an unpublished version number stays blocked for good. NuGet is the same: a version never changes, a deleted version number stays blocked for good, and pushing the identical package again is accepted and changes nothing, so a push that a client repeats after a lost answer succeeds. A Debian package file is stored once per package, version and architecture: other bytes under the same identity are refused, and the same bytes again only add suites and components; the signed index that APT reads follows a few seconds after the upload. For generic files, uploading other bytes to an existing path replaces the file, but only for people who hold the overwrite permission.

### Remote repositories

A remote repository is a pull-through cache of an upstream registry; for npm the default upstream is `https://registry.npmjs.org/`. The first request for a package fetches it from the upstream and stores it; later requests are answered from trove. While the upstream is down, trove serves what it has cached. A version that the upstream withdraws is no longer served, even from the cache, unless an administrator chose otherwise. You cannot publish to a remote repository.

For NuGet the default upstream is `https://api.nuget.org/v3/index.json`: trove reads the service index of the feed and takes the package files and documents from the addresses it names. A NuGet remote checks every package it caches: the ID and version the package declares must be the ones the request named, and an archive whose ID and version cannot be read is refused the same way: the answer is `502` `upstream_package_mismatch` and nothing is stored. A version that disappears from the upstream's lists stays downloadable from the cache by its exact address; an ID the upstream no longer has is not served at all, though its cached packages stay stored and the cache view (`<key>~cache`) lists them.

For Debian (APT) the default upstream is `https://deb.debian.org/debian/`. A Debian remote passes the archive's signed release files through unchanged, so your APT client verifies the archive's own signature; [Debian packages with APT](apt-packages.md#through-a-remote-repository) says which key file to use.

### Virtual repositories

A virtual repository is one address for several npm repositories, several NuGet repositories or several OCI repositories, of its own project, in an order an administrator sets, for example an internal local repository followed by a remote one. Point your package manager at a virtual repository and you get internal and public packages through one address and one token. Keep in mind:

- You need the read permission on the members too. A member you may not read is left out without notice.
- Publishing through a virtual repository goes to its deployment target, a local member. A virtual repository without one refuses every publish.
- For NuGet, the version list and the package documents of an ID are the union of the versions of its members. A version that two members hold comes from the first one in the order, and so does the package you download; a search lists a package once, with the versions of all members.
- A name that a local member has claimed is served only from that member, so a public package with the same name and a higher version never replaces an internal one.

## Projects and repository keys

Every repository belongs to exactly one project, for example the project `web` of a team. The project's key and the repository's key together name the repository: `web/npm-internal`. The key is unique within its project only, so the projects `web` and `mobile` can each have a repository `npm-remote`, and the two are different repositories.

Keys are set when the repository is created and cannot be renamed:

- 2 to 64 characters: lower-case letters and digits, separated by one `.`, one `_`, two `_` or any number of `-`
- it starts and ends with a letter or a digit, so `docker-local` and `docker.hub` are accepted and `docker-` and `a.-b` are not

Your client reaches a repository at an address built from the installation's address, the project key and the repository key:

| Format | Address for your package manager | Address for raw files |
|---|---|---|
| npm | `https://trove.example.com/api/npm/<project>/<key>/` | `https://trove.example.com/files/<project>/<key>/` |
| NuGet | `https://trove.example.com/api/nuget/<project>/<key>/v3/index.json` | `https://trove.example.com/files/<project>/<key>/` |
| Generic | `https://trove.example.com/files/<project>/<key>/` | the same |
| OCI (Docker) | `trove.example.com/<project>/<key>/<image>:<tag>`, an image reference without a scheme | `https://trove.example.com/files/<project>/<key>/` (download and delete; uploads go through the registry) |
| Debian (APT) | `https://trove.example.com/api/deb/<project>/<key>/`, with a suite in the source line | `https://trove.example.com/files/<project>/<key>/` (download and delete; uploads go to `upload/<file>` of the APT address) |

The repository screen shows these addresses for your installation, so you do not have to build them yourself. A repository that an administrator moves to another project changes its address: the old address answers `404`, and your client configuration needs the new one.

## Formats in this version

| Format | Classes | Clients the guides and setup cover | Where its content appears in the UI |
|---|---|---|---|
| npm | Local, remote, virtual | npm, pnpm, Yarn Berry (2 and later), Yarn Classic (1.x) | **Packages** and **Browse** |
| NuGet | Local, remote, virtual | .NET CLI (dotnet), NuGet CLI (nuget.exe), Visual Studio | **Packages** and **Browse** |
| OCI (Docker) | Local, remote, virtual | Docker CLI (with Buildx), Podman; Kubernetes through an image pull secret | **Packages** (an image name is a package, a tag a version) and **Browse** |
| Debian (APT) | Local, remote | APT (apt, apt-get) on Debian and Ubuntu | **Packages** (a binary package name is a package) and **Browse** |
| Generic | Local | curl | **Browse** only: generic files have no package names, so they are not listed under **Packages** |

Generic repositories store files of any kind at paths you choose: installers, archives, documents, build outputs. OCI repositories hold container images and other OCI artifacts; [Container images with Docker and Podman](container-images.md) explains them. Debian repositories are APT repositories with signed indexes; [Debian packages with APT](apt-packages.md) explains them. Other formats are not built yet; see [Known limitations](../admin/limitations.md).

## What you see depends on your permissions

trove shows you only what you may read. A repository you may not read is not listed anywhere, and its addresses answer `404`, exactly as if it did not exist (or `403`, where an administrator chose not to hide repositories).

| What you hold | What you see and can do |
|---|---|
| A role in the repository's project | **Reader** reads every repository of the project, **Developer** also publishes and uploads, **Maintainer** and **Owner** also replace, delete and manage; [Projects](../admin/projects.md#members-and-roles) explains the roles |
| Read on a repository | It appears in the **Browse** tree and, for npm, NuGet, OCI and Debian, under **Packages**; you can open its repository screen, download, and install from it |
| Deploy, overwrite or delete | Upload or publish, replace, and delete, on the paths and packages your permission names |
| The permission to read repository configuration | Every repository, the full repository screen with all settings, the **Find a repository** search in the top bar, and **Admin area › Repositories** |
| Nothing, as an anonymous reader | Only the repositories open to anonymous readers, and nothing you can change |

Permissions can be narrower than a whole repository: a permission may cover only some paths or package names, and you then see only those. Ask an Owner of the repository's project if something you need is missing.

## The repository screen

Open a repository in one of these ways:

1. In **Browse**, select the repository at the top of the tree, then select **Repository configuration** in the details.
2. Select it in the project's **Repositories**.
3. If you may read repository configuration, select it in **Admin area › Repositories** (every project, each row naming its project) or search for it with **Find a repository**.

The screen is titled **Repository** and shows the key. **Browse** opens the repository in the file browser.

### The consumer view

If you may use the repository but not read its configuration, the screen says **You may use this repository but not read or change its configuration.** It shows:

| Field | Meaning |
|---|---|
| **Class** | Local, remote or virtual |
| **Format** | npm, NuGet, OCI (Docker) or generic |
| **Description** | What the repository is for, as its administrator wrote it |
| **Client address** | Every address of the repository on this installation: for npm the address for your package manager and the raw file address |

Below the facts follows the **How to use** guide.

### The full view

If you may read repository configuration, the screen also shows the layout, the client address with a copy button, and when the repository was created, last changed and last used. The tabs **General**, **Access**, the format's settings (for example **npm settings**), **How to use** and **API** hold the configuration. Changing it needs a further permission and is described in [Managing repositories](../admin/repositories.md).

## The How to use guide

**How to use** is a guide written for the repository you are looking at: it uses this installation's addresses and the repository's key, and it describes what the repository's class allows. It is a tab on the full view and a section on the consumer view.

1. Under **Client**, choose your package manager or curl. The guide changes to that client.
2. Read the sections in order, or jump to one from **On this page**.
3. Copy a snippet with **Copy snippet**. A snippet meant for a file names it, for example **File**: `.npmrc`.
4. **Open the client setup** prepares the client setup page for the same repository and client, where you can create a token and copy ready-made configuration. **Open in the guides** opens the same guide in the **Guides** area.

The sections cover what the repository is, pointing the client at it, signing the client in, installing, publishing and maintaining (where the class allows writes), and troubleshooting; the npm guide adds security audits and lockfiles. A guide never contains a token: snippets read it from an environment variable such as `TROVE_TOKEN`, and the guide for an anonymous reader uses no credential at all.

**Guides** in the start side bar and in the user menu holds the same guides without opening a repository first: choose a **Package type**, a **Client**, and either a **Repository** or **None in particular: the guide of a repository class** together with a **Repository class**. To configure a client step by step, see [Connecting a package client](client-setup.md).
