import { REGISTERED_AUDIT_ACTIONS } from './audit-actions.fixture';
import { auditActionLabel } from './audit-labels';

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
});
