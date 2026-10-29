import { User } from '../../api/identity-api';

/** The origin of a user as the screens spell it (R-UI-024). */
export function originLabel(origin: User['origin']): string {
  switch (origin) {
    case 'jit':
      return $localize`:@@user.origin.jit:Identity provider`;
    case 'admin':
      return $localize`:@@user.origin.admin:Created by an administrator`;
    case 'scim':
      return $localize`:@@user.origin.scim:SCIM`;
    default:
      return $localize`:@@user.origin.system:Break-glass`;
  }
}

/** True while the break-glass lockout of a user is in force (R-AUTH-038). */
export function isLocked(user: Pick<User, 'lockedUntil'>, now: number = Date.now()): boolean {
  return user.lockedUntil !== null && Date.parse(user.lockedUntil) > now;
}
