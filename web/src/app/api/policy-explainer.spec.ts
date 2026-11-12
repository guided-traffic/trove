import { TestBed } from '@angular/core/testing';
import { DraftPolicyExplainer, ExplainDraft } from './policy-explainer';
import { IdentityApi } from './identity-api';

const draft = {
  input: { entries: [{ principal: 'group:team-web', actions: ['read'] }] },
  covers: ['npm-internal'],
  invalid: false,
} as unknown as ExplainDraft;

describe('DraftPolicyExplainer (R-UI-027, R-AUTHZ-033)', () => {
  beforeEach(() => {
    TestBed.resetTestingModule();
    TestBed.configureTestingModule({
      providers: [
        DraftPolicyExplainer,
        {
          provide: IdentityApi,
          useValue: {
            user: async (name: string) => ({ value: { groups: name === 'alice' ? [{ name: 'team-web', source: 'manual' }] : [] } }),
          },
        },
      ],
    });
  });
  const ask = (principal: string) =>
    TestBed.inject(DraftPolicyExplainer).explain({ principal, repository: 'npm-internal', action: 'read' }, draft);

  it('R-AUTHZ-033 an entry for a group applies to a user in the group', async () => {
    expect((await ask('user:alice')).decision).toBe('allowed');
  });

  it('R-AUTHZ-033 it does not apply to a user outside the group, and no entry reaches everyone: a level does that', async () => {
    expect((await ask('user:bob')).decision).toBe('denied');
  });

  it('R-AUTHZ-033 nor to a service account, a CI mapping or anonymous, which no group holds', async () => {
    expect((await ask('sa:ci-bot')).decision).toBe('denied');
    expect((await ask('ci:acme/web')).decision).toBe('denied');
    expect((await ask('anonymous')).decision).toBe('denied');
  });
});
