# Browsing and transferring files

The file browser shows the repositories you may read as a tree of folders and files, with the details of whatever you select, and lets you download, upload, create folders and delete as your permissions allow. Every file is also reachable over plain HTTP below the files path, which this page shows with curl.

## The file browser

Select **Browse** in the side bar. It is also the start page of trove. The screen has two panes side by side: the tree of repositories and files on the left, and the details of what you select on the right. On a narrow screen one pane shows at a time; **Back to the tree** returns from the details.

### The tree

Above the tree, the field **Filter repositories** keeps the repositories whose key or description contains what you type; the list narrows while you type, there is no search button. **Filters** opens the filter panel inside the tree pane with a **Class** and a **Format** to choose; select **Apply filters** to use them. Each applied filter is shown as a chip below the toolbar; the chip's cross removes it, and **Clear all** in the panel removes both. The sort button orders the repositories by **Key**, or **Class, then key** (virtual, then local, then remote). **Repositories** at the right of the toolbar counts the repositories shown; a plus sign means more can be loaded.

Each repository row has an icon for its class (a box for local, a cloud for remote, layers for virtual), the key and a badge with its format. A remote repository carries **cache**, because it shows the files it has cached so far; a repository an administrator switched off carries **disabled**. A virtual repository does not hold files of its own: expand it to see the repositories it **resolves from**, in the order it asks them, and select one to open it. Only people who may read the repository's configuration see the members.

Expand a repository or a folder to load its entries, 200 at a time; **Load more** at the end of a folder loads the next 200, and **Load more repositories** below the tree loads further repositories. Repositories you may not read are not listed, and neither are files and folders you may not read. If there is no repository you may read, the tree says so; an administrator is offered **Create repository**, everyone else should ask an administrator for read access.

