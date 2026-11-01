import { TestBed } from '@angular/core/testing';
import { DraftPolicyExplainer, ExplainDraft } from './policy-explainer';
import { IdentityApi } from './identity-api';

const draft = {
  input: { entries: [{ principal: 'authenticated', actions: ['read'] }] },
  covers: ['npm-internal'],
  invalid: false,
} as unknown as ExplainDraft;

describe('DraftPolicyExplainer (R-UI-027, R-AUTHZ-030)', () => {
  beforeEach(() => {
    TestBed.resetTestingModule();
    TestBed.configureTestingModule({
      providers: [
        DraftPolicyExplainer,
        { provide: IdentityApi, useValue: { user: async () => ({ value: { groups: [] } }) } },
      ],
    });
  });
  const ask = (principal: string) =>
    TestBed.inject(DraftPolicyExplainer).explain({ principal, repository: 'npm-internal', action: 'read' }, draft);

  it('R-AUTHZ-030 the built-in principal authenticated applies to a user', async () => {
    expect((await ask('user:alice')).decision).toBe('allowed');
  });

  it('R-AUTHZ-030 the built-in principal authenticated does not apply to a service account (as on the server)', async () => {
    expect((await ask('sa:ci-bot')).decision).toBe('denied');
  });

  it('R-AUTHZ-030 nor to a CI mapping or anonymous', async () => {
    expect((await ask('ci:acme/web')).decision).toBe('denied');
    expect((await ask('anonymous')).decision).toBe('denied');
  });
});
