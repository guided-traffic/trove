`ansible-galaxy` reads its servers from the `[galaxy]` section of an `ansible.cfg`: `server_list` names them, and each name has a `[galaxy_server.<name>]` section with the `url`. Use the address of the snippet as it is, with the repository in it; there is no separate path for the API.

Put the `ansible.cfg` next to your playbooks, or in `~/.ansible.cfg` for the whole machine. `ansible-galaxy` asks the servers of `server_list` in order and takes the first that has the collection, so a virtual repository as the only server is the simplest setup. With several servers, list your own repository first, and note that a collection of the same name on a later server is never consulted when an earlier one has it.

{{if .Anonymous}}You are not signed in, so the snippets carry no credential. They work for repositories that are open to anonymous readers.{{else}}The snippets do not write the token into the file. `ansible.cfg` does not expand variables, so a reference there would be sent as the token itself. The server is named `trove` in the snippet, and `ansible-galaxy` reads the token of a server named `trove` from the environment variable `ANSIBLE_GALAXY_SERVER_TROVE_TOKEN`. Set it in your shell or your CI job, as in the snippet, and keep the token out of version control.{{end}}

The address of {{if .Repository}}`{{.Repository}}`{{else}}a repository{{end}} is `{{.RegistryURL}}`.
