import { Injectable, inject } from '@angular/core';
import { IdentityApi } from './identity-api';
import { PermissionPolicyInput } from './authz-api';

/*
 * "Why can X do Y" for the policy editor's preview panel (R-UI-027). The explain operation of
 * R-AUTHZ-024 / R-API-034 is P1 and not built (Q-AUTHZ-27), so the preview answers from what exists:
 * the draft itself, the keys `validate-policy` says the draft covers, the groups of a user, and the
 * caller's own `authz/check`. The screen codes against `PolicyExplainer`; when the explain
 * operation lands, an implementation that calls it replaces `DraftPolicyExplainer` in
 * `providers.ts` and the panel needs no change. The draft answer does not evaluate the other
 * policies, token scopes, exposure rules or ownership, and says so in `scope`.
 */

/** The question: may `principal` do `action` on `repository`, optionally on one package or path. */
export interface ExplainQuestion {
  /** A principal reference: `user:alice`, `group:team-web`, `sa:ci-bot`, `anonymous`, `authenticated`. */
  readonly principal: string;
  readonly repository: string;
  readonly action: string;
  readonly package?: string;
  readonly path?: string;
}

/** One step of the answer, in evaluation order. */
export interface ExplainStep {
  readonly outcome: 'pass' | 'fail' | 'info';
  readonly text: string;
}

export interface Explanation {
  readonly decision: 'allowed' | 'denied';
  readonly steps: readonly ExplainStep[];
  /** What was evaluated: the unsaved draft only, or the stored policies of the installation. */
  readonly scope: 'draft' | 'installation';
}

/** What the preview knows about the draft: its content and the repositories it covers now. */
export interface ExplainDraft {
  readonly input: PermissionPolicyInput;
  readonly covers: readonly string[];
  /** The draft has validation errors; its coverage is unknown. */
  readonly invalid: boolean;
}

export abstract class PolicyExplainer {
  abstract explain(question: ExplainQuestion, draft: ExplainDraft): Promise<Explanation>;
}

@Injectable()
export class DraftPolicyExplainer extends PolicyExplainer {
  private readonly identity = inject(IdentityApi);

  override async explain(q: ExplainQuestion, draft: ExplainDraft): Promise<Explanation> {
    const steps: ExplainStep[] = [];
    const deny = (): Explanation => ({ decision: 'denied', steps, scope: 'draft' });

    if (draft.invalid) {
      steps.push({
        outcome: 'fail',
        text: $localize`:@@explain.invalid:The draft has errors, so it covers nothing yet. Fix them first.`,
      });
      return deny();
    }
    if (!draft.covers.includes(q.repository)) {
      steps.push({
        outcome: 'fail',
        text: $localize`:@@explain.not-covered:The draft's repository selectors do not cover ${q.repository}:repository:.`,
      });
      return deny();
    }
    steps.push({
      outcome: 'pass',
      text: $localize`:@@explain.covered:The draft's repository selectors cover ${q.repository}:repository:.`,
    });

    const refs = await this.references(q.principal, steps);
    const matching = (draft.input.entries ?? []).filter((e) => refs.some((r) => r.ref === e.principal));
    if (matching.length === 0) {
      steps.push({
        outcome: 'fail',
        text: $localize`:@@explain.no-entry:The draft has no entry for ${q.principal}:principal: or for a group or class it belongs to.`,
      });
      return deny();
    }
    const entry = matching.find((e) => expandPresets(e.actions).includes(q.action));
    if (!entry) {
      steps.push({
        outcome: 'fail',
        text: $localize`:@@explain.no-action:The entries that apply to ${q.principal}:principal: do not include the action ${q.action}:action:.`,
      });
      return deny();
    }
    steps.push({
      outcome: 'pass',
      text:
        entry.principal === q.principal
          ? $localize`:@@explain.entry:The draft has an entry for ${entry.principal}:principal: that includes the action ${q.action}:action:.`
          : $localize`:@@explain.entry-via:The draft has an entry for ${entry.principal}:entry: that includes the action ${q.action}:action: and applies to ${q.principal}:principal: through its group or class.`,
    });
    steps.push({
      outcome: 'info',
      text: $localize`:@@explain.limits:Not evaluated here: other policies, the token scope, the endpoint's exposure rules and package ownership.`,
    });
    return { decision: 'allowed', steps, scope: 'draft' };
  }

  /**
   * The references an entry can name to apply to `principal`: itself, `authenticated` for any
   * signed-in principal, and for a user the groups of its record (needs `identities:read`).
   */
  private async references(
    principal: string,
    steps: ExplainStep[],
  ): Promise<{ ref: string }[]> {
    const refs: { ref: string }[] = [{ ref: principal }];
    if (principal !== 'anonymous') {
      refs.push({ ref: 'authenticated' });
    }
    if (principal.startsWith('user:')) {
      try {
        const user = await this.identity.user(principal.slice('user:'.length));
        for (const g of user.groups) refs.push({ ref: `group:${g.name}` });
      } catch {
        steps.push({
          outcome: 'info',
          text: $localize`:@@explain.groups-unknown:The groups of ${principal}:principal: could not be read, so entries for groups are not considered.`,
        });
      }
    }
    return refs;
  }
}

function expandPresets(actions: readonly string[]): string[] {
  const presets: Record<string, string[]> = {
    read: ['read'],
    write: ['read', 'annotate', 'deploy'],
    maintain: ['read', 'annotate', 'deploy', 'overwrite', 'delete'],
    full: ['read', 'annotate', 'deploy', 'overwrite', 'delete', 'manage'],
  };
  return actions.flatMap((a) => presets[a] ?? [a]);
}
