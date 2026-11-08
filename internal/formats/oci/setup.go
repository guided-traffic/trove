package oci

import "github.com/guided-traffic/trove/internal/formats"

// The client IDs (R-OCI-030).
const (
	ClientDocker = "docker"
	ClientPodman = "podman"
)

// clients are the tested clients of the format: the Docker CLI with Buildx and Podman. Kubernetes pulls with an image pull secret
// and has no client of its own here. Versions are the majors the e2e suite pins (R-OCI-035).
func clients() []formats.Client {
	return []formats.Client{
		{ID: ClientDocker, DisplayName: "Docker CLI (with Buildx)", Versions: []string{"28"}},
		{ID: ClientPodman, DisplayName: "Podman", Versions: []string{"5"}},
	}
}

// loginTarget is what a login command names: "<host>/<projectKey>/<repoKey>", the form an image reference starts with
// (SetupData.ImagePrefix). One spelling serves the login, the pull and the push: Podman and Kubernetes accept the path as the narrower
// scope of a credential; the Docker CLI is not documented to (it is expected to store the credential by the host, so a second login to
// another repository of the same host replaces the first).
const loginTarget = `{{shell .ImagePrefix}}`

// The user name of a login. The token service records it and ignores it (R-OCI-004, INV-8), so the instructions use a fixed one.
const loginUser = "trove"

// SetupTemplates are the client setup snippets of R-OCI-030 and R-UI-018: how to sign a client in (configure), how to pull an
// image through the repository (install) and how to push one (publish; the API renders it for a caller who holds deploy on the publish
// target). A template sees [formats.SetupData] and never a token, so the credential is always a reference to the environment
// variable that the shell expands itself, written in a line that the API drops when the reader asked for snippets without a credential.
//
// The login line reads the token from standard input, never from the command line, so it does not land in the process list or the
// shell history. A repository that is open to anonymous readers (SetupData.RequiresAuth false) needs no login to pull; the snippet
// then is the interactive login, which a client needs only to push, and which asks for the token itself.
//
// The image reference is "<host>/<projectKey>/<repoKey>/<name>[:<tag>]" and not the registry URL: the URL carries the "/v2" of the
// protocol, which no image reference has (SetupData.ImagePrefix, Q-OCI-4).
func (f *Format) SetupTemplates() []formats.SetupTemplate {
	login := func(tool string) string {
		return `{{if .RequiresAuth}}printf '%s' "` + formats.SetupTokenRef + `" | ` + tool + ` login ` + loginTarget +
			` --username ` + loginUser + ` --password-stdin
{{else}}` + tool + ` login ` + loginTarget + ` --username ` + loginUser + `
{{end}}`
	}
	pull := func(tool string) string { return tool + " pull " + loginTarget + "/" + formats.SetupPackageRef + "\n" }
	push := func(tool string) string {
		return tool + " tag myapp:1.0 " + loginTarget + "/myapp:1.0\n" + tool + " push " + loginTarget + "/myapp:1.0\n"
	}
	// An image pull secret is how Kubernetes signs in; its password is a trove token (R-OCI-004). The server is the image prefix, so
	// the kubelet sends the credential for the images below it only. The token reaches kubectl through a file it reads from standard
	// input, built by the shell's printf (a builtin, so the token is in no argument list), and not through --docker-password, whose value
	// would sit on kubectl's command line. A reader who asked for no credential gets a comment: a secret without one is no secret.
	pullSecret := `{{if .RequiresAuth}}printf '{"auths":{"%s":{"username":"` + loginUser + `","password":"%s"}}}' ` + loginTarget + ` "` + formats.SetupTokenRef + `" | \
  kubectl create secret generic trove-registry --type=kubernetes.io/dockerconfigjson --from-file=.dockerconfigjson=/dev/stdin
{{else}}# An image pull secret holds a credential: ask for the snippet with one.
{{end}}`
	return []formats.SetupTemplate{
		{ID: "docker.shell.configure", Client: ClientDocker, Section: "configure", Lang: "shell", Template: login("docker")},
		{ID: "docker.kubernetes.configure", Client: ClientDocker, Section: "configure", Lang: "shell", Template: pullSecret},
		{ID: "podman.shell.configure", Client: ClientPodman, Section: "configure", Lang: "shell", Template: login("podman")},

		// The version of an image is its tag, written after a colon; the quoted segments of a shell word join into one reference.
		{ID: "docker.install", Client: ClientDocker, Section: "install", Lang: "shell", Template: pull("docker"), VersionSpec: ":" + formats.SetupVersionRef},
		{ID: "podman.install", Client: ClientPodman, Section: "install", Lang: "shell", Template: pull("podman"), VersionSpec: ":" + formats.SetupVersionRef},

		{ID: "docker.publish", Client: ClientDocker, Section: "publish", Lang: "shell", Template: push("docker")},
		{
			ID: "docker.buildx.publish", Client: ClientDocker, Section: "publish", Lang: "shell",
			Template: "docker buildx build --platform linux/amd64,linux/arm64 --tag " + loginTarget + "/myapp:1.0 --push .\n",
		},
		{ID: "podman.publish", Client: ClientPodman, Section: "publish", Lang: "shell", Template: push("podman")},
	}
}

// optionNames are the client options a template spells although their names contain "password" (kit check 8): the name of the option,
// never a value.
func optionNames() []string { return []string{"--password-stdin", `"password"`} }
