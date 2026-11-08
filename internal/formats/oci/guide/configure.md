{{if eq .Client "docker"}}The Docker CLI needs no setting to find a registry: the host is part of every image reference. What it needs is a login, and Docker keeps the credential per host, so one `docker login` covers every repository of every project on this trove host. A second `docker login` to the same host, for another repository or with another token, replaces the first credential; keep one token that covers what you pull and push, or log in again before the command that needs the other.{{else}}Podman needs no setting to find a registry: the host is part of every image reference. What it needs is a login. Podman keeps the credential in its own `auth.json` and falls back to the file of the Docker CLI, so a machine that already ran `docker login` for this host pulls with Podman as well.{{end}}

The command of the snippet reads the token from standard input, so it neither appears in the process list nor lands in the shell history. The user name is recorded and ignored: trove checks the token only.{{if not .Anonymous}} The snippet reads the token from the environment variable `{{.TokenEnvVar}}`; the variable has to be set before you run it.{{end}}
{{if .Anonymous}}
You are not signed in, so the snippets carry no credential. Pulling from a repository that is open to anonymous readers needs no login at all; pushing always does.
{{end}}
{{if eq .Client "docker"}}
### Kubernetes

A cluster pulls with an **image pull secret** whose password is a trove token. The `kubectl` snippet creates it; reference it in the pod's `imagePullSecrets` or in the service account. The server of the secret is the image prefix, so the kubelet sends the credential only for the images below it. Kubernetes pulls by digest as well as by tag, so a workload pinned to a digest keeps starting after a tag has moved.
{{end}}
{{if eq .Client "docker"}}
### Your own certificate authority

When trove is served with a certificate of your own authority, the Docker daemon (not the CLI) has to trust it: place the CA certificate at `/etc/docker/certs.d/<host>/ca.crt` on the machine that runs the daemon. Without it every command ends in `x509: certificate signed by unknown authority`.
{{else}}
### Your own certificate authority

When trove is served with a certificate of your own authority, Podman has to trust it: place the CA certificate at `/etc/containers/certs.d/<host>/ca.crt` or `~/.config/containers/certs.d/<host>/ca.crt`. Without it every command ends in `x509: certificate signed by unknown authority`.
{{end}}
The address of {{if .Repository}}`{{.Repository}}`{{else}}a repository{{end}} is `{{if .ImagePrefix}}{{.ImagePrefix}}{{else}}<host>/<project>/<repository>{{end}}`.
