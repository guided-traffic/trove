import { Injectable, computed, inject } from '@angular/core';
import { ProjectContext, projectKeyOf } from './project-context';
import { SessionStore } from './session-store';

export interface NavItem {
  readonly route: string;
  readonly label: string;
  /** Icon font ligature (Q-UI-1); decorative, the label is the name. */
  readonly icon: string;
  /** Capability from `authz/me` that shows the entry (R-UI-002); none means always shown. */
  readonly capability?: string;
  /** Instead of `capability`: the entry is shown to a caller who holds any one of these. */
  readonly anyCapability?: readonly string[];
}

/** A section of the sidebar with nested entries that can be folded. */
export interface NavGroup {
  readonly id: string;
  readonly label: string;
  readonly icon: string;
  readonly items: readonly NavItem[];
}

export type NavEntry = ({ readonly kind: 'item' } & NavItem) | ({ readonly kind: 'group' } & NavGroup);

export interface NavArea {
  readonly id: 'home' | 'project' | 'admin';
  /** The title at the top of the sidebar while the area is shown. */
  readonly label: string;
  readonly entries: readonly NavEntry[];
}

/**
 * The information architecture of R-UI-002 (Q-UI-68, Q-UI-127, Q-UI-129): three sidebars. The **home** sidebar belongs to
 * every screen outside a project (the start page of your projects, the guides, the profile); the **project** sidebar
 * belongs to `/projects/<key>/…` and is built for that key (see {@link projectEntries}); the **admin** sidebar belongs to
 * `/admin/…` behind the top bar's Admin button. In the admin area, single entries (repositories, endpoints) and folding
 * groups (identity, access, system). Only the screens that exist in the current milestone are listed; the capabilities
 * are those of R-AUTHZ-010. The router and the API decide access, this only hides entries (R-UI-005).
 */
export const NAV_AREAS: readonly NavArea[] = [
  {
    id: 'home',
    label: $localize`:@@nav.context.home:Your work`,
    entries: [
      { kind: 'item', route: '/projects', icon: 'folder_shared', label: $localize`:@@nav.projects:Projects` },
      { kind: 'item', route: '/guides', icon: 'menu_book', label: $localize`:@@nav.guides:Guides` },
    ],
  },
  {
    id: 'admin',
    label: $localize`:@@nav.context.admin:Admin area`,
    entries: [
      { kind: 'item', route: '/admin/dashboard', icon: 'dashboard', label: $localize`:@@nav.dashboard:Dashboard`, capability: 'settings:read' },
      { kind: 'item', route: '/admin/projects', icon: 'folder_shared', label: $localize`:@@nav.projects:Projects`, capability: 'projects:read' },
      // >>> T-9 security dashboard
      { kind: 'item', route: '/admin/security', icon: 'security', label: $localize`:@@nav.security:Security`, anyCapability: ['vulnerabilities:triage', 'content:metadata-read'] },
      // <<< T-9 security dashboard
      { kind: 'item', route: '/admin/repositories', icon: 'inventory_2', label: $localize`:@@nav.repositories:Repositories`, capability: 'repositories:read-config' },
      { kind: 'item', route: '/admin/endpoints', icon: 'router', label: $localize`:@@nav.endpoints:Endpoints`, capability: 'endpoints:read' },
      // >>> M5b bastion
      { kind: 'item', route: '/admin/bastions', icon: 'public', label: $localize`:@@nav.bastions:Bastions`, capability: 'endpoints:read' },
      // <<< M5b bastion
      {
        kind: 'group',
        id: 'identity',
        icon: 'groups',
        label: $localize`:@@nav.section.identity:Identity`,
        items: [
          { route: '/admin/users', icon: 'person', label: $localize`:@@nav.users:Users`, capability: 'identities:read' },
          { route: '/admin/groups', icon: 'group', label: $localize`:@@nav.groups:Groups`, capability: 'identities:read' },
          { route: '/admin/service-accounts', icon: 'smart_toy', label: $localize`:@@nav.service-accounts:Service accounts`, capability: 'identities:read' },
          { route: '/admin/tokens', icon: 'key', label: $localize`:@@nav.tokens:Tokens`, capability: 'tokens:read' },
          // >>> M4 ci
          { route: '/admin/ci', icon: 'rocket_launch', label: $localize`:@@nav.ci:CI identity`, capability: 'ci:read' },
          // <<< M4 ci
        ],
      },
      {
        kind: 'group',
        id: 'access',
        icon: 'shield',
        label: $localize`:@@nav.section.access:Access`,
        items: [
          { route: '/admin/permissions', icon: 'policy', label: $localize`:@@nav.permissions:Permission policies`, capability: 'policies:read' },
          { route: '/admin/roles', icon: 'admin_panel_settings', label: $localize`:@@nav.roles:System roles`, capability: 'roles:read' },
        ],
      },
      {
        kind: 'group',
        id: 'system',
        icon: 'dns',
        label: $localize`:@@nav.section.system:System`,
        items: [
          { route: '/admin/system', icon: 'speed', label: $localize`:@@nav.system:System status`, capability: 'settings:read' },
          { route: '/admin/audit', icon: 'history', label: $localize`:@@nav.audit:Audit log`, capability: 'audit:read' },
          { route: '/admin/settings/auth', icon: 'tune', label: $localize`:@@nav.settings:Settings`, capability: 'settings:read' },
          // >>> M4b vulnerabilities
          { route: '/admin/advisory-sources', icon: 'rss_feed', label: $localize`:@@nav.advisory-sources:Advisory sources`, capability: 'settings:read' },
          // <<< M4b vulnerabilities
        ],
      },
    ],
  },
];

