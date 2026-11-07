# Security dashboard

The security dashboard is the instance-wide view of the vulnerability findings, in the admin area. It shows the counts and the trend over every project, finds a finding by advisory identifier, package or project, lists the findings with the project of each, and lets the people who may triage change their status.

The **Vulnerabilities** screen in the content area is the same report for the repositories you read; this page covers what differs. For what a finding is and what the triage statuses mean, see [Vulnerabilities](../user/vulnerabilities.md).

## Who sees it

**Security** in the admin area's side bar opens `/admin/security`. It is shown to everyone who holds one of two capabilities:

| Capability | Held by | What it allows on the dashboard |
|---|---|---|
| `content:metadata-read` | `auditor`, `admin` | Reads the findings of every project, whether or not you are a member of it |
| `vulnerabilities:triage` | `auditor`, `security-admin`, `admin` | Changes the status of a finding: confirm, dismiss, block, lift a block |

A holder of `content:metadata-read` without `vulnerabilities:triage` reads; the selection boxes, **Change status** and the status buttons of a finding are absent, and the dashboard says so. A holder of `vulnerabilities:triage` alone triages only the findings of the repositories that holder may read, as in the content area: the capability does not widen what is readable. The roles `auditor`, `security-admin` and `admin` also hold `settings:read`, so **Admin** in the top bar opens the [admin dashboard](dashboard.md) for them; **Security** is the entry below it in the side bar. A caller whose token carries only one of the two capabilities (a scoped token) lands on the security dashboard.

## What it shows

**The counts and the banner** are those of the content report (**Critical** to **Unknown**, **Malicious**, **Needs triage**, **Blocked versions**; the banner for stale or never-imported advisory data), counted over every project you may read. Each count is a link that applies its filter and keeps the project you narrowed to.

**The trend** is the chart of open findings per day by severity over 30, 90 or 365 days, with **Show as table**.

**The list** has the columns of the content report and, before **Repository**, a **Project** column. A project key in a row narrows the list to that project. The identifier of a row opens the finding, **Advisory sources** the sources screen of the installation, and **Export CSV** downloads the rows of the current filter with a `project` column; the findings stay inside the dashboard.

## Searching

The search row above the counts has three fields; each one set narrows the counts, the trend and the list together, and each appears as a chip.

| Field | Finds | Notes |
|---|---|---|
| **Advisory ID** | The findings of an advisory by its identifier or by any alias | `CVE-2021-23337` finds the finding whose advisory is `GHSA-35jh-r3h4-6jhm`, because the GitHub advisory names that CVE as an alias. The identifier is written in its usual spelling as you type: `cve-2021-23337` is sent as `CVE-2021-23337`, a `GHSA-` identifier keeps its lower-case groups |
| **Package starts with** | Findings of packages whose normalized name starts with the text | `lodash` finds `lodash` and `lodash.merge`. The exact package name and the version are in the filter panel under **Package** |
| **Project key** | The findings of the repositories of that project | A key that does not exist, or a project you may not read, gives an empty list, not an error |

Everything else of the content report's filter panel (severity, status, kind, repository, format, flags, detection dates, text) is available as well, and all of it is part of the address. A search by advisory identifier does not scan every finding: it starts at the advisory and reaches its findings through an index, so it answers as fast on a large installation as on a small one. The **Text** filter in the panel is a substring search over package, identifier and summary and is slower on very large installations; use the three fields when you know what you look for.

## Triage

With `vulnerabilities:triage`, tick findings (**Select all loaded findings** ticks the loaded rows) and choose **Change status** to confirm, dismiss, or block several at once; the dialog reports one result per finding. A single finding is changed on its own screen, where the history is. Blocking a finding refuses the download of that version to every client of its repository and of the virtual repositories that contain it, in whichever project it lives; the dialog says so before it acts. Every change is audited with your name.

## Limits

- The dashboard lists findings; it does not show a project's repositories or members. Open the project from **Projects**.
- The package link of a row opens the package page in the content area.
- Counting is per request and per caller; nothing is cached across callers, so a figure can differ between two people who read different projects.
