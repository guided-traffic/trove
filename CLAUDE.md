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

## Stage: CI and releases until the first stable release

By the owner's decisions of 2026-11-10 ([releasing.md](docs/developer/releasing.md#until-the-first-stable-release)):

- Only a minimal set of jobs gates Semantic Release (Q-ARCH-457). `integration`, `s3-conformance`, every `e2e` leg and the two
  coverage jobs that need them still run on every push and hold nothing back.
- Never wait for or re-run one of those to get a release out. A red one is a finding. Run the tests that matter for a change locally.
- Tests run before a release. Nothing runs against published artifacts (Q-HA-281): a failure there would come too late.
- Job and step names say what they do: no requirement codes such as `(E0.1)`, no tool versions, no "on kind". An e2e job is `e2e (<set>)`.
- An e2e set is cut by what it tests, not by feature: what a format does belongs to that format's set.

The full gate returns with the first stable release.