/**
 * The entries of the sidebar of project `key` (Q-UI-129). Packages, browse, repositories, vulnerabilities and members are
 * for every caller who sees the project (the API decides what each shows); the policies, service accounts and CI
 * identity are for the capability holders and the project's Owners, as their endpoints are (R-AUTHZ-026); the settings
 * are read by every member and changed by the Owner and `projects:write`.
 */
export function projectEntries(key: string, session: SessionStore): readonly NavEntry[] {
  const at = (path: string): string => `/projects/${encodeURIComponent(key)}/${path}`;
  const manages = (capability: string): boolean => session.has(capability) || session.isOwnerOf(key);
  const access: NavItem[] = [
    { route: at('members'), icon: 'group', label: $localize`:@@nav.project.members:Members` },
    ...(manages('policies:read')
      ? [{ route: at('policies'), icon: 'policy', label: $localize`:@@nav.permissions:Permission policies` }]
      : []),
    ...(manages('identities:read')
      ? [{ route: at('service-accounts'), icon: 'smart_toy', label: $localize`:@@nav.service-accounts:Service accounts` }]
      : []),
    // >>> M4 ci
    ...(manages('ci:read') ? [{ route: at('ci'), icon: 'rocket_launch', label: $localize`:@@nav.ci:CI identity` }] : []),
    // <<< M4 ci
  ];
  return [
    // >>> M2 ui-packages
    { kind: 'item', route: at('packages'), icon: 'category', label: $localize`:@@nav.packages:Packages` },
    // <<< M2 ui-packages
    { kind: 'item', route: at('browse'), icon: 'folder_open', label: $localize`:@@nav.browse:Browse` },
    { kind: 'item', route: at('repositories'), icon: 'inventory_2', label: $localize`:@@nav.repositories:Repositories` },
    // >>> M4b vulnerabilities
    { kind: 'item', route: at('vulnerabilities'), icon: 'security', label: $localize`:@@nav.vulnerabilities:Vulnerabilities` },
    // <<< M4b vulnerabilities
    {
      kind: 'group',
      id: 'project-access',
      icon: 'shield',
      label: $localize`:@@nav.section.project-access:Access`,
      items: access,
    },
    { kind: 'item', route: at('settings'), icon: 'settings', label: $localize`:@@nav.project.settings:Settings` },
  ];
}

/** The areas and entries the signed-in caller sees; empty groups and areas disappear. */
@Injectable({ providedIn: 'root' })
export class Navigation {
  private readonly session = inject(SessionStore);
  private readonly context = inject(ProjectContext);

  /** The fixed areas: the home sidebar, and the admin sidebar for a caller with capabilities. */
  readonly areas = computed<readonly NavArea[]>(() => {
    const capabilities = this.session.capabilities();
    const administrator = this.session.hasAdminArea();
    const allowed = (i: Pick<NavItem, 'capability' | 'anyCapability'>) =>
      (!i.capability && !i.anyCapability) ||
      (!!i.capability && capabilities.includes(i.capability)) ||
      (!!i.anyCapability && i.anyCapability.some((c) => capabilities.includes(c)));
    return NAV_AREAS.filter((a) => a.id === 'home' || administrator)
      .map((a): NavArea => ({
        ...a,
        entries: a.entries.flatMap((e): NavEntry[] => {
          if (e.kind === 'item') return allowed(e) ? [e] : [];
          const items = e.items.filter(allowed);
          return items.length > 0 ? [{ ...e, items }] : [];
        }),
      }))
      .filter((a) => a.entries.length > 0);
  });

  /** The sidebar of the project the URL is in, or null outside a project. */
  readonly projectArea = computed<NavArea | null>(() => {
    const key = this.context.key();
    if (key === null) return null;
    const entries = projectEntries(key, this.session).flatMap((e): NavEntry[] =>
      e.kind === 'item' || e.items.length > 0 ? [e] : [],
    );
    return { id: 'project', label: this.context.current()?.displayName || key, entries };
  });

  /** The area a route belongs to: `/admin` and below is the admin area, `/projects/<key>` and below a project. */
  static areaOf(url: string): NavArea['id'] {
    if (url === '/admin' || url.startsWith('/admin/') || url.startsWith('/admin?')) return 'admin';
    return projectKeyOf(url) === null ? 'home' : 'project';
  }

  /** The entries of one area for the caller, or null when the caller has none of it. */
  area(id: NavArea['id']): NavArea | null {
    return id === 'project' ? this.projectArea() : (this.areas().find((a) => a.id === id) ?? null);
  }
}
