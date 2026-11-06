# trove — working rules for agents

## Stage: pre-alpha, no backward compatibility

trove is pre-alpha. It has no users and no running installations (owner, 2026-11-06).

Until the first stable release there is **no backward compatibility**, by the owner's rule:

- Change an API, a URL, a configuration key, the database schema or a stored format the way that is technically best.
  Breaking changes are expected; nobody's client, script or installation has to keep working.
- Do not add what only an older version or an existing client would need: no deprecations, aliases, fallbacks, shims,
  version switches, two-step rollouts, data migration paths or "keep the old field for now".
- Do not argue for a design with "clients do not notice" or "existing installations keep working". Those are not costs
  before the first stable release.
- A breaking change still updates the spec, the documentation and the tests in the same change.

The rule ends with the first stable release; from then on reliability and compatibility come first.
