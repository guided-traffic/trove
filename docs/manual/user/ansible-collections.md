# Ansible collections and roles with ansible-galaxy

How to point ansible-galaxy at a trove repository, give it a token, install collections and roles, publish a collection, and what the messages of the client mean. The Client setup page and the How to use tab of a repository give the same commands with the addresses of your installation.

## The address and what ansible-galaxy reads

trove serves the Galaxy API from repositories of the format **Ansible Galaxy**. The client is `ansible-galaxy` of ansible-core; the setup lists 2.17 and 2.21. The address of a repository is the server URL you give the client:

| Part | Example | Rule |
|---|---|---|
| Address | `https://trove.example.com/api/ansible/web/ansible-local/` | the base URL, `/api/ansible/`, the project key and the repository key, with the trailing slash |

Use the address as it is. There is no separate path for the API: the address itself answers the discovery document the client asks first.

| Class | Collections | Roles |
|---|---|---|
| Local | the collections you publish | not served: `ansible-galaxy role install` against a local repository alone fails with the client's message that the v1 API is not available |
| Remote | a cache of a Galaxy, the public one by default | served by translation of the Galaxy's role API, the archive fetched from the hosts the administrator allowed |
| Virtual | the union of its members | answered by the first remote member that knows the role |

A collection name is `namespace.name` in lower case; a version is a semantic version such as `1.2.3` or `1.0.0-rc.1`.

## Configure ansible-galaxy

The Client setup page, with **Package type** Ansible Galaxy and **Client** **Ansible Galaxy (ansible-galaxy)**, renders this for the repository you choose:

```ini
# ansible.cfg, next to your playbooks or as ~/.ansible.cfg
[galaxy]
server_list = trove

[galaxy_server.trove]
url=https://trove.example.com/api/ansible/web/ansible-local/
```

```shell
# a repository that is not anonymously readable also needs the token, from the environment
export ANSIBLE_GALAXY_SERVER_TROVE_TOKEN="${TROVE_TOKEN}"
```

`ansible.cfg` does not expand variables, so the token is not written into it: a reference there would be sent as the token itself. `ansible-galaxy` reads the token of a server named `trove` from `ANSIBLE_GALAXY_SERVER_TROVE_TOKEN`, so keep the server named `trove`, or rename the variable after the server (`ANSIBLE_GALAXY_SERVER_<NAME>_TOKEN`, upper case). The page also renders the forms for PowerShell and cmd.

With several servers, `ansible-galaxy` asks them in the order of `server_list` and takes the first that has the collection. Put your own repository first, or give the client one virtual repository.

## Which credential ansible-galaxy sends

| How you configure it | What the client sends |
|---|---|
| `ANSIBLE_GALAXY_SERVER_TROVE_TOKEN`, a `token=` line in `ansible.cfg`, or `--token` | `Authorization: Token <token>` |
| `username=` and `password=` in `ansible.cfg` | HTTP Basic with the token as the password; the user name is ignored |
| a `~/.netrc` line for the host | HTTP Basic for that host |

Prefer the environment variable: it keeps the token out of every file. A wrong token is refused with `401` and never falls back to anonymous access.

**Roles need the `~/.netrc` line.** The client downloads a role archive without its token header and falls back to `~/.netrc`. The setup page renders the line for your host:

```shell
touch ~/.netrc && chmod 600 ~/.netrc
printf 'machine %s login trove password %s\n' trove.example.com "${TROVE_TOKEN}" >>~/.netrc
```

The command appends the line, because netrc expands no variable. The client uses the line for every request to the host and then sends Basic in place of its token header, so keep the token in it valid.
Without the line the role lookup works, because it carries the token, and the archive download fails with the client's `401`. One line serves every repository on the host, so a token that is scoped to one repository cannot fetch a role from another.

## Install

```shell
ansible-galaxy collection install acme.tools            # the newest stable version
ansible-galaxy collection install acme.tools:1.2.0      # an exact version
ansible-galaxy collection install -r requirements.yml   # several, with versions and ranges
```

```yaml
# requirements.yml
collections:
  - name: acme.tools
    version: '>=1.0.0'
    source: trove
roles:
  - name: geerlingguy.pip
```

The dependencies of a collection are resolved by the client through the same servers, so each of them passes the same rules of the repository. Roles are installed with `ansible-galaxy role install -r requirements.yml`; the `roles:` entries need a remote or virtual repository over a Galaxy and the netrc line above. A role with a Git or URL source is fetched by the client on its own and never passes through trove.

`ansible-galaxy` caches its answers in `~/.ansible/galaxy_cache`. After a version was removed on the server, the cached list can still show it; `--clear-response-cache` forces a new request.

## Publish a collection

```shell
ansible-galaxy collection build                         # writes acme-tools-1.0.0.tar.gz
ansible-galaxy collection publish acme-tools-1.0.0.tar.gz --server https://trove.example.com/api/ansible/web/ansible-local/ --token "${TROVE_TOKEN}"
```

The collection's `galaxy.yml` names the namespace, name and version, so the command needs no coordinates; the namespace and name have to be lower case. The command prints the import messages of the server and exits with an error when the import failed, and then the repository holds no version. You need the permission **Deploy** on the repository. On a virtual repository the publish goes to its deployment target, which has to be a local repository.

- Publishing the same file again succeeds and changes nothing.
- Different bytes under an existing version are refused (`version_exists`). Publish a new version number.
- A deleted version leaves a tombstone, and its number cannot be published again (`version_tombstoned`) until an administrator clears the tombstone.
- A first publish of a name that a remote member of a virtual repository already serves is refused unless you may manage the repository.

The web interface shows the import tasks of a local repository on its **Import tasks** tab, and the version with the Ansible versions it needs, its dependencies, licenses and tags on the package page.

## Errors and what to do

| What you see | Why, and what to do |
|---|---|
| `HTTP Error 401` at the first request, naming the server | The repository needs a credential and the client sent none or a wrong one. Check that the variable is set in the shell that runs the command and that the server is named `trove`. |
| `403` or `permission_denied` on a publish | The token may read but not deploy to the repository, or the namespace belongs to another publisher. |
| `Mismatch artifact hash` | The archive does not match the SHA-256 in the version document. trove refuses an archive that does not match what the upstream announced, so you see this when a document was changed. Report it to the administrator. |
| `Galaxy import process failed` with a description | The server refused the archive: not a collection archive, a name or version that breaks the rules, or a manifest that disagrees with the file name. |
| `The v1 API is not available`, or a role that is not found | A role install reached a repository that serves no roles. Put a virtual repository with a remote Galaxy member into `server_list`. |
| A role downloads its metadata and then fails with `401` | The netrc line is missing for this host. |
| `503 upstream_unavailable` | A remote member could not be asked and the merged answer would have been incomplete. Try again. |
