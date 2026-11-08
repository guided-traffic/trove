APT trusts a repository through a key file named in the source: the `Signed-By` field. The snippet below downloads this repository's public key from `signing-key.asc`, installs it under `/etc/apt/keyrings`, and writes the source file in the deb822 form. Nothing in it turns verification off: the options `trusted=yes` and `allow-insecure`, and `apt-key`, are not used and not needed.

{{if .Repository}}The files are named after the project and the repository (`trove-{{.Project}}-{{.Key}}`), so two repositories of one host never share a key file. {{end}}The suite is the `Suites:` line of the source; change it to the suite you read from. The one-line form of the source is shown as well, for systems that keep their sources in `sources.list.d/*.list`.

The address of the repository is {{if .RegistryURL}}`{{.RegistryURL}}`{{else}}`<base>/api/deb/<project>/<repository>/`{{end}}. APT adds `dists/<suite>/…` for the index files and the path from the index for the package files.

### Your own certificate authority

When trove is served with a certificate of your own authority, APT has to trust it: point `Acquire::https::CAInfo` at the CA certificate (a file below `/etc/apt/apt.conf.d/`), or install the certificate in the system store. Without it `apt-get update` ends in a certificate error. Do not work around that by turning the peer verification off.
