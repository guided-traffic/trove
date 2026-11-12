import { REGISTERED_AUDIT_ACTIONS } from './audit-actions.fixture';
import { actionSuggestions, auditActionLabel } from './audit-labels';

describe('Audit action names', () => {
  // The fixture is the audit registry itself (internal/audit), written and kept current by a Go test: a new event type fails it
  // until the fixture is regenerated, and then fails here until the type has a name.
  it('R-UI-037 every event type of the audit registry has a plain name of its own', () => {
    expect(REGISTERED_AUDIT_ACTIONS.length).toBeGreaterThan(100);
    const unnamed = REGISTERED_AUDIT_ACTIONS.filter((action) => auditActionLabel(action) === action);
    expect(unnamed).toEqual([]);
    const names = REGISTERED_AUDIT_ACTIONS.map(auditActionLabel);
    const shared = names.filter((name, i) => names.indexOf(name) !== i);
    expect(shared).toEqual([]);
  });

  it('R-UI-037 the action filter suggests every prefix of the registry at every level, then every type with its name', () => {
    const suggested = actionSuggestions();
    const values = suggested.map((s) => s.value);
    const firstSegments = new Set(REGISTERED_AUDIT_ACTIONS.map((a) => `${a.split('.')[0]}.*`));
    for (const prefix of firstSegments) expect(values).toContain(prefix);
    for (const prefix of ['ci.*', 'ci.issuer.*', 'vulnerability.*', 'vulnerability.finding.*', 'system.backup.*', 'npm.*', 'token.*']) {
      expect(values).toContain(prefix);
    }
    // A prefix is a proper one: `npm.publish.*` covers npm.publish.manifest_mismatch, and no type is offered as its own prefix.
    expect(values).not.toContain('npm.publish.manifest_mismatch.*');
    for (const action of REGISTERED_AUDIT_ACTIONS) {
      expect(suggested.find((s) => s.value === action)?.label).toBe(auditActionLabel(action));
    }
    expect(suggested.filter((s) => s.value.endsWith('.*')).every((s) => s.label === null)).toBe(true);
    expect(new Set(values).size).toBe(values.length);
  });
});
