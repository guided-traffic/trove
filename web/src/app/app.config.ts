import { ApplicationConfig, provideBrowserGlobalErrorListeners } from '@angular/core';
import {
  provideHttpClient,
  withFetch,
  withInterceptors,
  withXsrfConfiguration,
} from '@angular/common/http';
import { provideRouter, TitleStrategy, withComponentInputBinding } from '@angular/router';
import { routes } from './app.routes';
import { provideApiServices } from './api/providers';
import {
  conventionsInterceptor,
  problemInterceptor,
  resilienceInterceptor,
} from './core/http/interceptors';
import { TroveTitleStrategy } from './core/title-strategy';

/** Cookie and header of the CSRF protection for cookie-authenticated requests (R-AUTH-012). */
export const CSRF_COOKIE = '__Host-trv_csrf';
export const CSRF_HEADER = 'X-CSRF-Token';

/**
 * Root providers. Zoneless change detection is the Angular 22 default, so no provider names it.
 * Interceptor order, outermost first: problem mapping and session consequences, the one
 * automatic retry, then the request conventions (epoch, idempotency key). CSRF comes from
 * Angular's XSRF support, which adds the header to unsafe same-origin requests.
 */
export const appConfig: ApplicationConfig = {
  providers: [
    provideBrowserGlobalErrorListeners(),
    ...provideApiServices(),
    provideRouter(routes, withComponentInputBinding()),
    { provide: TitleStrategy, useExisting: TroveTitleStrategy },
    provideHttpClient(
      withFetch(),
      withXsrfConfiguration({ cookieName: CSRF_COOKIE, headerName: CSRF_HEADER }),
      withInterceptors([problemInterceptor, resilienceInterceptor, conventionsInterceptor]),
    ),
  ],
};
