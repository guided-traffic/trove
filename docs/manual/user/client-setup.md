# Connecting a package client

How to point npm, pnpm, Yarn and curl at a trove repository, sign them in with a token from an environment variable, and install, publish and maintain packages and files through it. The page explains the concepts and the places in the UI that give you the exact configuration of your installation: the Client setup page, the Guides area and the How to use tab of a repository.

## Where the instructions are

trove gives you the configuration for your installation in four places. All of them use the configured addresses of the endpoint you are using, so you can copy what they show.

| Place | How to open it | What it gives you |
|---|---|---|
| **Client setup** | User menu › **Client setup**, or **Set up your client for this repository** on a package page | Configuration snippets in the tabs **Configure**, **Install** and **Publish**, with a choice of credentials |
| **Guides** | **Packages and files › Guides** | The full guide of a client: what the repository class is, configuring, signing in, installing, publishing, dist-tags and unpublish, security audit, lockfiles, troubleshooting |
| **How to use** | A tab on the screen of a repository (a section, if you may use the repository but not see its configuration) | The guide of that one repository, with a choice of client and links **Open the client setup** and **Open in the guides** |
| **Install** panel | On the page of a package | The install command of the selected version for the client and repository you choose |

The two main places differ like this:

| | Client setup | Guides and How to use |
|---|---|---|
| Content | Snippets only, grouped by tab | Explanations with the same snippets in context |
| Credentials | An environment variable, a token created for this setup, or a token you paste | Always the environment variable; a guide never contains a token |
| Repository | Required | Optional on **Guides**: without one, the guide covers a repository class and the addresses show `<repository>` |
| Files | **Download** for snippets that are whole configuration files | Copy only |

Each page links to the other with the same package type, client and repository. Choosing anything on these pages changes nothing on the server, except **Create token** on the Client setup page.

## Repository addresses

Every repository has one client address, built from the configured address of the endpoint you use:

| Package type | Address | Example |
|---|---|---|
| npm | `<base URL>/api/npm/<repository key>/` | `https://trove.example.com/api/npm/npm-all/` |
| Generic | `<base URL>/files/<repository key>/` | `https://trove.example.com/files/generic-local/` |

The trailing slash belongs to the address. Which class of repository to use:

- **Virtual** repositories are the recommended address: internal and public packages through one address and one credential. A publish through a virtual repository goes to its deployment target, a local member.
- **Local** repositories hold what your teams publish and serve nothing else.
- **Remote** repositories cache an upstream registry and are read-only.

[Repositories](repositories.md) explains the classes. If the Client setup page or a guide answers that the service is unavailable, the installation has no configured base URL for the endpoint you use; tell an administrator.

## Use the Client setup page

1. Under **Package type**, choose the format.
2. Under **Client**, choose your client: **npm**, **pnpm**, **Yarn Berry (2 and later)** or **Yarn Classic (1.x)** for npm, **curl** for generic repositories.
3. Under **Repository**, choose one of the repositories you can read. Virtual repositories come first and are marked **recommended**; each entry says **Read only**, **Publishing goes to its default deployment target**, **You can publish** or **You cannot publish**.
4. Under **Credentials**, choose how the snippets sign in (next section). Without a session this step only says that the snippets contain no authentication.
5. Copy the snippets from the tabs **Configure**, **Install** and **Publish** with **Copy snippet**, or save a configuration file with **Download**.

For npm repositories the **Publish** tab holds no snippet in this version, and the sentence it shows in its place is not a statement about your permissions: the hint of step 3 says whether you can publish, and the guide's section **Publish a package** has the commands.

### Credentials

| Option | What the snippets contain | Notes |
|---|---|---|
| **Reference an environment variable** (default) | `${TROVE_TOKEN}`, which the client expands when it runs | No secret on the page; the file can be committed. The link **Create a token to put into the variable** opens your personal tokens |
| **Create a token for this setup** | A new personal token, written into the snippets | See below |
| **Use a token I already have** | The token you paste | The value stays in the browser and is never sent to the server |

