`ansible-galaxy` sends the credential in one of three forms, and trove accepts all of them with a trove token:

| How you configure it | What the client sends |
|---|---|
| the variable `ANSIBLE_GALAXY_SERVER_TROVE_TOKEN`, a `token=` line in `ansible.cfg`, or `--token` | `Authorization: Token <token>` |
| `username=` and `password=` in `ansible.cfg` | HTTP Basic with the token as the password; the user name is ignored, and Basic authentication has to be enabled on this installation |
| a `~/.netrc` line for the host | HTTP Basic for that host, with the same rule |

Prefer the environment variable: it keeps the token out of every file. A `token=` line in `ansible.cfg` works, but it writes the token to disk and into whatever commits the file.

**Roles need the `~/.netrc` line.** The client downloads a role archive without its token header and falls back to `~/.netrc`. Without a line for the host the role lookup works, because it carries the token, and the archive download fails with the client's `401`. One `machine` line serves every repository on the host, so a token that is scoped to one repository cannot fetch a role from another. Run `chmod 600 ~/.netrc`, as the snippet says.

{{if .Anonymous}}A repository that is open to anonymous readers needs no credential to install from. Publishing always needs a token.{{else}}A token that is present and wrong is refused with `401`; it never falls back to anonymous access. A token bound to another endpoint, or scoped to a path or package that the request is not in, is refused as well.{{end}}
