/** The translated label of an audit event name (R-UI-037); an unknown name is shown as it is. */
export function auditActionLabel(action: string): string {
  switch (action) {
    case 'ansible.hub.exchange_failed':
      return $localize`:@@audit.action.ansible.hub.exchange_failed:Ansible Hub token exchange failed`;
    case 'ansible.import_task.failed':
      return $localize`:@@audit.action.ansible.import_task.failed:Ansible collection import failed`;
    case 'ansible.tombstone.cleared':
      return $localize`:@@audit.action.ansible.tombstone.cleared:Ansible tombstone cleared`;
    case 'ansible.version.deleted':
      return $localize`:@@audit.action.ansible.version.deleted:Ansible collection version deleted`;
    case 'artifact.cached':
      return $localize`:@@audit.action.artifact.cached:File cached from the upstream`;
    case 'artifact.delete_stopped':
      return $localize`:@@audit.action.artifact.delete_stopped:Large delete stopped`;
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
    case 'auth.registry_token.denied':
      return $localize`:@@audit.action.auth.registry_token.denied:Registry token refused`;
    case 'auth.session.ended':
      return $localize`:@@audit.action.auth.session.ended:Session ended`;
    case 'authz.denied':
      return $localize`:@@audit.action.authz.denied:Request denied`;
    case 'authz.ownership.added':
      return $localize`:@@audit.action.authz.ownership.added:Package owner added`;
    case 'authz.ownership.claimed':
      return $localize`:@@audit.action.authz.ownership.claimed:Package name claimed`;
    case 'authz.ownership.released':
      return $localize`:@@audit.action.authz.ownership.released:Package name released`;
    case 'authz.ownership.removed':
      return $localize`:@@audit.action.authz.ownership.removed:Package owner removed`;
    case 'authz.policy.created':
      return $localize`:@@audit.action.authz.policy.created:Permission policy created`;
    case 'authz.policy.deleted':
      return $localize`:@@audit.action.authz.policy.deleted:Permission policy deleted`;
    case 'authz.policy.updated':
      return $localize`:@@audit.action.authz.policy.updated:Permission policy changed`;
    case 'authz.project.created':
      return $localize`:@@audit.action.authz.project.created:Project created`;
    case 'authz.project.deleted':
      return $localize`:@@audit.action.authz.project.deleted:Project deleted`;
    case 'authz.project.member_added':
      return $localize`:@@audit.action.authz.project.member_added:Project member added`;
    case 'authz.project.member_changed':
      return $localize`:@@audit.action.authz.project.member_changed:Project member's role changed`;
    case 'authz.project.member_removed':
      return $localize`:@@audit.action.authz.project.member_removed:Project member removed`;
    case 'authz.project.updated':
      return $localize`:@@audit.action.authz.project.updated:Project changed`;
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
    case 'bastion.contact':
      return $localize`:@@audit.action.bastion.contact:Bastion replica reported a change`;
    case 'bastion.created':
      return $localize`:@@audit.action.bastion.created:Bastion registered`;
    case 'bastion.deleted':
      return $localize`:@@audit.action.bastion.deleted:Bastion deleted`;
    case 'bastion.disabled':
      return $localize`:@@audit.action.bastion.disabled:Bastion disabled`;
    case 'bastion.downloads.dropped':
      return $localize`:@@audit.action.bastion.downloads.dropped:Bastion lost download records`;
    case 'bastion.enabled':
      return $localize`:@@audit.action.bastion.enabled:Bastion enabled`;
    case 'bastion.updated':
      return $localize`:@@audit.action.bastion.updated:Bastion changed`;
    case 'ci.exchange.denied':
      return $localize`:@@audit.action.ci.exchange.denied:CI token exchange refused`;
    case 'ci.exchange.repeated':
      return $localize`:@@audit.action.ci.exchange.repeated:CI token exchange repeated`;
    case 'ci.exchange.replayed':
      return $localize`:@@audit.action.ci.exchange.replayed:Reused CI job token refused`;
    case 'ci.exchange.succeeded':
      return $localize`:@@audit.action.ci.exchange.succeeded:CI token issued`;
    case 'ci.issuer.created':
      return $localize`:@@audit.action.ci.issuer.created:CI issuer created`;
    case 'ci.issuer.deleted':
      return $localize`:@@audit.action.ci.issuer.deleted:CI issuer deleted`;
    case 'ci.issuer.disabled':
      return $localize`:@@audit.action.ci.issuer.disabled:CI issuer disabled`;
    case 'ci.issuer.enabled':
      return $localize`:@@audit.action.ci.issuer.enabled:CI issuer enabled`;
    case 'ci.issuer.updated':
      return $localize`:@@audit.action.ci.issuer.updated:CI issuer changed`;
    case 'ci.job_token.lookup':
      return $localize`:@@audit.action.ci.job_token.lookup:CI job token checked with GitLab`;
    case 'ci.keys.refresh_failed':
      return $localize`:@@audit.action.ci.keys.refresh_failed:CI signing keys not refreshed`;
    case 'ci.keys.refreshed':
      return $localize`:@@audit.action.ci.keys.refreshed:CI signing keys refreshed`;
    case 'ci.mapping.created':
      return $localize`:@@audit.action.ci.mapping.created:CI mapping created`;
    case 'ci.mapping.deleted':
      return $localize`:@@audit.action.ci.mapping.deleted:CI mapping deleted`;
    case 'ci.mapping.disabled':
      return $localize`:@@audit.action.ci.mapping.disabled:CI mapping disabled`;
    case 'ci.mapping.enabled':
      return $localize`:@@audit.action.ci.mapping.enabled:CI mapping enabled`;
    case 'ci.mapping.updated':
      return $localize`:@@audit.action.ci.mapping.updated:CI mapping changed`;
    case 'ci.package_ownership.claimed':
      return $localize`:@@audit.action.ci.package_ownership.claimed:Package name claimed by a CI job`;
    case 'ci.package_ownership.conflict':
      return $localize`:@@audit.action.ci.package_ownership.conflict:CI job refused: the package has another owner`;
    case 'ci.token.revoked':
      return $localize`:@@audit.action.ci.token.revoked:CI token revoked`;
    case 'deb.coordinates.changed':
      return $localize`:@@audit.action.deb.coordinates.changed:Debian package coordinates changed`;
    case 'deb.coordinates.removed':
      return $localize`:@@audit.action.deb.coordinates.removed:Debian package coordinates removed`;
    case 'deb.package.deleted':
      return $localize`:@@audit.action.deb.package.deleted:Debian package deleted`;
    case 'deb.reinstate':
      return $localize`:@@audit.action.deb.reinstate:Withheld Debian package reinstated`;
    case 'deb.signing_key.created':
      return $localize`:@@audit.action.deb.signing_key.created:Debian signing key created`;
    case 'deb.signing_key.promoted':
      return $localize`:@@audit.action.deb.signing_key.promoted:Debian signing key promoted to primary`;
    case 'deb.signing_key.reinstated':
      return $localize`:@@audit.action.deb.signing_key.reinstated:Debian signing key reinstated`;
    case 'deb.signing_key.retired':
      return $localize`:@@audit.action.deb.signing_key.retired:Debian signing key retired`;
    case 'deb.upstream_signature_invalid':
      return $localize`:@@audit.action.deb.upstream_signature_invalid:Invalid signature of a Debian upstream`;
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
    case 'mirror.bound_reached':
      return $localize`:@@audit.action.mirror.bound_reached:Mirror reached its limit`;
    case 'mirror.pass_completed':
      return $localize`:@@audit.action.mirror.pass_completed:Mirror pass ended`;
    case 'mirror.pass_failed':
      return $localize`:@@audit.action.mirror.pass_failed:Mirror pass failed`;
    case 'mirror.pass_requested':
      return $localize`:@@audit.action.mirror.pass_requested:Mirror pass requested`;
    case 'mirror.rate_limited':
      return $localize`:@@audit.action.mirror.rate_limited:Mirror rate-limited by the upstream`;
    case 'npm.deprecate':
      return $localize`:@@audit.action.npm.deprecate:npm deprecation changed`;
    case 'npm.dist_tag.delete':
      return $localize`:@@audit.action.npm.dist_tag.delete:npm dist-tag removed`;
    case 'npm.dist_tag.set':
      return $localize`:@@audit.action.npm.dist_tag.set:npm dist-tag set`;
    case 'npm.publish':
      return $localize`:@@audit.action.npm.publish:npm package published`;
    case 'npm.publish.manifest_mismatch':
      return $localize`:@@audit.action.npm.publish.manifest_mismatch:npm publish refused: the manifest does not match`;
    case 'npm.reinstate':
      return $localize`:@@audit.action.npm.reinstate:Withheld npm version reinstated`;
    case 'npm.remote.version_removed':
      return $localize`:@@audit.action.npm.remote.version_removed:npm version removed upstream`;
    case 'npm.trusted_publishing.exchange':
      return $localize`:@@audit.action.npm.trusted_publishing.exchange:npm trusted publishing token exchange`;
    case 'npm.unpublish':
      return $localize`:@@audit.action.npm.unpublish:npm version unpublished`;
    case 'oci.manifest.deleted':
      return $localize`:@@audit.action.oci.manifest.deleted:Image manifest deleted`;
    case 'oci.reinstate':
      return $localize`:@@audit.action.oci.reinstate:Withheld image manifest reinstated`;
    case 'package.deleted':
      return $localize`:@@audit.action.package.deleted:Package version deleted`;
    case 'package.metadata_changed':
      return $localize`:@@audit.action.package.metadata_changed:Package metadata changed`;
    case 'package.published':
      return $localize`:@@audit.action.package.published:Package version published`;
    case 'proxy.egress_denied':
      return $localize`:@@audit.action.proxy.egress_denied:Upstream address blocked by the egress rules`;
    case 'proxy.integrity_mismatch':
      return $localize`:@@audit.action.proxy.integrity_mismatch:Upstream file failed its integrity check`;
    case 'proxy.tls_untrusted':
      return $localize`:@@audit.action.proxy.tls_untrusted:Upstream certificate not trusted`;
    case 'proxy.upstream_digest_changed':
      return $localize`:@@audit.action.proxy.upstream_digest_changed:Upstream changed a cached file's digest`;
    case 'proxy.upstream_gone':
      return $localize`:@@audit.action.proxy.upstream_gone:Upstream no longer has a file`;
    case 'repository.cache_invalidated':
      return $localize`:@@audit.action.repository.cache_invalidated:Repository cache invalidated`;
    case 'repository.claims_changed':
      return $localize`:@@audit.action.repository.claims_changed:Repository's package claims changed`;
    case 'repository.created':
      return $localize`:@@audit.action.repository.created:Repository created`;
    case 'repository.deleted':
      return $localize`:@@audit.action.repository.deleted:Repository deleted`;
    case 'repository.disabled':
      return $localize`:@@audit.action.repository.disabled:Repository disabled`;
    case 'repository.moved':
      return $localize`:@@audit.action.repository.moved:Repository moved to another project`;
    case 'repository.moved_in':
      return $localize`:@@audit.action.repository.moved_in:Repository moved into this project`;
    case 'repository.narrowed':
      return $localize`:@@audit.action.repository.narrowed:Repository settings narrowed`;
    case 'repository.remote_package_shadowed':
      return $localize`:@@audit.action.repository.remote_package_shadowed:Local package shadows a remote one`;
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
    case 'system.backup.completed':
      return $localize`:@@audit.action.system.backup.completed:Backup completed`;
    case 'system.backup.deleted':
      return $localize`:@@audit.action.system.backup.deleted:Backup deleted`;
    case 'system.backup.failed':
      return $localize`:@@audit.action.system.backup.failed:Backup failed`;
    case 'system.backup.target_adopted':
      return $localize`:@@audit.action.system.backup.target_adopted:Backup target taken over`;
    case 'system.banner_changed':
      return $localize`:@@audit.action.system.banner_changed:Banner changed`;
    case 'system.deletions_paused':
      return $localize`:@@audit.action.system.deletions_paused:Deletions paused`;
    case 'system.deletions_resumed':
      return $localize`:@@audit.action.system.deletions_resumed:Deletions resumed`;
    case 'system.job.cancelled':
      return $localize`:@@audit.action.system.job.cancelled:Job cancelled`;
    case 'system.maintenance_mode_changed':
      return $localize`:@@audit.action.system.maintenance_mode_changed:Maintenance mode changed`;
    case 'system.restore.journal_replayed':
      return $localize`:@@audit.action.system.restore.journal_replayed:Revocation journal replayed after a restore`;
    case 'system.restored':
      return $localize`:@@audit.action.system.restored:Installation restored`;
    case 'system.storage.versions_restored':
      return $localize`:@@audit.action.system.storage.versions_restored:Deleted storage objects restored`;
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
    case 'user.avatar.removed':
      return $localize`:@@audit.action.user.avatar.removed:Avatar removed`;
    case 'user.avatar.set':
      return $localize`:@@audit.action.user.avatar.set:Avatar uploaded`;
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
    case 'vulnerability.advisory.kind_changed':
      return $localize`:@@audit.action.vulnerability.advisory.kind_changed:Advisory kind changed`;
    case 'vulnerability.claimed_name_match':
      return $localize`:@@audit.action.vulnerability.claimed_name_match:Advisory matches a claimed package name`;
    case 'vulnerability.download_blocked':
      return $localize`:@@audit.action.vulnerability.download_blocked:Download blocked by a finding`;
    case 'vulnerability.finding.reopened':
      return $localize`:@@audit.action.vulnerability.finding.reopened:Finding reopened`;
    case 'vulnerability.finding.status_changed':
      return $localize`:@@audit.action.vulnerability.finding.status_changed:Finding status changed`;
    case 'vulnerability.source.failed':
      return $localize`:@@audit.action.vulnerability.source.failed:Advisory sync failed`;
    case 'vulnerability.source.imported':
      return $localize`:@@audit.action.vulnerability.source.imported:Advisory data imported`;
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