**Create a token for this setup** creates a personal token with the description `Client setup: <client>, <repository>, <date>` that may read the chosen repository only. Tick **The token may also publish to the deployment target of this repository** to let it publish as well. It lives 30 days unless an administrator configured another period, and it appears under your [personal tokens](access-tokens.md), where you revoke it. The page shows it once; choosing another repository or credential option, or leaving the page, discards it. Creating it can ask you to [sign in again](troubleshooting.md#session-ended-and-re-authentication).

Reading through a virtual repository also needs read on its members, so a token limited to a virtual repository finds no package there. For a virtual repository, reference the environment variable and put a token into it whose scope covers the members too.

A pasted token is checked in the browser for its prefix, its length of 57 characters, its alphabet and its checksum; a value that fails is not written into the snippets. A file that holds a token belongs in your home directory, never in version control.

## Configure an npm client

| Client | File | Credential setting |
|---|---|---|
| npm | `.npmrc` in the project, or `~/.npmrc` for every project | `//<host>/api/npm/<key>/:_authToken=${TROVE_TOKEN}` |
| pnpm | `~/.npmrc` (pnpm 11 and 12 ignore a credential that names a variable in a project file) | as npm |
| Yarn Berry | `.yarnrc.yml` in the project (Yarn Berry does not read `.npmrc`) | `npmAuthToken: "${TROVE_TOKEN}"` |
| Yarn Classic | `.npmrc` in the project | as npm |

The npm credential line names the registry address without its scheme, so the client sends the token to this repository and to no other URL. When the repository is not open to anonymous readers, Yarn Berry needs `npmAlwaysAuth: true` and Yarn Classic `always-auth=true`, or they send the token for scoped packages only; the snippets add the switch when it is needed. An `.npmrc` for npm looks like this:

```ini
# example: the virtual repository npm-all
registry=https://trove.example.com/api/npm/npm-all/
//trove.example.com/api/npm/npm-all/:_authToken=${TROVE_TOKEN}
```

and a `.yarnrc.yml` for Yarn Berry:

```yaml
npmRegistryServer: "https://trove.example.com/api/npm/npm-all/"   # example
npmAlwaysAuth: true            # only when the repository is not open to anonymous readers
npmAuthToken: "${TROVE_TOKEN}"
```

To take only one scope from trove and everything else from the client's default registry, replace `registry=` by `@your-scope:registry=<address>` and keep the credential line (Yarn Berry: an `npmScopes` entry); the guide shows this variant for every client.

### Sign the client in

Put a [personal access token](access-tokens.md) into the environment variable before the client runs, without typing it on a command line your shell keeps in its history:

```shell
read -rs TROVE_TOKEN && export TROVE_TOKEN
npm whoami
```

`npm whoami`, `pnpm whoami` and `yarn npm whoami` print your trove user name (`sa:<name>` for a service-account token); Yarn Classic has no such command. In CI, store a service-account token with only the permissions the job needs as a masked, protected variable named `TROVE_TOKEN`. The variable has this name unless an administrator configured another; the snippets always show the name in use. `npm login` and its pnpm and Yarn equivalents do not work with trove: put the token into the configuration instead.

## Install, publish and maintain npm packages

| Task | npm | pnpm | Yarn Berry | Yarn Classic |
|---|---|---|---|---|
| Install | `npm install <package>` | `pnpm add <package>` | `yarn add <package>` | `yarn add <package>` |
| Install from the lockfile | `npm ci` | `pnpm install --frozen-lockfile` | `yarn install --immutable` | `yarn install --frozen-lockfile` |
| Publish | `npm publish` | `pnpm publish` | `yarn npm publish` | `yarn publish --new-version <version>` |
| Set a dist-tag | `npm dist-tag add <name>@<version> <tag>` | `pnpm dist-tag add <name>@<version> <tag>` | `yarn npm tag add <name>@<version> <tag>` | `yarn tag add <name>@<version> <tag>` |
| Unpublish a version | `npm unpublish <name>@<version>` | `pnpm unpublish <name>@<version> --force` | none | none |
| Security audit | `npm audit` | `pnpm audit` (pnpm 11 and later) | `yarn npm audit` | not through trove |

What each needs and how the classes behave:

- **Permissions.** Installing needs read, publishing and changing dist-tags deploy, unpublishing delete. Through a virtual repository you also need read on the members you install from.
- **Local repositories** accept a name and version once: publishing it again answers `409` `npm_version_exists`, and an unpublished version number is blocked for good (`409` `npm_version_unpublished`). By default, a publish of a version lower than `latest` stores it and leaves `latest` where it is; move the tag yourself. `latest` cannot be removed, only moved.
- **Remote repositories** take no writes (`405` `npm_class_not_writable`). A package document is refreshed from the upstream after its metadata period, two hours unless the repository sets another.
- **Virtual repositories** send publish, dist-tag changes and unpublish to their deployment target; without one they answer `405` `no_deploy_target`. pnpm 11 and 12 cannot unpublish through a virtual repository; unpublish at the deployment target.
- **Package names.** The first publish of a name to a local repository records its owners and, by default, claims the name: virtual repositories that contain the local repository then never take a public package of that name from their remote members. [Errors and troubleshooting](troubleshooting.md#typical-npm-problems) explains the refusals this can cause.
- **Security audit** is forwarded to an advisory provider by remote repositories and by virtual repositories with a forwarding remote member, with only names that are not internal. A local repository answers `501` `npm_audit_unavailable`.
- **Not available in this version:** `npm deprecate` (it fails with `E404` and changes nothing) and `npm publish --stage` (`501`).

The guide of your client and repository names the exact limits, notices and client quirks.

## Lockfiles

- **npm** and **Yarn Classic** record the address and integrity of every tarball. The addresses lie under the repository the install went through, and an install from the lockfile fetches them directly, whatever the configuration says now. To move an npm lockfile to another repository that serves the same versions, replace the old address by the new one in it; the integrity values stay valid.
- **pnpm** checks every tarball against the integrity in `pnpm-lock.yaml`.
- **Yarn Berry** writes no registry addresses into `yarn.lock`, so pointing `npmRegistryServer` elsewhere needs no change to the lockfile.

The integrity values stay valid because a version's bytes never change in a local repository, and a remote repository stores a tarball only when it matches the integrity its upstream announces. By default, a version the upstream withdrew answers `404` through remote and virtual repositories, also for an install from a lockfile. `EINTEGRITY` usually means the lockfile was written against a registry that served other bytes for the version.

## Download and upload files with curl

The configure snippet for a generic repository defines a shell function, `trove_curl`, that hands curl the token on standard input, so the token appears neither on curl's command line nor in a shell trace:

```shell
trove_curl() {
  curl --silent --show-error --config - "$@" <<EOF
header = "Authorization: Bearer ${TROVE_TOKEN}"
EOF
}
trove_curl --fail --remote-name "https://trove.example.com/files/generic-local/tools/app-1.2.0.tar.gz"
trove_curl --fail --upload-file app-1.2.0.tar.gz "https://trove.example.com/files/generic-local/tools/app-1.2.0.tar.gz"
```

Downloading needs read, uploading to a free path deploy, replacing a file with other bytes overwrite as well, deleting delete. Add `--header "If-None-Match: *"` to an upload that must never replace a file: it then answers `412` when the path exists. [Browsing and transferring files](files.md) describes paths, digests and folders; the curl guide shows resuming, conditional downloads and uploads with a digest.

## What the in-app guide adds

This page explains the concepts; the guide of your installation gives the exact text for your choice:

- the addresses of the endpoint you are using, and for a chosen repository its key and its deployment target;
- sections that depend on the class and the client, and only the steps that apply to you: an anonymous reader gets no sign-in steps, a repository you cannot publish to says so;
- the section **On this endpoint**, shown only on an endpoint with exposure rules, which says what such an endpoint hides and refuses;
- a **Troubleshooting** table with the error codes of the protocol and what to do about each.

When something goes wrong, start with [Errors and troubleshooting](troubleshooting.md).
