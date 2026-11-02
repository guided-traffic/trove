# Repositories

Everything trove stores lives in a repository, and every repository has a key, a format and a class. This page explains the local, remote and virtual classes from a user's point of view, how keys become addresses, which formats this version serves, what you see depending on your permissions, and how the repository screen and its How to use guide help you connect a client.

## Repository classes

A repository is local, remote or virtual. An administrator chooses the class when creating the repository; it never changes.

| Class | What it holds | What you do with it |
|---|---|---|
| **Local** | The packages and files your teams publish or upload | Read, and publish or upload where you hold the permission |
| **Remote** | A cache of an upstream registry, filled on demand | Read only; trove fetches from the upstream for you |
| **Virtual** | Nothing of its own: it combines several repositories, its members, under one address | Read through one address; publishing goes to one local member |

### Local repositories

A local repository keeps what is published to it, and a publish is visible as soon as it has returned. For npm, a published version never changes: publishing the same version again is refused, and an unpublished version number stays blocked for good. NuGet is the same: a version never changes, a deleted version number stays blocked for good, and pushing the identical package again is accepted and changes nothing, so a push that a client repeats after a lost answer succeeds. For generic files, uploading other bytes to an existing path replaces the file, but only for people who hold the overwrite permission.

### Remote repositories

A remote repository is a pull-through cache of an upstream registry; for npm the default upstream is `https://registry.npmjs.org/`. The first request for a package fetches it from the upstream and stores it; later requests are answered from trove. While the upstream is down, trove serves what it has cached. A version that the upstream withdraws is no longer served, even from the cache, unless an administrator chose otherwise. You cannot publish to a remote repository.

For NuGet the default upstream is `https://api.nuget.org/v3/index.json`: trove reads the service index of the feed and takes the package files and documents from the addresses it names. A NuGet remote checks every package it caches: the ID and version the package declares must be the ones the request named, otherwise the answer is `502` `upstream_package_mismatch` and nothing is stored. A version that disappears from the upstream's lists stays downloadable from the cache by its exact address; an ID the upstream no longer has is not served at all, though its cached packages stay stored and the cache view (`<key>~cache`) lists them.

### Virtual repositories

A virtual repository is one address for several npm repositories in an order an administrator sets, for example an internal local repository followed by a remote one. Point your package manager at a virtual repository and you get internal and public packages through one address and one token. Keep in mind:

- You need the read permission on the members too. A member you may not read is left out without notice.
- Publishing through a virtual repository goes to its deployment target, a local member. A virtual repository without one refuses every publish.
- A name that a local member has claimed is served only from that member, so a public package with the same name and a higher version never replaces an internal one.

## Repository keys

The key names a repository in every address and every screen. Keys are set by an administrator when the repository is created and cannot be renamed.

- 2 to 64 characters: lower-case letters, digits, `.`, `_` and `-`
- it starts with a letter or a digit, does not end with `.` and does not contain `..`

Your client reaches a repository at an address built from the installation's address and the key:

| Format | Address for your package manager | Address for raw files |
|---|---|---|
| npm | `https://trove.example.com/api/npm/<key>/` | `https://trove.example.com/files/<key>/` |
| Generic | `https://trove.example.com/files/<key>/` | the same |

The repository screen shows these addresses for your installation, so you do not have to build them yourself.

## Formats in this version

| Format | Classes | Clients the guides and setup cover | Where its content appears in the UI |
|---|---|---|---|
| npm | Local, remote, virtual | npm, pnpm, Yarn Berry (2 and later), Yarn Classic (1.x) | **Packages** and **Browse** |
| NuGet | Local, remote | .NET CLI (dotnet), NuGet CLI (nuget.exe), Visual Studio | **Packages** and **Browse** |
| Generic | Local | curl | **Browse** only: generic files have no package names, so they are not listed under **Packages** |

Generic repositories store files of any kind at paths you choose: installers, archives, documents, build outputs. Other formats are not built yet; see [Known limitations](../admin/limitations.md).

## What you see depends on your permissions

trove shows you only what you may read. A repository you may not read is not listed anywhere, and its addresses answer `404`, exactly as if it did not exist (or `403`, where an administrator chose not to hide repositories).

| What you hold | What you see and can do |
|---|---|
| Read on a repository | It appears in the **Browse** tree and, for npm, under **Packages**; you can open its repository screen, download, and install from it |
| Deploy, overwrite or delete | Upload or publish, replace, and delete, on the paths and packages your permission names |
| The permission to read repository configuration | Every repository, the full repository screen with all settings, the **Find a repository** search in the top bar, and **Administration › Repositories** |
| Nothing, as an anonymous reader | Only the repositories open to anonymous readers, and nothing you can change |

Permissions can be narrower than a whole repository: a permission may cover only some paths or package names, and you then see only those. Ask whoever manages a repository if something you need is missing.

## The repository screen

Open a repository in one of these ways:

1. In **Browse**, select the repository at the top of the tree, then select **Repository configuration** in the details.
2. If you may read repository configuration, select it in **Administration › Repositories** or search for it with **Find a repository**.

The screen is titled **Repository** and shows the key. **Browse** opens the repository in the file browser.

### The consumer view

If you may use the repository but not read its configuration, the screen says **You may use this repository but not read or change its configuration.** It shows:

| Field | Meaning |
|---|---|
| **Class** | Local, remote or virtual |
| **Format** | npm or generic |
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

The **Guides** area in the side bar holds the same guides without opening a repository first: choose a **Package type**, a **Client**, and either a **Repository** or **None in particular: the guide of a repository class** together with a **Repository class**. To configure a client step by step, see [Connecting a package client](client-setup.md).
