Install a collection with `ansible-galaxy collection install namespace.name`, optionally with `:<version>` for an exact version. A `requirements.yml` lists several at once; install it with `ansible-galaxy collection install -r requirements.yml`. A `source:` entry names the server of `server_list` to ask, and without one every server is asked in order.

The dependencies of a collection are resolved by the client through the same servers, so each of them passes the same rules of the repository.

Roles are a separate command, `ansible-galaxy role install -r requirements.yml`, and the entries of the `roles:` list apply to it. What trove serves of them:

- A role named `<owner>.<role>` is served by a remote or a virtual repository over a remote Galaxy. The client takes the first server of `server_list` that offers roles, so a local repository in front of such a server does not hide them.
- A role with a `git+https` source, a `scm: git` entry or a URL to an archive is not served by trove. The client clones or downloads it on its own, and trove neither sees nor caches it.
{{if eq .Class "local"}}- This is a local repository: it serves no roles. Put a virtual repository in `server_list` for them, or publish the role inside a collection.
{{end}}
`ansible-galaxy` caches what it downloaded in `~/.ansible/galaxy_cache`. After a version was removed on the server, the cached list can still show it until the cache expires; `--clear-response-cache` forces a new request.
