/** A package-manager client the setup dialog and the install panel offer for a format. */
export interface ClientChoice {
  /** The value of the `client` parameter of R-API-030. */
  readonly id: string;
  /** A product name; it is not translated. */
  readonly label: string;
}

/**
 * The clients per format (R-UI-016, R-UI-009). The server owns the snippets and rejects a client
 * it does not know; this table only decides what the pickers offer. A format without an entry has
 * no setup screen yet and is not offered. NuGet offers the .NET CLI, nuget.exe
 * and Visual Studio (R-UI-018, Q-UI-93). OCI offers the Docker CLI and Podman (R-OCI-030); a Kubernetes cluster
 * signs in with the image pull secret of the Docker CLI's configure section. Debian offers APT: apt and apt-get read
 * one source format, so they are one client (R-DEB-029).
 */
export const FORMAT_CLIENTS: Readonly<Record<string, readonly ClientChoice[]>> = {
  generic: [{ id: 'curl', label: 'curl' }],
  npm: [
    { id: 'npm', label: 'npm' },
    { id: 'pnpm', label: 'pnpm' },
    { id: 'yarn', label: 'Yarn Berry (2 and later)' },
    { id: 'yarn-classic', label: 'Yarn Classic (1.x)' },
  ],
  nuget: [
    { id: 'dotnet', label: '.NET CLI (dotnet)' },
    { id: 'nuget', label: 'NuGet CLI (nuget.exe)' },
    { id: 'visual-studio', label: 'Visual Studio' },
  ],
  oci: [
    { id: 'docker', label: 'Docker CLI (with Buildx)' },
    { id: 'podman', label: 'Podman' },
  ],
  deb: [{ id: 'apt', label: 'APT (apt, apt-get)' }],
};

export function clientsOf(format: string): readonly ClientChoice[] {
  return FORMAT_CLIENTS[format] ?? [];
}

/**
 * The explanation of a snippet, by its stable id (R-API-030: the server sends ids, the text is the
 * UI's and is translated, R-UI-045). An id without an entry shows the snippet without a sentence.
 */
