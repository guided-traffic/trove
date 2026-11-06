import { ProjectRole } from '../../api/projects-api';

/** The word for a project role (Q-AUTHZ-208); the glossary fixes "Owner", "Maintainer", "Developer" and "Reader". */
export function projectRoleLabel(role: ProjectRole): string {
  switch (role) {
    case 'reader':
      return $localize`:@@project-role.reader:Reader`;
    case 'developer':
      return $localize`:@@project-role.developer:Developer`;
    case 'maintainer':
      return $localize`:@@project-role.maintainer:Maintainer`;
    case 'owner':
      return $localize`:@@project-role.owner:Owner`;
  }
}

/** What a role holds, for the hint beside the role choice (Q-AUTHZ-208). */
export function projectRoleHint(role: ProjectRole): string {
  switch (role) {
    case 'reader':
      return $localize`:@@project-role.reader-hint:Reads every repository of the project.`;
    case 'developer':
      return $localize`:@@project-role.developer-hint:Also annotates and deploys.`;
    case 'maintainer':
      return $localize`:@@project-role.maintainer-hint:Every action on the repositories, also overwrite and delete.`;
    case 'owner':
      return $localize`:@@project-role.owner-hint:Also administers the project: members, repositories, policies and service accounts.`;
  }
}
