/** The translated label of an audit event name (R-UI-037); an unknown name is shown as it is. */
export function auditActionLabel(action: string): string {
  switch (action) {
    case 'artifact.deleted':
      return $localize`:@@audit.action.artifact.deleted:File or folder deleted`;
    case 'artifact.deployed':
      return $localize`:@@audit.action.artifact.deployed:File deployed`;
    case 'artifact.downloaded':
      return $localize`:@@audit.action.artifact.downloaded:File downloaded`;
    case 'audit.exported':
      return $localize`:@@audit.action.audit.exported:Audit log exported`;
    case 'auth.breakglass.locked':
      return $localize`:@@audit.action.auth.breakglass.locked:Break-glass account locked`;
    case 'auth.breakglass.login':
      return $localize`:@@audit.action.auth.breakglass.login:Break-glass sign-in`;
    case 'auth.breakglass.unlocked':
      return $localize`:@@audit.action.auth.breakglass.unlocked:Break-glass account unlocked`;
    case 'auth.credential.rejected':
      return $localize`:@@audit.action.auth.credential.rejected:Credential rejected`;
    case 'auth.csrf_failed':
      return $localize`:@@audit.action.auth.csrf_failed:Request refused by the forgery protection`;
    case 'auth.login.failed':
      return $localize`:@@audit.action.auth.login.failed:Sign-in failed`;
    case 'auth.login.succeeded':
      return $localize`:@@audit.action.auth.login.succeeded:Signed in`;
    case 'auth.logout':
      return $localize`:@@audit.action.auth.logout:Signed out`;
    case 'auth.session.ended':
      return $localize`:@@audit.action.auth.session.ended:Session ended`;
    case 'authz.denied':
      return $localize`:@@audit.action.authz.denied:Request denied`;
    case 'authz.policy.created':
      return $localize`:@@audit.action.authz.policy.created:Permission policy created`;
    case 'authz.policy.deleted':
      return $localize`:@@audit.action.authz.policy.deleted:Permission policy deleted`;
    case 'authz.policy.updated':
      return $localize`:@@audit.action.authz.policy.updated:Permission policy changed`;
    case 'authz.read.allowed':
      return $localize`:@@audit.action.authz.read.allowed:Read allowed`;
    case 'authz.role.bound':
      return $localize`:@@audit.action.authz.role.bound:System role granted`;
    case 'authz.role.unbound':
      return $localize`:@@audit.action.authz.role.unbound:System role withdrawn`;
    case 'authz.settings.changed':
      return $localize`:@@audit.action.authz.settings.changed:Access setting of a repository changed`;
    case 'authz.write.allowed':
      return $localize`:@@audit.action.authz.write.allowed:Write allowed`;
    case 'endpoint.exposure.created':
      return $localize`:@@audit.action.endpoint.exposure.created:Exposure rule created`;
    case 'endpoint.exposure.deleted':
      return $localize`:@@audit.action.endpoint.exposure.deleted:Exposure rule deleted`;
    case 'endpoint.exposure.updated':
      return $localize`:@@audit.action.endpoint.exposure.updated:Exposure rule changed`;
    case 'group.created':
      return $localize`:@@audit.action.group.created:Group created`;
    case 'group.deleted':
      return $localize`:@@audit.action.group.deleted:Group deleted`;
    case 'group.members.changed':
      return $localize`:@@audit.action.group.members.changed:Group members changed`;
    case 'group.updated':
      return $localize`:@@audit.action.group.updated:Group changed`;
    case 'repository.created':
      return $localize`:@@audit.action.repository.created:Repository created`;
    case 'repository.deleted':
      return $localize`:@@audit.action.repository.deleted:Repository deleted`;
    case 'repository.updated':
      return $localize`:@@audit.action.repository.updated:Repository changed`;
    case 'serviceaccount.created':
      return $localize`:@@audit.action.serviceaccount.created:Service account created`;
    case 'serviceaccount.deleted':
      return $localize`:@@audit.action.serviceaccount.deleted:Service account deleted`;
    case 'serviceaccount.disabled':
      return $localize`:@@audit.action.serviceaccount.disabled:Service account disabled`;
    case 'serviceaccount.updated':
      return $localize`:@@audit.action.serviceaccount.updated:Service account changed`;
    case 'settings.auth.changed':
      return $localize`:@@audit.action.settings.auth.changed:Authentication settings changed`;
    case 'system.job.cancelled':
      return $localize`:@@audit.action.system.job.cancelled:Job cancelled`;
    case 'system.task_triggered':
      return $localize`:@@audit.action.system.task_triggered:Maintenance task started`;
    case 'token.created':
      return $localize`:@@audit.action.token.created:Token created`;
    case 'token.expiring':
      return $localize`:@@audit.action.token.expiring:Token about to expire`;
    case 'token.rejected':
      return $localize`:@@audit.action.token.rejected:Token rejected`;
    case 'token.revoked':
      return $localize`:@@audit.action.token.revoked:Token revoked`;
    case 'token.rotated':
      return $localize`:@@audit.action.token.rotated:Token rotated`;
    case 'user.created':
      return $localize`:@@audit.action.user.created:User created`;
    case 'user.deleted':
      return $localize`:@@audit.action.user.deleted:User deleted`;
    case 'user.disabled':
      return $localize`:@@audit.action.user.disabled:User disabled`;
    case 'user.dormancy_warning':
      return $localize`:@@audit.action.user.dormancy_warning:User close to dormancy`;
    case 'user.enabled':
      return $localize`:@@audit.action.user.enabled:User enabled`;
    case 'user.groups.claim_missing':
      return $localize`:@@audit.action.user.groups.claim_missing:Group claim missing at sign-in`;
    case 'user.groups.synced':
      return $localize`:@@audit.action.user.groups.synced:Group memberships synchronised`;
    case 'user.rename_conflict':
      return $localize`:@@audit.action.user.rename_conflict:User rename conflict`;
    case 'user.renamed':
      return $localize`:@@audit.action.user.renamed:User renamed`;
    case 'user.updated':
      return $localize`:@@audit.action.user.updated:User changed`;
    default:
      return action;
  }
}

/** The event names the action filter suggests. */
export const KNOWN_ACTIONS: readonly string[] = [
  'artifact.*',
  'auth.*',
  'authz.*',
  'endpoint.*',
  'group.*',
  'repository.*',
  'serviceaccount.*',
  'system.*',
  'token.*',
  'user.*',
];

export function categoryLabel(category: string): string {
  switch (category) {
    case 'auth':
      return $localize`:@@audit.category.auth:Authentication`;
    case 'token':
      return $localize`:@@audit.category.token:Tokens`;
    case 'identity':
      return $localize`:@@audit.category.identity:Identities`;
    case 'authz':
      return $localize`:@@audit.category.authz:Authorization`;
    case 'repository':
      return $localize`:@@audit.category.repository:Repositories`;
    case 'content':
      return $localize`:@@audit.category.content:Content`;
    case 'system':
      return $localize`:@@audit.category.system:System`;
    case 'config':
      return $localize`:@@audit.category.config:Configuration`;
    case 'webhook':
      return $localize`:@@audit.category.webhook:Webhooks`;
    default:
      return category;
  }
}

export function outcomeLabel(outcome: string): string {
  switch (outcome) {
    case 'success':
      return $localize`:@@audit.outcome.success:success`;
    case 'failure':
      return $localize`:@@audit.outcome.failure:failure`;
    case 'denied':
      return $localize`:@@audit.outcome.denied:denied`;
    default:
      return outcome;
  }
}