export function snippetExplanation(id: string): string | null {
  switch (id) {
    case 'npm.configure.npmrc':
    case 'pnpm.configure.npmrc':
    case 'yarn-classic.configure.npmrc':
      return $localize`:@@setup.snippet.npmrc:Save this as .npmrc in your project. It sends every request for packages to this repository and signs in with the token.`;
    case 'npm.configure.npmrc-scoped':
    case 'pnpm.configure.npmrc-scoped':
    case 'yarn-classic.configure.npmrc-scoped':
      return $localize`:@@setup.snippet.npmrc-scoped:Use this variant to take only the packages of one scope from this repository. All other packages still come from npm's default registry.`;
    case 'yarn.configure.yarnrc-scoped':
      return $localize`:@@setup.snippet.yarnrc-scoped:Add this to .yarnrc.yml to take only the packages of one scope from this repository. All other packages still come from npm's default registry.`;
    case 'npm.configure.cli':
      return $localize`:@@setup.snippet.npm-cli:Writes the same settings into your user configuration file ~/.npmrc. Your shell fills in the token, so it is stored in that file.`;
    case 'yarn.configure.yarnrc':
      return $localize`:@@setup.snippet.yarnrc:Save this as .yarnrc.yml in your project. Yarn fills in the token variable itself.`;
    case 'npm.install':
    case 'pnpm.install':
    case 'yarn.install':
    case 'yarn-classic.install':
      return $localize`:@@setup.snippet.install:Installs a package through this repository once the client is configured.`;
    case 'npm.publish':
    case 'pnpm.publish':
    case 'yarn-classic.publish':
      return $localize`:@@setup.snippet.publish-flag:Publishes the package in the current directory to the repository that receives uploads.`;
    case 'yarn.publish':
      return $localize`:@@setup.snippet.publish-berry:Add this to .yarnrc.yml, then run yarn npm publish.`;
    case 'npm.publish.gitlab-ci.generic':
      return $localize`:@@setup.snippet.gitlab-generic:For npm 10, pnpm 10 on npm 10 and Yarn 1. A pipeline step exchanges the job's ID token for a short-lived trove token and revokes it again as its last step.`;
    case 'npm.publish.gitlab-ci.native':
      return $localize`:@@setup.snippet.gitlab-native:For npm 11.5.1 and later, pnpm 11 and 12 and Yarn 4. The client exchanges the job's ID token itself.`;
    case 'dotnet.configure.shell':
    case 'dotnet.configure.powershell':
      return $localize`:@@setup.snippet.dotnet-add-source:Adds this repository as a package source of your user configuration. On Linux and macOS the .NET CLI cannot encrypt the password, so it is stored in clear text in that file.`;
    case 'dotnet.configure.nugetconfig':
    case 'nuget.configure.nugetconfig':
    case 'visual-studio.configure.nugetconfig':
      return $localize`:@@setup.snippet.nuget-config:Save this as nuget.config next to your solution. It removes all other package sources, so every package comes through this repository, and it reads the token from the environment variable.`;
    case 'nuget.configure.cmd':
    case 'nuget.configure.powershell':
      return $localize`:@@setup.snippet.nuget-sources-add:Adds this repository as a package source of your user configuration with nuget.exe.`;
    case 'dotnet.install':
      return $localize`:@@setup.snippet.dotnet-install:Adds the package to the project in the current directory and restores it through the configured sources.`;
    case 'nuget.install':
      return $localize`:@@setup.snippet.nuget-install:Installs the package into the current directory through this repository.`;
    case 'visual-studio.install.packagereference':
      return $localize`:@@setup.snippet.vs-packagereference:Add this line to the project file inside an ItemGroup, then restore. Or choose this source in the NuGet package manager of Visual Studio and install the package there.`;
    case 'dotnet.publish.shell':
    case 'dotnet.publish.powershell':
    case 'nuget.publish.cmd':
    case 'nuget.publish.powershell':
      return $localize`:@@setup.snippet.nuget-publish:Pushes the package to the repository that receives uploads. The token travels as the API key.`;
    case 'curl.configure.function':
      return $localize`:@@setup.snippet.curl-function:Paste this into the shell or the CI script that runs curl. It defines trove_curl, which hands curl the credential on standard input, so the token never appears on curl's command line.`;
    case 'curl.install':
      return $localize`:@@setup.snippet.curl-install:Downloads a file of this repository with trove_curl. Replace <path> with the path of the file in the repository.`;
    case 'curl.publish':
      return $localize`:@@setup.snippet.curl-publish:Uploads a file to the path you give. Replacing a file with other bytes also needs the overwrite permission.`;
    case 'docker.configure.shell':
    case 'podman.configure.shell':
      return $localize`:@@setup.snippet.oci-login:Signs the client in to this repository. A token is read from standard input, so it appears neither in the process list nor in the shell history; without a token the client asks for the password. The images of this repository begin with the address in the login.`;
    case 'docker.configure.kubernetes':
      return $localize`:@@setup.snippet.oci-pull-secret:For a Kubernetes cluster: creates an image pull secret that holds the token. A snippet without a token is only a comment, because a secret needs a credential. Name the secret under imagePullSecrets of the pods that pull images of this repository.`;
    case 'docker.install':
    case 'podman.install':
      return $localize`:@@setup.snippet.oci-pull:Pulls an image of this repository by its tag. Replace <package> with the image name. Use the digest instead of the tag to pull exactly these bytes.`;
    case 'docker.publish':
    case 'podman.publish':
      return $localize`:@@setup.snippet.oci-push:Names a local image below this repository and pushes it to the repository that receives uploads.`;
    case 'docker.publish.buildx':
      return $localize`:@@setup.snippet.oci-buildx:Builds the image for two platforms from the Dockerfile in the current directory and pushes the result as one multi-platform image.`;
    case 'apt.configure.shell':
    case 'apt.configure.shell-scoped':
      return $localize`:@@setup.snippet.apt-configure:Installs the repository's signing key as a key file and adds the repository as an APT source that is verified against that key. The credentials go into a file that only root can read, never into the source URL. The key file is replaced by running the key command again, which is also how a key rotation reaches this machine.`;
    case 'apt.configure.list':
    case 'apt.configure.list-scoped':
      return $localize`:@@setup.snippet.apt-list:The same source as one line, for systems that prefer a .list file. Install the key file as above first.`;
    case 'apt.install':
      return $localize`:@@setup.snippet.apt-install:Reads the repository's indexes and installs the package through APT. Replace the package name with yours.`;
    case 'apt.publish.curl':
      return $localize`:@@setup.snippet.apt-publish:Uploads a .deb file to the repository that receives uploads, into the suite and component named in the address. An index that lists the package follows within moments.`;
    default:
      return null;
  }
}
