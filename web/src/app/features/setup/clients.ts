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
 * no setup screen yet and is not offered. The NuGet clients arrive with the NuGet format.
 */
export const FORMAT_CLIENTS: Readonly<Record<string, readonly ClientChoice[]>> = {
  npm: [
    { id: 'npm', label: 'npm' },
    { id: 'pnpm', label: 'pnpm' },
    { id: 'yarn', label: 'Yarn Berry (2 and later)' },
    { id: 'yarn-classic', label: 'Yarn Classic (1.x)' },
  ],
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
    default:
      return null;
  }
}
