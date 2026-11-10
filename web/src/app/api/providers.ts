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
import { HttpProjectsApi, ProjectsApi } from './projects-api';
import { DraftPolicyExplainer, PolicyExplainer } from './policy-explainer';
import { HttpSessionsApi, SessionsApi } from './sessions-api';
import { HttpTokensApi, TokensApi } from './tokens-api';
// <<< M1 ui-b
// >>> M2 ui-packages
import { ClientSetupApi, HttpClientSetupApi, HttpPackagesApi, PackagesApi } from './packages-api';
import { GuidesApi, HttpGuidesApi } from './guides-api';
import { HttpManualApi, ManualApi } from './manual-api';
// <<< M2 ui-packages
// >>> M4 ci
import { CiApi, HttpCiApi } from './ci-api';
// <<< M4 ci
// >>> M5b bastion
import { BastionsApi, HttpBastionsApi } from './bastions-api';
// <<< M5b bastion
// >>> M4b vulnerabilities
import { HttpVulnerabilitiesApi, VulnerabilitiesApi } from './vulnerabilities-api';
// <<< M4b vulnerabilities
// >>> G2 backups
import { BackupsApi, HttpBackupsApi } from './backups-api';
// <<< G2 backups
// >>> F0 ansible
import { AnsibleApi, HttpAnsibleApi } from './ansible-api';
// <<< F0 ansible
// >>> T17 deb
import { DebApi, HttpDebApi } from './deb-api';
// <<< T17 deb

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
    { provide: ProjectsApi, useClass: HttpProjectsApi },
    { provide: PolicyExplainer, useClass: DraftPolicyExplainer },
    // <<< M1 ui-b
    // >>> M1 ui-a
    { provide: RepositoriesApi, useClass: HttpRepositoriesApi },
    { provide: FilesApi, useClass: HttpFilesApi },
    { provide: EndpointsApi, useClass: HttpEndpointsApi },
    { provide: AuditApi, useClass: HttpAuditApi },
    { provide: JobsApi, useClass: HttpJobsApi },
    // <<< M1 ui-a
    // >>> M2 ui-packages
    { provide: PackagesApi, useClass: HttpPackagesApi },
    { provide: ClientSetupApi, useClass: HttpClientSetupApi },
    { provide: GuidesApi, useClass: HttpGuidesApi },
    { provide: ManualApi, useClass: HttpManualApi },
    // <<< M2 ui-packages
    // >>> M4 ci
    { provide: CiApi, useClass: HttpCiApi },
    // <<< M4 ci
    // >>> M5b bastion
    { provide: BastionsApi, useClass: HttpBastionsApi },
    // <<< M5b bastion
    // >>> M4b vulnerabilities
    { provide: VulnerabilitiesApi, useClass: HttpVulnerabilitiesApi },
    // <<< M4b vulnerabilities
    // >>> G2 backups
    { provide: BackupsApi, useClass: HttpBackupsApi },
    // <<< G2 backups
    // >>> F0 ansible
    { provide: AnsibleApi, useClass: HttpAnsibleApi },
    // <<< F0 ansible
    // >>> T17 deb
    { provide: DebApi, useClass: HttpDebApi },
    // <<< T17 deb
  ];
}
