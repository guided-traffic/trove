import { Injectable, computed, inject } from '@angular/core';
import { SessionStore } from './session-store';

export interface NavItem {
  readonly route: string;
  readonly label: string;
  /** Capability from `authz/me` that shows the entry (R-UI-002); none means always shown. */
  readonly capability?: string;
}

export interface NavGroup {
  readonly id: 'content' | 'admin';
  readonly label: string;
  readonly items: readonly NavItem[];
}

/**
 * The information architecture of R-UI-002: **Packages and files** and **Administration**. Only
 * the screens that exist in the current milestone are listed; the capabilities are those of
 * R-AUTHZ-010. The router and the API decide access, this only hides entries (R-UI-005).
 */
export const NAV_GROUPS: readonly NavGroup[] = [
  {
    id: 'content',
    label: $localize`:@@nav.group.content:Packages and files`,
    items: [{ route: '/browse', label: $localize`:@@nav.browse:Browse` }],
  },
  {
    id: 'admin',
    label: $localize`:@@nav.group.admin:Administration`,
    items: [
      { route: '/admin/repositories', label: $localize`:@@nav.repositories:Repositories`, capability: 'repositories:read-config' },
      { route: '/admin/users', label: $localize`:@@nav.users:Users`, capability: 'identities:read' },
      { route: '/admin/groups', label: $localize`:@@nav.groups:Groups`, capability: 'identities:read' },
      { route: '/admin/service-accounts', label: $localize`:@@nav.service-accounts:Service accounts`, capability: 'identities:read' },
      { route: '/admin/permissions', label: $localize`:@@nav.permissions:Permission policies`, capability: 'policies:read' },
      { route: '/admin/roles', label: $localize`:@@nav.roles:System roles`, capability: 'roles:read' },
      { route: '/admin/tokens', label: $localize`:@@nav.tokens:Tokens`, capability: 'tokens:read' },
      { route: '/admin/endpoints', label: $localize`:@@nav.endpoints:Endpoints`, capability: 'endpoints:read' },
      { route: '/admin/audit', label: $localize`:@@nav.audit:Audit log`, capability: 'audit:read' },
      { route: '/admin/system', label: $localize`:@@nav.system:System status`, capability: 'settings:read' },
      { route: '/admin/settings/auth', label: $localize`:@@nav.settings:Settings`, capability: 'settings:read' },
    ],
  },
];

/** The groups and entries the signed-in caller sees. */
@Injectable({ providedIn: 'root' })
export class Navigation {
  private readonly session = inject(SessionStore);

  readonly groups = computed<readonly NavGroup[]>(() => {
    const capabilities = this.session.capabilities();
    const administrator = this.session.isAdministrator();
    return NAV_GROUPS.filter((g) => g.id === 'content' || administrator)
      .map((g) => ({
        ...g,
        items: g.items.filter((i) => !i.capability || capabilities.includes(i.capability)),
      }))
      .filter((g) => g.items.length > 0);
  });
}
