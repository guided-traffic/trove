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
- Do not hold a release for one of those. A red pipeline is still yours to work on: find the cause and fix it, also after the release is out (owner, 2026-11-10). Run the tests that matter for a change locally.
- Tests run before a release. Nothing runs against published artifacts (Q-HA-281): a failure there would come too late.
- Job and step names say what they do: no requirement codes such as `(E0.1)`, no tool versions, no "on kind". An e2e job is `e2e (<set>)`.
- An e2e set is cut by what it tests, not by feature: what a format does belongs to that format's set.

The full gate returns with the first stable release.

## Product: simple by default, details on demand

By the owner's decisions of 2026-11-12 (T-21 in [promt.md](docs/projektplan/promt.md#owner-task-of-2026-11-12)). trove is for people who
want to keep and fetch their artifacts, not for people who want to configure a repository manager. "Keep it simple, for every
repository type, but allow detailed settings."

- **The plain path first.** The standard flow of every repository, for every format and every class, gives a plain user a simple way
  to manage their artifacts: only what that repository needs to work. Access is the members and the visibility level (private,
  internal, public); nothing else about access belongs in the standard flow.
- **Details on demand.** A setting that makes sense but that only an expert needs (include and exclude patterns, disabling,
  hiding existence, administrator notes, mirror and protocol tuning) stays, in a detail view or behind an advanced-settings
  disclosure. It is never a step of the creation flow or part of the main form.
- **Remote repositories:** the connection can be tested before the repository is saved, where the upstream is entered.
  The well-known public upstreams of the format are offered as one-click choices; for OCI (Docker) these are Docker Hub, Quay,
  GitHub (ghcr.io) and GitLab.
- **A default is a value in the field**, editable, not a hint beside an empty field.
- **A field is as wide as its expected input.** A number of seconds needs no room for billions.
- **A title names the thing:** "New Ansible repository", "New OCI repository", "New NuGet repository", not "New repository".
- **Every UI change is rendered and looked at before it lands** (rule 11 of [promt.md](docs/projektplan/promt.md)): both schemes, wide and
  narrow, realistic data. The renders are never committed.
