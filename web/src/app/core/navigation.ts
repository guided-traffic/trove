import { Injectable, computed, inject } from '@angular/core';
import { SessionStore } from './session-store';

export interface NavItem {
  readonly route: string;
  readonly label: string;
  /** Icon font ligature (Q-UI-1); decorative, the label is the name. */
  readonly icon: string;
  /** Capability from `authz/me` that shows the entry (R-UI-002); none means always shown. */
  readonly capability?: string;
  /** Also shown to a caller that administers a project, whatever its capabilities (R-UI-041, Q-AUTHZ-181). */
  readonly projectAdmin?: boolean;
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
  readonly id: 'content' | 'admin';
  /** The title at the top of the sidebar while the area is shown. */
  readonly label: string;
  readonly entries: readonly NavEntry[];
}

/**
 * The information architecture of R-UI-002 (Q-UI-68, Q-UI-127): the areas **Artifacts** and
 * the **Admin area**, each with a sidebar of its own; the sidebar shows the area of the current route
 * (`/admin/…` is the admin area), and the top bar's Admin button leads into it. In the admin area,
 * single entries (repositories, endpoints) and folding groups (identity, access, system). Only the screens that exist in the current milestone are listed;
 * the capabilities are those of R-AUTHZ-010. The router and the API decide access, this only
 * hides entries (R-UI-005). A project administrator without any capability sees the Administration
 * area with the Projects entry only (R-UI-041). Packages, search and the other later screens are added by their
 * work packages.
 */
export const NAV_AREAS: readonly NavArea[] = [
  {
    id: 'content',
    label: $localize`:@@nav.group.content:Artifacts`,
    entries: [
      // >>> M2 ui-packages
      { kind: 'item', route: '/packages', icon: 'category', label: $localize`:@@nav.packages:Packages` },
      // <<< M2 ui-packages
      { kind: 'item', route: '/browse', icon: 'folder_open', label: $localize`:@@nav.browse:Browse` },
      { kind: 'item', route: '/guides', icon: 'menu_book', label: $localize`:@@nav.guides:Guides` },
      // >>> M4b vulnerabilities
      { kind: 'item', route: '/vulnerabilities', icon: 'security', label: $localize`:@@nav.vulnerabilities:Vulnerabilities` },
      // <<< M4b vulnerabilities
    ],
  },
  {
    id: 'admin',
    label: $localize`:@@nav.context.admin:Admin area`,
    entries: [
      { kind: 'item', route: '/admin/dashboard', icon: 'dashboard', label: $localize`:@@nav.dashboard:Dashboard`, capability: 'settings:read' },
      { kind: 'item', route: '/admin/projects', icon: 'folder_shared', label: $localize`:@@nav.projects:Projects`, capability: 'projects:read', projectAdmin: true },
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
        ],
      },
    ],
  },
];

/** The areas and entries the signed-in caller sees; empty groups and areas disappear. */
@Injectable({ providedIn: 'root' })
export class Navigation {
  private readonly session = inject(SessionStore);

  readonly areas = computed<readonly NavArea[]>(() => {
    const capabilities = this.session.capabilities();
    const administrator = this.session.hasAdminArea();
    const projectAdmin = this.session.isProjectOwner();
    const allowed = (i: Pick<NavItem, 'capability' | 'projectAdmin'>) =>
      !i.capability || capabilities.includes(i.capability) || (!!i.projectAdmin && projectAdmin);
    return NAV_AREAS.filter((a) => a.id === 'content' || administrator)
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

  /** The area a route belongs to: `/admin` and below is the admin area, everything else the content. */
  static areaOf(url: string): NavArea['id'] {
    return url === '/admin' || url.startsWith('/admin/') || url.startsWith('/admin?') ? 'admin' : 'content';
  }

  /** The entries of one area for the caller, or null when the caller has none of it. */
  area(id: NavArea['id']): NavArea | null {
    return this.areas().find((a) => a.id === id) ?? null;
  }
}
