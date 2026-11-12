/** A page of the manual (R-API-054): `/help/<section>/<page>`. */
export interface HelpTopic {
  readonly section: 'user' | 'admin';
  readonly page: string;
}

/**
 * The manual page that explains a screen (R-UI-050, Q-UI-90): the "?" of the top bar opens it, and
 * the overview of the help area where no entry matches. The longest matching route prefix wins.
 * `web/scripts/check-rules.mjs` fails the lint when a page named here is not a file of
 * `docs/manual/<section>/`.
 */
export const HELP_TOPICS: readonly (readonly [route: string, topic: HelpTopic])[] = [
  // The screens of a project (`/projects/<key>/…`, Q-UI-129) are looked up as `/project/…`, whatever the key.
  ['/project', { section: 'admin', page: 'projects' }],
  ['/project/packages', { section: 'user', page: 'packages' }],
  ['/project/browse', { section: 'user', page: 'files' }],
  ['/project/repositories', { section: 'user', page: 'repositories' }],
  // >>> M4b vulnerabilities
  ['/project/vulnerabilities', { section: 'user', page: 'vulnerabilities' }],
  // <<< M4b vulnerabilities
  ['/project/policies', { section: 'admin', page: 'permissions' }],
  ['/project/service-accounts', { section: 'admin', page: 'sign-in-and-identity' }],
  // >>> M4 ci
  ['/project/ci', { section: 'admin', page: 'gitlab-ci' }],
  // <<< M4 ci
  ['/projects', { section: 'admin', page: 'projects' }],
  ['/search', { section: 'user', page: 'search' }],
  ['/guides', { section: 'user', page: 'client-setup' }],
  ['/setup', { section: 'user', page: 'client-setup' }],
  ['/profile/tokens', { section: 'user', page: 'access-tokens' }],
  ['/admin/projects', { section: 'admin', page: 'projects' }],
  ['/admin/repositories', { section: 'admin', page: 'repositories' }],
  ['/admin/endpoints', { section: 'admin', page: 'endpoints' }],
  // >>> M5b bastion
  ['/admin/bastions', { section: 'admin', page: 'bastion' }],
  // <<< M5b bastion
  ['/admin/users', { section: 'admin', page: 'sign-in-and-identity' }],
  ['/admin/groups', { section: 'admin', page: 'sign-in-and-identity' }],
  ['/admin/service-accounts', { section: 'admin', page: 'sign-in-and-identity' }],
  ['/admin/settings', { section: 'admin', page: 'sign-in-and-identity' }],
  ['/admin/settings/general', { section: 'admin', page: 'operations' }],
  ['/admin/tokens', { section: 'admin', page: 'tokens' }],
  // >>> M4 ci
  ['/admin/ci', { section: 'admin', page: 'gitlab-ci' }],
  // <<< M4 ci
  ['/admin/permissions', { section: 'admin', page: 'permissions' }],
  ['/admin/roles', { section: 'admin', page: 'permissions' }],
  ['/admin/dashboard', { section: 'admin', page: 'dashboard' }],
  ['/admin/security', { section: 'admin', page: 'security-dashboard' }],
  ['/admin/system', { section: 'admin', page: 'operations' }],
  ['/admin/system/backups', { section: 'admin', page: 'backup-and-restore' }],
  ['/admin/audit', { section: 'admin', page: 'operations' }],
  // >>> M4b vulnerabilities
  ['/admin/advisory-sources', { section: 'admin', page: 'vulnerability-sources' }],
  // <<< M4b vulnerabilities
];

/** The router commands of the help for a URL: the matching page, or the overview. */
export function helpFor(url: string): readonly string[] {
  const raw = url.split(/[?#]/, 1)[0] ?? '';
  const inProject = /^\/projects\/[^/]+(\/.*)?$/.exec(raw);
  const path = inProject ? `/project${inProject[1] ?? ''}` : raw;
  let best: readonly [string, HelpTopic] | undefined;
  for (const entry of HELP_TOPICS) {
    const [route] = entry;
    if ((path === route || path.startsWith(route + '/')) && route.length > (best?.[0].length ?? 0)) best = entry;
  }
  return best ? ['/help', best[1].section, best[1].page] : ['/help'];
}
