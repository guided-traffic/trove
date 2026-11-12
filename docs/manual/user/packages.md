# Packages

The Packages screen lists the packages of all repositories you may read, with filters, sorting and saved views, and opens each package with its versions, metadata, distribution tags, files, README and a ready-made install command. This page explains each part.

## What the list shows

Select **Packages** in the side bar of a project. The list holds the npm packages of every repository of the project that you may read; generic repositories have no package names, so their files are in **Browse** instead (see [Browsing and transferring files](files.md)).

A package appears once, however many repositories hold it. Everything in its row is counted from the versions you may see: a version you may not read changes neither the latest version nor the counts. A remote repository contributes only the versions it has already cached.

| Column | Meaning |
|---|---|
| **Package** | The package name, which opens the package, and under it the description of the latest version |
| **Format** | The package format |
| **Latest version** | The highest stable version you may see. A pre-release is shown only while no stable version exists |
| **Updated** | When the latest version you may see was published, as a relative time; hover for the exact time |

When a version was published, its licences, its downloads and the repositories that hold it are in **Details** at the end of the
package's page.

The **?** next to some column headings explains the column. The list shows one page at a time; **Load more** adds the next page. When nothing is published yet, the list says **No package has been published yet.** and offers **Set up a client to publish the first package**. **Set up a client** at the top of the screen opens the client setup at any time.

## Filter the list

1. Select **Filters**. The filter panel opens next to the list, or above it on a narrow screen. The button shows how many filters are applied.
2. Open a category and set its value:

   | Category | What it keeps |
   |---|---|
   | **Package type** | Packages of the ticked formats |
   | **Repository** | Packages with a version in one of the ticked repositories; a virtual repository stands for its members |
   | **Name or description** | Packages whose name or description contains the text, ignoring case |
   | **Keyword** | Packages with a version that lists exactly this keyword |
   | **Created** | Packages whose first version falls into **Last 7 days**, **Last 30 days**, **Last 90 days** or a **Custom period** from **From** to **To (included)** |

3. Select **Search** to apply the filters.

Each applied filter appears as a chip above the list; the cross on a chip removes that filter. **Clear all** in the panel removes every filter, and so does **Clear all filters** when no package matches. **Close filters** or Escape closes the panel.

## Sort the list

Select the heading **Package** or **Updated** to sort by it; select it again to reverse the order. The list is sorted by name unless you choose otherwise. The other columns cannot be sorted.

Filters, sort and the open tab are part of the page address, so you can bookmark a filtered list or send it to a colleague. The colleague sees the packages that they may read.

## Recently viewed and saved views

When you are signed in, two more tabs appear next to **All packages**:

- **Recently viewed** lists the last 10 packages you opened.
- **Saved views** keeps up to 8 combinations of filters and sort. To save one, set the filters and the sort, open **Saved views**, enter a **Name of the view** and select **Save the current filters and sort**. Each saved view can be opened, renamed with **Rename**, removed with **Delete**, and made the default with **Make default**: the list then opens with that view whenever you open **Packages** without filters. **Remove default** undoes that.

Both are kept in this browser only, separately for each user. They are not available where the browser blocks site storage, and they do not follow you to another browser.

## Package details

Select a package name to open its page. The page shows the name, the format and **All packages** to return to the list. The address of a package is `/projects/<project>/packages/<format>/<name>`, for example `https://trove.example.com/ui/en/projects/web/packages/npm/@team-a/ui`.

### Choose a version

The page opens with the latest stable version you may see, or the latest pre-release when there is no stable version.

1. In **Version**, type a version or pick one from the suggestions.
2. Tick **Include pre-releases** to have pre-releases suggested as well.

The chosen version is part of the page address, so a link can point at one version. The facts below the picker belong to that version:

| Fact | Meaning |
|---|---|
| **Published** | When this version was published |
| **Licenses** | The licenses the version declares |
| **Downloads** | Downloads of this version |
| **Repositories** | The repositories that hold this version and that you may read, including the virtual repositories that contain them |

A version that its publisher marked as deprecated shows **Deprecated.** with the publisher's message.

### Tabs

| Tab | What it shows |
|---|---|
| **README** | The README of this version. Shown only when the version has one |
| **Metadata** | **Links** (**Homepage**, **Source repository**, **Issue tracker**), **Authors**, **Distribution tags**, **Dependencies**, **Development dependencies**, **Peer dependencies**, **Optional dependencies** and **Required runtimes**, as far as the version declares them |
| **Files** | The files of this version with **Size**, **SHA-256** and a **Download** link. **Copy SHA-256** copies the checksum; compare it with the output of `sha256sum` on the downloaded file |
| **Repositories** | Each repository that holds the version, its **Class** and when it was **Published** there. **Show in browser** opens the repository in **Browse** |
| **All versions** | Every version you may see, newest first, with **Published**, **Size**, **Repositories** and **Downloads**; pre-releases carry a **pre-release** badge. Select a version to switch to it |

Links in **Metadata** open in a new tab and only for `http` and `https` addresses; any other link is shown as text.

## Distribution tags

npm packages carry distribution tags, names such as `latest` or `next` that point at a version. **Metadata**, section **Distribution tags**, lists each tag with its version, and only tags that point at a version you may see.

You change tags with your package manager, not in the UI, for example `npm dist-tag add @team-a/ui@1.4.0 next`. That needs the deploy permission and, where the package has owners, ownership of it. The **How to use** guide of a repository has the commands for each client; see [Repositories](repositories.md#the-how-to-use-guide). The content page of a repository also lists its packages with their install commands; see [Repositories](repositories.md#the-content-of-a-repository).

## The install panel

The panel **Install** sits beside the tabs on a wide screen and above them on a narrow one. It shows the command that installs the chosen version.

1. Under **Client**, choose npm, pnpm, Yarn Berry (2 and later) or Yarn Classic (1.x).
2. Under **Repository**, choose the repository to install from. The picker appears only when several repositories hold the version; trove preselects a virtual repository, otherwise a local one.
3. Select **Copy install command**. A command longer than three lines is collapsed; **Show the whole command** expands it and **Show less** collapses it again.

The command assumes that your package manager is already configured for the repository; it never contains a token. **Set up your client for this repository** opens the client setup for the chosen repository and client, where you configure that once; see [Connecting a package client](client-setup.md).

## The README

trove renders the README on the server from the package's own Markdown, with tables, task lists and code blocks, and removes anything that could run in your browser. Links to other sites carry no referrer, and the links of a README's table of contents jump within the README.

- A README larger than the display limit is shortened, and the tab says **This README is shortened because it is larger than the display limit.**
- Images from other sites are shown only from sites your administrators allowed; every other external image is replaced by a link, and the tab says how many.
