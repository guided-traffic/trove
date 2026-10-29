import { Provider } from '@angular/core';
import { HttpLoginApi, LoginApi } from './login-api';
import { HttpPreferencesApi, PreferencesApi } from './preferences-api';
import { HttpSessionApi, SessionApi } from './session-api';
import { EncryptionApi, HttpEncryptionApi, HttpSystemApi, SystemApi } from './system-api';

/**
 * Binds every service interface of `api/` to its HTTP implementation. Specs and component tests
 * replace the bindings with the in-memory fakes of `testing/fakes.ts` (`provideFakeApis`).
 */
export function provideApiServices(): Provider[] {
  return [
    { provide: SessionApi, useClass: HttpSessionApi },
    { provide: LoginApi, useClass: HttpLoginApi },
    { provide: PreferencesApi, useClass: HttpPreferencesApi },
    { provide: SystemApi, useClass: HttpSystemApi },
    { provide: EncryptionApi, useClass: HttpEncryptionApi },
  ];
}
