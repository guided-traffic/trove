# Ansible Galaxy repositories

What an administrator decides for repositories of the format Ansible Galaxy: the three classes, the settings, the limits, the import tasks and tombstones, and what is not built yet. The side of the clients is in the user guide page on ansible-galaxy.

## Classes

| Class | What it does | Settings that apply |
|---|---|---|
| Local | stores the collection archives that `ansible-galaxy collection publish` sends; immutable versions, tombstones for deleted ones | none of the format |
| Remote | a cache of a Galaxy-compatible server; `remote.url` is its API root, `https://galaxy.ansible.com/api/` by default. Role archives come from the extra hosts of the repository, `github.com` and `codeload.github.com` by default | `ansible.authUrl`, `ansible.clientId` |
| Virtual | one address over local and remote members | none of the format |

A remote verifies the SHA-256 that the upstream announces for every collection archive, whatever the checksum policy says, so the policies that skip the check are refused for this format. Signatures of a version pass through unchanged; trove neither signs nor verifies them. A role archive has no announced digest and is stored as fetched, within the limit below.

Give clients a virtual repository: one `server_list` entry then reads your own collections and the public ones. The first publish of a name into a local member claims the name for it, so a remote member's versions of that name are not offered through the virtual repository (a public collection cannot slip in under the name of one of yours). A grant with a path pattern that excludes an archive hides that version from the list and from the install of the principal it is granted to.

## Mirror

A remote in mode `mirror` keeps the collections its include patterns select (`acme.*`, `community.general`), so that they stay installable while the upstream is away. A pattern that bounds the set is required. A pass stores the collection documents, the version lists and version documents, and every archive of the selected collections within the version bounds; installs afterwards make no request to the upstream. Roles and the index of a hub are not read. A mirror of the whole public Galaxy is a configuration of its own and not measured, so plan its storage before you start one; start and watch a pass on the edit form of the repository.

## Settings

`ansible.authUrl` (the token endpoint of the identity realm of a hub behind single sign-on, an `https` URL) and `ansible.clientId` (default `cloud-services`) are accepted for remote repositories only: with `authUrl`, the offline token stored in the repository's credential is exchanged for a short-lived access token before the upstream is called. The exchange is specified from documentation and has never run against a real hub, so try it with a test token first.

## Limits and retention

| Key | Default | What it bounds |
|---|---|---|
| `ansible.max_archive_bytes` | `1GiB` | the largest collection archive a publish or an upload accepts; a larger body is refused with `413` |
| `ansible.role_archive_max_bytes` | `256MiB` | the largest role archive a remote keeps |
| `ansible.import_task_keep` | `720h` | how long an import task stays readable; the task `ansible-import-task-sweep` deletes older ones and keeps the versions |

The page [Configuration](configuration.md) lists every key.

## Import tasks and tombstones

On the edit form of a local repository, **Import tasks** lists the publishes newest first with their state and messages; a virtual repository shows the tasks of its deployment target. **Tombstones** (local, needs the permission **Manage**) lists the deleted versions whose numbers are blocked and clears one after a confirmation. Clearing is audited; the number can then be published again with other bytes, while clients and caches that have seen the deleted version may still hold the old archive.

## Recalculate

`POST …/reindex` of a local repository enqueues the job `ansible-reindex`: it reads the two documents of every stored archive again and rewrites the rows that the version lists are built from, after a restore or after archives were copied in. It never changes stored bytes. On a remote repository the operation answers `405`.

## Copying the collections of an existing Galaxy

`trove admin import ansible` copies the collections of an existing Galaxy into a local repository; see [Importing from another registry](import.md). Roles are not copied; the report counts them once per run.
