# Security policy

## Reporting a vulnerability

Please do not report security vulnerabilities through public issues, discussions or pull requests.

Report them privately through GitHub's
[private vulnerability reporting](https://docs.github.com/en/code-security/security-advisories/guidance-on-reporting-and-writing-information-about-vulnerabilities/privately-reporting-a-security-vulnerability).
Open the repository's **Security** tab and choose **Report a vulnerability**. Include:

- the affected component, document or version (a commit hash while nothing is released);
- what an attacker gains, and under which preconditions;
- steps or a proof of concept that reproduce it;
- whether you plan to publish, and when.

The report stays visible only to you and the maintainers until an advisory is published. We
coordinate the fix and the disclosure with you and credit you in the advisory unless you prefer
otherwise.

trove is in its specification phase. A flaw in the specified design under
[docs/spec/](docs/spec/README.md), for example a token, permission or federation flow that can be
abused as written, is a valid report. Please send design flaws through the same private channel.

## Supported versions

No version has been released. There is nothing to patch yet. Fixes land in the specification and,
once code exists, on the `main` branch.

| Version | Supported |
|---|---|
| none released | — |

## Known limitations

Gaps that are already known are documented as `H-<n>` items, each next to the mechanism that has
it. Start at [docs/security/](docs/security/README.md). An already documented item is not a new
report, unless you find a way to exploit it beyond what the item describes.
