# Repositories

Everything trove stores lives in a repository, and every repository belongs to a project and has a key, a format and a class. This page explains the local, remote and virtual classes from a user's point of view, how projects and keys become addresses, which formats this version serves, what you see depending on your permissions, and how the content page of a repository and its setup help you connect a client.

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

A virtual repository is one address for several npm repositories, several NuGet repositories, several OCI repositories or several Debian repositories, of its own project, in an order an administrator sets, for example an internal local repository followed by a remote one. Point your package manager at a virtual repository and you get internal and public packages through one address and one token. Keep in mind:

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
| Ansible Galaxy | `https://trove.example.com/api/ansible/<project>/<key>/`, the `url` of a server in `ansible.cfg` | none: collections are published with `ansible-galaxy collection publish` |

The page of a repository shows its address for your package manager under its title, with a copy button, and the others under **Details**; **Set up client** shows them in the configuration of your client, so you do not have to build them yourself. A repository that an administrator moves to another project changes its address: the old address answers `404`, and your client configuration needs the new one.

## Formats in this version

| Format | Classes | Clients the guides and setup cover | Where its content appears in the UI |
|---|---|---|---|
| npm | Local, remote, virtual | npm, pnpm, Yarn Berry (2 and later), Yarn Classic (1.x) | **Packages** and **Browse** |
| NuGet | Local, remote, virtual | .NET CLI (dotnet), NuGet CLI (nuget.exe), Visual Studio | **Packages** and **Browse** |
| OCI (Docker) | Local, remote, virtual | Docker CLI (with Buildx), Podman; Kubernetes through an image pull secret | **Packages** (an image name is a package, a tag a version) and **Browse** |
| Debian (APT) | Local, remote, virtual | APT (apt, apt-get) on Debian and Ubuntu | **Packages** (a binary package name is a package) and **Browse** |
| Ansible Galaxy | Local, remote, virtual | ansible-galaxy (ansible-core 2.17 and 2.21) | **Packages** (a collection is a package) and **Browse** |
| Generic | Local | curl | **Browse** only: generic files have no package names, so they are not listed under **Packages** |

Generic repositories store files of any kind at paths you choose: installers, archives, documents, build outputs. OCI repositories hold container images and other OCI artifacts; [Container images with Docker and Podman](container-images.md) explains them. Debian repositories are APT repositories with signed indexes; [Debian packages with APT](apt-packages.md) explains them. Other formats are not built yet; see [Known limitations](../admin/limitations.md).

## What you see depends on your permissions

trove shows you only what you may read. A repository you may not read is not listed anywhere, and its addresses answer `404`, exactly as if it did not exist (or `403`, where an administrator chose not to hide repositories).

| What you hold | What you see and can do |
|---|---|
| A role in the repository's project | **Reader** reads every repository of the project, **Developer** also publishes and uploads, **Maintainer** and **Owner** also replace, delete and manage; [Projects](../admin/projects.md#members-and-roles) explains the roles |
| Read on a repository | It appears in the **Browse** tree and, for npm, NuGet, OCI and Debian, under **Packages**; you can open its content page, download, and install from it |
| Deploy, overwrite or delete | Upload or publish, replace, and delete, on the paths and packages your permission names |
| The permission to read repository configuration | Every repository, the settings of every repository, the **Find a repository** search in the top bar, and **Admin area › Repositories** |
| Nothing, as an anonymous reader | Only the repositories open to anonymous readers, and nothing you can change |

Permissions can be narrower than a whole repository: a permission may cover only some paths or package names, and you then see only those. Ask an Owner of the repository's project if something you need is missing.

## The content of a repository

Open a project and you land on its **Repositories**. Select a repository to see what it holds, on the level you think in: images and tags, packages and versions, or files. You get there from the project's **Repositories**, from **Admin area › Repositories** (every project, each row naming its project), and with **Find a repository** in the top bar if you may read repository configuration.

