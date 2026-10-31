Paste the snippet into the shell, or into the CI script, that runs curl; the snippets are written for a POSIX shell such as sh, bash or zsh. It defines the shell function `trove_curl`, which runs curl with the options and the address you give it{{if not .Anonymous}} and adds the header `Authorization: Bearer` with the token from the environment variable `{{.TokenEnvVar}}`.

The function reads the variable each time it runs and hands the header to curl as configuration on standard input (`--config -`). The token therefore appears neither on curl's command line, where other users of the machine could read it in the process list, nor in a shell trace (`set -x`), and no file holds it: there is no `.netrc` to keep secret. Because standard input carries the configuration, `trove_curl` cannot upload from standard input (`--upload-file -`); upload a file instead{{end}}.
{{if .Anonymous}}
You are not signed in, so the function sends no credential. It works for repositories that are open to anonymous readers.
{{end}}
### Addresses

The address of {{if .Repository}}`{{.Repository}}`{{else}}the repository{{end}} is `{{.RegistryURL}}`; append the path of a file to it. curl does not encode the address for you, so write a space in a path as `%20`. curl also resolves `.` and `..` segments itself before it sends the request, so a path holding them reaches another file; trove refuses such a path with `400` `invalid_path` when it arrives as written (`--path-as-is`).
