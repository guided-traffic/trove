{{if .Anonymous}}You are not signed in, so the snippets carry no credential. A repository that is open to anonymous readers needs none to read; the key download and `apt-get update` work without it. Uploading always needs a token.
{{else}}APT sends its credentials from a file below `/etc/apt/auth.conf.d/`, never from the source line. The snippet creates the file with the permissions `0600`, so only root can read it, and writes the host, the user name and the token into it. The user name is recorded and ignored: trove checks the token only. The token comes from the environment variable `{{.TokenEnvVar}}`, which has to be set before you run the snippet.
{{end}}
Do not put the credential into the URL of the source: that line lands in world-readable files, in the output of `apt-get update` and in logs.

A principal whose permission to read is narrowed to some package names can download the package files it may read, but cannot use the repository as an APT source: the index lists every package of a suite, so reading it needs the permission without package restriction.