The page is titled with the repository's key and says what the repository is. Under the title is its **Client address**, the address your package manager needs, with **Copy client address**. At the top right:

- **Set up client** opens the client setup for this repository, with its format and the repository already chosen: you pick your client and how to handle the token. A disabled repository has none, because clients cannot use it.
- **Settings** opens the repository's settings, if you may read them.
- **More actions** holds **Browse files**, which opens the same repository in the file tree, and **Vulnerabilities**.

Below them the page lists the **Images**, **Packages** or **Files** of the repository, depending on the format, with a filter. **Details**, at the end and closed until you open it, holds the repository's other addresses, each named (**Address for downloads by path (curl, wget)**), the upstream a remote repository caches, and the members a virtual repository resolves from.

Depending on the format, the content shows:

| Format | What is listed | Open a row to see |
|---|---|---|
| **OCI (Docker)** | Images, each with its latest tag, the number of tags, the [grade](vulnerabilities.md#grades) of its latest tag, and when it was last pushed. A remote repository lists the images it has cached: the tags clients have pulled through it and, in mirror mode, the tags a pass has fetched | The tags with digest, size, pushed time (fetched time in a remote repository, the time its cached copy of the tag was stored) and grade, and a copyable `docker pull` or `podman pull` command for the tag you choose, with this installation's address |
| **npm, NuGet, Debian** | Packages, each with its latest version, the number of versions, the [grade](vulnerabilities.md#grades) of its latest version where the format is scanned (npm and NuGet today) and downloads | The versions with size, published time and grade, and the install command for the version you choose |
| **Generic** | The files of the repository, folder by folder | Selecting a folder opens it in place; selecting a file opens it in the file browser with all its details; the copy button copies its download address |

Select a package or image name to open the [package page](packages.md) for details, the README and all versions. A virtual repository lists what it resolves through its members, which are named above the list. A remote repository lists what has been fetched through it, marked in a note.

The **Vulnerabilities** column shows a shield with the grade and the count that decides it, for example **E · 13 Critical**; point at it to
see every count. Select its heading to sort by grade: once ascending, from N/A and S to E, and again for the worst first. Sorting by
grade loads up to 1,000 entries first; in a larger repository the list says that it sorted only those. The column is not shown when
you are signed out, or when the installation does not scan.

Debian packages are not scanned for vulnerabilities yet; the page says so and shows no vulnerability column for them.

## Repository settings

**Settings** on the content page opens the settings of the repository at `…/settings`. Editing it needs a further permission and is described in [Managing repositories](../admin/repositories.md). If you may use the repository but not read its configuration, the page says **You may use this repository but not read or change its configuration.** and shows the facts and the **How to use** guide instead of the form.

The settings page has the tabs **General**, **Access**, the format's settings (for example **npm settings**), **How to use** and **API**.

## The How to use guide

**How to use** is a guide written for the repository you are looking at: it uses this installation's addresses and the repository's key, and it describes what the repository's class allows. It is a tab on the settings page and a section on the consumer view.

1. Under **Client**, choose your package manager or curl. The guide changes to that client.
2. Read the sections in order, or jump to one from **On this page**.
3. Copy a snippet with **Copy snippet**. A snippet meant for a file names it, for example **File**: `.npmrc`.
4. **Open the client setup** prepares the client setup page for the same repository and client, where you can create a token and copy ready-made configuration. **Open in the guides** opens the same guide in the **Guides** area.

The sections cover what the repository is, pointing the client at it, signing the client in, installing, publishing and maintaining (where the class allows writes), and troubleshooting; the npm guide adds security audits and lockfiles. A guide never contains a token: snippets read it from an environment variable such as `TROVE_TOKEN`, and the guide for an anonymous reader uses no credential at all.

**Guides** in the start side bar and in the user menu holds the same guides without opening a repository first: choose a **Package type**, a **Client**, and either a **Repository** or **None in particular: the guide of a repository class** together with a **Repository class**. To configure a client step by step, see [Connecting a package client](client-setup.md).
