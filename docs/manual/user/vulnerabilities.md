# Vulnerabilities

The Vulnerabilities screen shows which stored package versions are affected by a known security advisory or a known malicious package, how serious each finding is, and what has been decided about it. People with the triage permission confirm, dismiss or block findings here. This page explains the report, a finding, the triage actions and the package page.

## What trove checks

trove keeps a copy of public advisory data for the package formats it serves (npm and NuGet) and compares it with the versions stored in your repositories. It compares names and versions only; it does not open the files. A match is a **finding**: one advisory affects one stored version in one repository. A finding is created when a version is published or fetched into a repository, and again when the advisory data changes.

The report shows the findings of the repositories you may read, and only those. A repository you may not read contributes no row and no count.

## The report

Select **Vulnerabilities** in the side bar.

**The counts.** The top of the screen counts the open findings by severity (**Critical**, **High**, **Medium**, **Low**, **Unknown**), then **Malicious**, **Needs triage** and **Blocked versions**. An open finding is one that needs triage, is confirmed, or is blocked. Each count is a link: it opens the list below with that filter applied. A line under the counts says how many of your repositories are scanned, hold only internal names, wait for advisory data or are not covered.

**The banner.** A banner appears when the advisory data of an ecosystem is stale, has never been imported completely, or its synchronisation keeps failing. Until data has been imported, the versions of that ecosystem are not evaluated. If you may read the settings, the banner links to the advisory sources.

**The trend.** The chart shows the open findings per day by severity over **30 days**, **90 days** or **365 days**. **Show as table** replaces the chart with the same numbers in a table. A finding counts under its current severity, and a dismissed finding counts until it is resolved.

**The list.** Each row is one finding.

| Column | Meaning |
|---|---|
| **Severity** | The level as a word with an icon, and the score when the advisory has one. The colour only reinforces the word |
| **Status** | **Needs triage**, **Confirmed**, **Blocked**, **Dismissed** or **Resolved**. A version whose download is refused shows as **Blocked**, with the reason: **malicious package** or **blocked in triage** |
| **Identifier** | The advisory identifier, which opens the finding. **Malicious** marks a package reported as malicious; **Known exploited** marks an advisory on the CISA list of exploited vulnerabilities |
| **Package and version** | Opens the package page on its **Vulnerabilities** tab |
| **Repository** | The repository that holds the version |
| **Fix available** | The versions that fix the advisory |
| **Detected** | When trove found it |

Select a heading to sort by severity, status, identifier, package or detection time. The list opens sorted by severity, worst first. **Load more** adds the next page.

**Filters.** **Filters** opens a panel with severity, status, kind, repository, format, package name and version, advisory (an identifier or an alias such as a CVE number), known exploited, fix available, blocks downloads, block reason, detection dates and a text search. Applied filters appear as chips; the cross on a chip removes one filter. Filters, sort and the trend period are part of the address, so a link or a reload keeps them. To see one repository, filter by it, or use **Vulnerabilities** in the repository list.

**Export CSV.** Downloads the findings of the current filter as a CSV file, with the same rows as the list. An export that would be larger than the installation allows is refused with a message; narrow the filters and try again.

## A finding

Select an identifier to open the finding. It shows the status, the severity and score, the package and version, the repository, the virtual repositories that expose it, the versions that fix it, when it was detected, and its history. Below that is the advisory itself: aliases, CVSS vector, weaknesses, affected ranges, references and the source with its license.

The advisory text comes from a public source and is untrusted, so trove shows it as plain text with its line breaks and never as formatted or clickable content. References are links only for addresses that start with http or https; they open another site in a new tab. **Where else is it used?** opens the advisory page, which lists every finding of the advisory that you may read, across repositories and versions.

When the download of the version is refused, a banner at the top says so: **Downloads of the package@version are blocked in the repository**, with the reason. A block for a malicious package is lifted by dismissing the finding. A block set in triage shows who set it, when, and their comment.

## Triage

Triage needs the permission `vulnerabilities:triage`. Without it the actions, the selection column and the bulk action are not shown, and the API refuses the request anyway. The roles admin, auditor and security-admin hold the permission, and you may only triage findings you may read.

| The finding is | Actions |
|---|---|
| **Needs triage** | **Confirm**, **Dismiss** |
| **Confirmed** | **Block downloads**, **Dismiss**, **Mark as needs triage** |
| **Blocked** | **Lift block** (back to confirmed), **Dismiss** |
| **Dismissed** | **Confirm**, **Mark as needs triage** |
| **Resolved** | None. trove resolves a finding when the version is no longer affected, the advisory is withdrawn, the version is removed or scanning is switched off, and detects it again if it returns |

Every action opens a dialog. **Dismiss** asks for a reason (**Not exploitable**, **False positive**, **Risk accepted**, **Mitigated elsewhere** or **Test code only**). You can add a comment of up to 4,000 characters; it stays in the history of the finding.

**Block downloads** is set on a confirmed finding only. Before it is set, the dialog names the repository: every client of that repository, and of the virtual repositories that contain it, is refused the version with a 403 answer until the block is lifted. The block applies to that repository only; the same version in another repository has a finding of its own. A dismissed finding never blocks.

If someone else changed the finding while you had it open, the change is refused with **Changed by someone else since you opened it**; the page then shows the current state and you can act again.

**Several findings at once.** With the permission, the list has a selection column. Select rows, or **Select all loaded findings**, then **Change status**. Choose the status, a reason for **Dismissed** and a comment. The dialog then reports one result per finding: **Changed**, **Unchanged**, or the reason a finding was refused, for example that a block needs a confirmed finding. The other findings are changed regardless.

## On the package page

The **Vulnerabilities** tab of a package version lists, per repository you may read that holds the version, whether it was evaluated, whether its download is blocked and why, and its findings with severity, identifier, status and fixed versions. The tab label carries the number of open findings. **No known vulnerabilities.** means the version was evaluated and nothing matched; a repository with internal names only, or one still waiting for advisory data, says so instead.

**All versions** shows the highest open severity of each version as a word. The header of the page reads **Blocked in** and the repository when the selected version is blocked there.

## Advisory sources

If you may read the settings, **Advisory sources** at the top of the report opens the table of sources: per source and ecosystem its mode, whether its data is current or stale, when it was last synchronised, the failed runs in a row with the last error, the number of records, rejected records and ranges trove cannot compare, and the next run. With the permission to change settings you can start a synchronisation from there.

A source in the mode **Mirror repository** reads its files from a generic local repository instead of the network, for installations without internet access. The screen shows the commands that download each file on a connected machine and upload it to that repository. Replace the placeholder with the key of the mirror repository.
