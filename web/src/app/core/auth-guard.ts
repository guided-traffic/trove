import { inject } from '@angular/core';
import { CanActivateFn, Router, RouterStateSnapshot } from '@angular/router';
import { LoginApi } from '../api/login-api';
import { SessionStore } from './session-store';

/**
 * Shapes navigation only; the API decides (R-UI-005). A signed-in session passes. Without a
 * session the guard asks `login-options`: with anonymous access the application runs anonymously
 * (R-UI-004), otherwise it goes to `/login?returnTo=<route>`. If the session could not be
 * determined at all (the server is down) the route is entered and the shell shows the failure.
 */
export const authGuard: CanActivateFn = async (_route, state: RouterStateSnapshot) => {
  const session = inject(SessionStore);
  const router = inject(Router);
  const login = inject(LoginApi);

  await session.load();
  switch (session.state()) {
    case 'authenticated':
    case 'failed':
      return true;
    default: {
      try {
        if ((await login.options()).anonymousAccess) return true;
      } catch {
        // No login options: send the user to the sign-in screen, which reports the failure.
      }
      return router.createUrlTree(['/login'], { queryParams: { returnTo: state.url } });
    }
  }
};
