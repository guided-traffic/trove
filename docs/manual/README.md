# trove manual

The manual for the people who use trove and the people who run it. The binary embeds these pages and the UI shows
them under **Help** (the "?" in the top bar), so the manual of an installation always matches the version it runs.
Readers on GitHub can browse the same files here.

| Section | Directory | For |
|---|---|---|
| User guide | [user/](user/) | Everyone who reads, installs or publishes packages and files |
| Administrator guide | [admin/](admin/) | Everyone who installs, configures and operates trove |

## User guide

1. [Getting started](user/getting-started.md)
2. [Repositories](user/repositories.md)
3. [Packages](user/packages.md)
4. [Vulnerabilities](user/vulnerabilities.md)
5. [Browsing and transferring files](user/files.md)
6. [Connecting a package client](user/client-setup.md)
7. [Personal access tokens](user/access-tokens.md)
8. [GitLab CI pipelines](user/gitlab-ci-pipelines.md)
9. [Errors and troubleshooting](user/troubleshooting.md)

## Administrator guide

1. [How trove runs](admin/overview.md)
2. [Installation](admin/installation.md)
3. [Configuration](admin/configuration.md)
4. [TLS and certificates](admin/tls-and-certificates.md)
5. [Endpoints and exposure rules](admin/endpoints.md)
6. [The trove Bastion](admin/bastion.md)
7. [Sign-in and identities](admin/sign-in-and-identity.md)
8. [Permissions](admin/permissions.md)
9. [Projects and delegated administration](admin/projects.md)
10. [Managing tokens](admin/tokens.md)
11. [GitLab CI federation](admin/gitlab-ci.md)
12. [Managing repositories](admin/repositories.md)
13. [Monitoring and operations](admin/operations.md)
14. [Upgrades](admin/upgrades.md)
15. [Known limitations](admin/limitations.md)

The order of these two lists is the order of the manual in the UI; a test in
[internal/manual](../../internal/manual/) fails when this page and the embedded contents disagree.

## Writing a page

The pages are rendered on the server by the README renderer (goldmark with GitHub-flavoured Markdown, then the
sanitizer allowlist) and shown in the UI. That sets a few rules:

- **Title and summary.** A page starts with one `# Title` line, then one paragraph of plain text (no links, no code)
  that says what the page is for. The UI shows the title as the page heading and in the contents, and the summary on
  the overview. Below that, use `##` and `###` headings only.
- **Links between pages** are relative Markdown links to the `.md` file, with an optional fragment in GitHub's
  heading-slug form: `[tokens](access-tokens.md#create-a-token)`, `[endpoints](../admin/endpoints.md)`. The server
  turns them into links of the help area; a link to a page or heading that does not exist fails the manual's test.
- **No links into the rest of the repository** (specification, developer pages), because the UI does not serve them.
  The exceptions are the exhaustive configuration and Helm reference in the root README and the release notes, linked
  by their absolute GitHub addresses. Links to upstream documentation (npm, Helm, PostgreSQL, RFCs) are absolute.
- **No images and no raw HTML** other than `<details>` and `<summary>` for long reference blocks.
- **English**, present tense, second person for instructions. UI labels in bold as the English UI shows them
  (**Administration › Endpoints**). Values in configuration snippets are marked `# default` or `# example`.
- **Only what is built.** Every statement is checked against the code of this version. A feature that does not exist
  yet is not described as if it did; [Known limitations](admin/limitations.md) lists what is missing.