What a repository holds depends on its class: a local repository shows what was uploaded or published to it, a remote repository the files it has cached so far, and npm repositories show their package files as trove stores them. See [Repositories](repositories.md#repository-classes).

The selection is part of the page address, for example `https://trove.example.com/ui/en/browse/generic-local/releases/app-1.2.0.tar.gz`, so you can bookmark a file or send the link; opening it expands the tree down to the file. The browser's back and forward buttons walk through your selections.

The tree works with the keyboard: the arrow keys move and expand, Home and End jump to the first and last entry, and typing a name jumps to it. Shift+F10 or the context-menu key moves the focus to the actions of the selection.

### The details

The top of the details shows the path of the selection as links (select a folder to go up) and the actions that apply to it and that you may use: **Download**, copy buttons for the address and the path, **Client setup**, **Upload files**, **New folder** and **Delete**. Below that:

| Selection | Details |
|---|---|
| A repository | Tab **General**: **Class**, **Format**, **Description** and, for people who may read its configuration, the upstream address of a remote repository, **Project**, **Created** and **Last activity**, with links **Repository configuration** and **Client setup**; then its content as a table, or for a virtual repository the list **Resolves from**. Tab **API** |
| A folder | Tab **Contents**: a table of its entries with **Name**, **Size** and **Last changed**; select a name to go into a folder or to a file. Tab **General** with **Path**, **Kind**, **Created** and **Last changed**, each with the user who did it where known; tab **API** |
| A file | Tab **General** as for a folder plus **Size**, **Media type** and the checksums **SHA-256**, **SHA-512**, **SHA-1** and **MD5**, each with **Copy checksum**; tab **Statistics** with **Downloads**, **Last download** and **Last downloader**; tab **API** |

Download counts reach the statistics within a minute of a download. If a file's stored content is missing or damaged, **General** says so under **Content**; tell an administrator. The **API** tab shows the selection as the management API describes it and the matching curl requests. They send the token from the environment variable `TROVE_TOKEN`: the header is in double quotes so that your shell fills in the value, and the address is in single quotes. Set the variable first, without typing the token into a shell that records its history.

## Download a file

1. Select the file in the tree.
2. Select **Download**. The browser saves the file under its name.

The link button beside **Download** copies the file's address (**Copy download URL**) below the files path, which curl and other tools can use (see [Transfer files with curl](#transfer-files-with-curl)). **Copy path** copies the path inside the repository.

## Upload files

You need the deploy permission on the target path, and the target must be a generic local repository. npm packages are published with the package manager, not uploaded here: an npm repository refuses an upload from this dialog, and remote and virtual repositories take no uploads at all.

1. Select the repository or the folder to upload into. **Upload files** appears only in a local repository of a generic format, and only where you hold the deploy permission.
2. Select **Upload files**.
3. Check **Target folder**. It is relative to the root of the repository and prefilled with your selection; missing folders are created, and an empty field is the root.
4. Drop files onto the dialog, or select **Choose files**.
5. To replace files that already exist at the target, tick **Replace files that already exist**. The option appears only if you hold the overwrite permission there.
6. Select **Upload**.

The files are sent one after another. **Status** shows **waiting**, **computing checksum**, a progress bar, **uploaded**, **cancelled**, or the reason a file failed. **Cancel upload** stops the file that is being sent; **Close** closes the dialog, and the folder in the tree shows the new files.

- The browser uploads files of up to 100 MiB. For a larger file the dialog says to use a native client or curl, which take files up to the installation's limit (10 GiB by default).
- The browser computes the SHA-256 of each file before sending it, and trove checks it on arrival, so a damaged transfer is not stored.
- Without the replace option, a file that exists already is kept, and the dialog says **A file already exists at this path.** Uploading the same bytes again with the replace option changes nothing.
- The repository's include and exclude patterns can refuse a path; ask whoever manages the repository.

## Create a folder

1. Select the repository or the folder to create the new folder in.
2. Select **New folder**.
3. Enter the **Folder name**. A name with slashes, such as `2026/q4`, creates the folders in between.
4. Select **Create folder**.

Folders also arise on their own when you upload a file to a path that contains them.

## Delete files and folders

You need the delete permission on the file, or on every file in a folder; if one is missing, nothing is deleted.

1. Select the file or folder. The root of a repository cannot be deleted here.
2. Select **Delete**.
3. Read the confirmation: it says how many items a folder holds. For a folder of more than 100 items, type the folder name into **Type the name to confirm**.
4. Select **Delete**.

Deleting is final: trove has no trash can yet. A very large folder (by default more than 10,000 items) is deleted by a background job; the details show **Deleting** with its progress, and the files disappear as the job runs.

## Transfer files with curl

Every file of a repository has an address below the files path:

```text
https://trove.example.com/files/<repository-key>/<path>
```

| Method | On a file | On a folder (address ends with `/`) |
|---|---|---|
| `GET` | Downloads the file | Lists the entries you may read, as JSON, or as a page with links in a browser |
| `HEAD` | Returns size, checksums and dates without the content | The same for the listing |
| `PUT` | Uploads the file (generic local repositories) | Creates an empty folder, without a body |
| `DELETE` | Deletes the file | Deletes the folder with everything below it |

### Authenticate

curl signs in with a trove token, never with your password: a personal access token for yourself, or a service-account token for a CI job. Create one under **Profile and tokens**, limited to the repositories and actions it needs; see [Personal access tokens](access-tokens.md). trove reads the token from the `Authorization: Bearer` header. It also takes the token as the password of HTTP Basic authentication with any user name, unless an administrator switched that off.

Keep the token out of command lines and scripts, where the process list and the shell history would show it. The client setup snippet for curl defines a shell function that reads the token from the environment variable `TROVE_TOKEN` each time it runs and hands it to curl on standard input:

```shell
trove_curl() {
  curl --silent --show-error --config - "$@" <<EOF
header = "Authorization: Bearer ${TROVE_TOKEN}"
EOF
}
```

Set `TROVE_TOKEN` in the environment curl runs in, for example from a masked CI variable, then paste the function into the shell or the CI script. Because standard input carries the header, `trove_curl` cannot upload from standard input; upload a file instead. The variable name can differ on your installation; [Connecting a package client](client-setup.md) shows the snippet as your installation writes it. For a repository open to anonymous readers, plain `curl` without a token downloads as well.

### Download and check

The examples keep the address of one file in a shell variable:

```shell
FILE="https://trove.example.com/files/generic-local/releases/app-1.2.0.tar.gz"
trove_curl --fail --remote-name "$FILE"
trove_curl --fail --head "$FILE"
trove_curl --fail --continue-at - --output app-1.2.0.tar.gz "$FILE"
```

The second line downloads the file into the current directory under its name. The third shows its size, its `ETag` (the SHA-256 in hexadecimal) and its `Repr-Digest` without downloading it. The last one resumes a broken download where the local copy ends.

`--fail` makes curl exit with an error instead of saving an error answer as the file. trove checks a file against its checksum while it serves it whole; a resumed copy joins two transfers, so compare it with the `ETag` (`sha256sum` prints the same hexadecimal value). Add `?stats=false` to the address when a download should not count in the statistics.

### Upload

```shell
trove_curl --fail --upload-file app-1.2.0.tar.gz "$FILE"
trove_curl --fail --upload-file app-1.2.0.tar.gz --header "If-None-Match: *" "$FILE"
trove_curl --fail --upload-file app-1.2.0.tar.gz \
  --header "Repr-Digest: sha-256=:$(openssl dgst -sha256 -binary app-1.2.0.tar.gz | base64):" \
  --header "Content-Type: application/gzip" \
  "$FILE"
```

The first command uploads the file; the folders on the way are created. The second uploads only if the path is free and never replaces a file. The third has trove check the SHA-256 you computed, refusing the upload with `409` if the bytes differ, and stores a media type.

trove answers `201` for a new or replaced file and `200` when the path already holds these bytes. Replacing a file with other bytes needs the overwrite permission besides deploy. Without `Content-Type`, the file is served as `application/octet-stream`.

### List and delete

```shell
trove_curl --fail "https://trove.example.com/files/generic-local/releases/?limit=100"
trove_curl --fail --request DELETE "$FILE"
```

The first command lists a folder as JSON: `items` holds the entries you may read, and `nextCursor` leads to the next page; pass it as `cursor`. A page holds 1,000 entries unless `limit` asks for another number (at most 10,000). Without the trailing `/`, a folder address redirects to the one with it. The second command deletes a file and answers `204`; a folder of more than 10,000 items answers `202` and is deleted by a background job.

### Common answers

| Status | Code | Meaning |
|---|---|---|
| 401 | `invalid_token` | The token is unknown, expired or revoked, or the variable is empty |
| 403 | `forbidden` | The token lacks the permission: deploy, overwrite or delete |
| 403 | `not_admitted` | The repository's include and exclude patterns refuse this path |
| 404 | `not_found` | The file does not exist, or you may not read it |
| 405 | `method_not_allowed` | An upload to a remote or virtual repository |
| 409 | `conflict` | An upload to an npm repository, or bytes that differ from the `Repr-Digest` sent |
| 409 | `path_conflict` | A file where a folder is, or the other way round |
| 412 | `precondition_failed` | `If-None-Match: *` and the path exists |
| 413 | `payload_too_large` | The file is larger than the limit of the installation or the repository |

The full list, with what to do for each code, is in [Errors and troubleshooting](troubleshooting.md).

### On an endpoint for customers or partners

An installation can serve repositories on further addresses with exposure rules, for example for customers. There, a file outside the exposed paths answers `404` like a missing one, a listing leaves it out, and uploads and deletes answer `403` with `endpoint_read_only`. Use the token issued for that address, and upload through the main address of the installation.
