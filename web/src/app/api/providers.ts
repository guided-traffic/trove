import { Provider } from '@angular/core';
import { HttpLoginApi, LoginApi } from './login-api';
import { HttpPreferencesApi, PreferencesApi } from './preferences-api';
import { HttpSessionApi, SessionApi } from './session-api';
// >>> M1 ui-a
import { AuditApi, HttpAuditApi } from './audit-api';
import { EndpointsApi, HttpEndpointsApi } from './endpoints-api';
import { FilesApi, HttpFilesApi } from './files-api';
import { HttpJobsApi, JobsApi } from './jobs-api';
import { HttpRepositoriesApi, RepositoriesApi } from './repositories-api';
// <<< M1 ui-a
import { EncryptionApi, HttpEncryptionApi, HttpSystemApi, SystemApi } from './system-api';
// >>> M1 ui-b
import { AuthSettingsApi, HttpAuthSettingsApi } from './auth-settings-api';
import { HttpPolicyApi, HttpRolesApi, PolicyApi, RolesApi } from './authz-api';
import { HttpIdentityApi, IdentityApi } from './identity-api';
import { DraftPolicyExplainer, PolicyExplainer } from './policy-explainer';
import { HttpSessionsApi, SessionsApi } from './sessions-api';
import { HttpTokensApi, TokensApi } from './tokens-api';
// <<< M1 ui-b

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
    // >>> M1 ui-b
    { provide: IdentityApi, useClass: HttpIdentityApi },
    { provide: TokensApi, useClass: HttpTokensApi },
    { provide: SessionsApi, useClass: HttpSessionsApi },
    { provide: AuthSettingsApi, useClass: HttpAuthSettingsApi },
    { provide: PolicyApi, useClass: HttpPolicyApi },
    { provide: RolesApi, useClass: HttpRolesApi },
    { provide: PolicyExplainer, useClass: DraftPolicyExplainer },
    // <<< M1 ui-b
    // >>> M1 ui-a
    { provide: RepositoriesApi, useClass: HttpRepositoriesApi },
    { provide: FilesApi, useClass: HttpFilesApi },
    { provide: EndpointsApi, useClass: HttpEndpointsApi },
    { provide: AuditApi, useClass: HttpAuditApi },
    { provide: JobsApi, useClass: HttpJobsApi },
    // <<< M1 ui-a
  ];
}
