# Scanning untrusted container images

Status: built ([R-VULN-033 to R-VULN-037](../spec/vulnerability-scanning.md#images), [developer page](../developer/vulnerabilities.md)). The perspective of this page is what an attacker who can push an image, or influence a database, can do to the worker that analyses it.

## The boundary

An image is whatever its pusher made it, and the analyser is a large parser of formats it did not choose. A remote repository brings the images of anyone the upstream serves. So the design treats the analyser as code that can be made to misbehave, and puts the limits outside it.

```
 object store ──bytes──▶ worker job ──checked layout──▶ analyser process ──report──▶ worker job ──▶ PostgreSQL
                         digest, size, decompression,    its own directory,          bounded, decoded,
                         entries, path traversal         empty environment, killable  text cleaned
```

What the worker enforces before the analyser sees a byte, and how, is the table of [R-VULN-036](../spec/vulnerability-scanning.md#r-vuln-036--scanning-untrusted-layers): the compressed size from the manifest, the digest and the size of every blob as it is copied, and one pass over every layer that decompresses it and walks its tar. That pass is what ends a decompression bomb, an image of many millions of entries and a `..` in a tar name: it runs inside the worker's own memory and time, in code this repository owns and tests, not in the analyser.

The analyser is not given access to the object store or a credential of the worker: it runs with an empty environment (no database address, no bucket key, no token), in a directory that the job created for this one scan, against an OCI layout the job wrote. It has no database connection, and its only output is a file the worker reads back under a size limit and decodes as untrusted input. The process group is killed with the job's context or its time limit, so a hung analyser cannot outlive its scan.

## What leaves the installation

Nothing about an image, as long as the analyser behaves as configured. It runs with the flags that switch off its update check, its telemetry and every database update (`--skip-version-check`, `--disable-telemetry`, `--skip-db-update`, `--skip-java-db-update`, `--offline-scan`), and the only outbound requests of the feature are those of the database's sync: the published artifact, through the outbound client under the egress rules of [egress.md](egress.md), which carry no package name and no digest of an image. In `mode: repository` there are none at all. That the pinned binary makes no other request under these flags was read from its options and its log, not measured on the network.

## The database as input

The database decides what is reported about every image. In `mode: repository`, whoever holds `deploy` on the mirror repository decides that, as for the advisory files of R-VULN-005 (H-309). An archive is read as untrusted input: only `trivy.db` and `metadata.json` at the root are taken, each within a size bound, and no entry decides a path, so the class of bug behind GHSA-mcj4-mphf-j9ff (a crafted database writing outside the cache) has nothing to act on. A database that does not pass its checks is refused and the last good one stays.

## Why a failed scan is visible

A scan that did not finish, for any reason, leaves the evaluation `scan_failed` or `pending_scan` and never `evaluated`, so an image cannot be made to look clean by being too large, malformed or slow to analyse. The price is an image that stays unevaluated until an operator acts; that is the right way round.

## The working space is the spool's

An image scan writes a lot: the database unpacked (1.6 GiB on 2026-11-10) and the layers of the image copied into a layout (up to `max_image_size`, 3 GiB by default). Those files live below the worker's spool volume, the one volume a pod may write, and the volume is an `emptyDir` with a size limit: when it runs out, the kubelet evicts the pod, and with it the mirror pass, the sync and every other scan on it. So the scanning does not write what it has not been admitted for ([Q-VULN-145](../spec/vulnerability-scanning.md#q-vuln-145--the-working-space-of-the-scanning-is-admitted-through-the-spools-own-budget)).

- The spool counts the scanning's files in the same budget as an upload, a mirror pass and an advisory archive. The unpacked database is held once, by the sizes in its archive's headers, before a byte is written; a scan holds the compressed size of its image from the manifest plus 64 MiB before it writes the first blob. A hold that does not fit is refused by the same two checks as an upload: the budget and the free space of the volume.
- A scan that does not fit **waits** two minutes for the room, then is put off (`spool_busy`, due again after a minute); a scan that cannot fit next to the database in an empty spool is refused at once (`spool_too_small`, due again after an hour). The row stays `pending`, so the image reads `pending_scan`, never clean, and no attempt is spent.
- The room is returned on every exit of the scan (finished, failed, cancelled, drained); a crash returns it with the process, and the files a crash left are removed at the next start, before any scan holds room again.
- The chart refuses a worker spool budget (size minus `spool.headroom`) below `max_image_size` + 2 GiB + 512 MiB for the pool that claims the queue `vuln`, and the default `worker.spool.sizeLimit` is 8 Gi for that reason.

What this does not do is bound what the analyser itself writes ([H-871](#h-871--the-analysers-own-files-are-bounded-by-a-margin-not-enforced)).

## What this does not cover

### H-810 · Java archives are not analysed

Reading a JAR needs a second database, of Java artifacts, which trove does not mirror, and without it the analyser refuses to work offline on an image that contains one. The analysis skips `*.jar`, `*.war`, `*.ear` and `*.par`, so vulnerabilities in Java libraries inside an image are not reported. An image that holds only Java libraries shows `evaluated` with no findings. Mirroring the Java database as well would close this.

### H-811 · The block of an image is the block of its manifest

A pull is refused at the manifest. The layers of a blocked image are not refused, by their digest or through `/files/`, because a layer is shared between images and is not itself vulnerable or not. A client that already knows the digests can still fetch them. `/files/` of the manifest item itself is not gated by the pull's check either.

### H-812 · The analyser shares the file system and the network of the worker pod

The analyser runs as the same user in the same pod. If it were compromised through a crafted image, it could read anything the worker's user can read: the configuration and the mounted secrets, and through them the database and the object store. It could also open connections, because the chart renders no egress rule (ADR-154, [H-516](egress.md#h-516--the-chart-restricts-no-egress-of-its-pods)). The empty environment closes the easy route but not this one; closing it needs a filesystem sandbox or a separate pod per scan (Q-VULN-140). Until then, run the scanning on the dedicated pool of the queue `vuln` so that what is exposed is at least not the whole worker fleet.

### H-813 · The integrity of the database is that of the registry's digest and TLS

An archive is checked against the digest its manifest names, and the manifest comes over TLS from the configured registry. Nothing checks a signature by the publisher: a compromised publisher, or a compromised mirror reached by a redirect within the egress rules, decides what is reported. A digest-pinned database would give that up for manual updates.

### H-814 · The first pull of a remote image is served before it is scanned

A scan is asynchronous, and a pull-through serves the manifest it fetched. Unless the repository sets `onMissingData: block`, the first client of a new image gets it before any finding exists. The setting refuses it instead, at the cost of a failed first pull.

### H-815 · The database has no malicious-package source

`kind` is always `vulnerability`, so `blockMalicious` never blocks an image; a block is a triager's decision. An image built to carry malware is reported only where a CVE describes it.

### H-816 · A narrowed read grant does not reach an image's findings

A finding's version is a digest, and a grant narrowed to a tag is decided by the tag's path, so a caller whose read of a repository is narrowed by version sees no findings of its images (fail closed). The format has no such grants today (exposure rules are deferred, Q-OCI-23); the reader of a repository or of a package sees them.

### H-817 · One job scans at a time

One `vuln-image-scan` job runs across the installation, `concurrency` images at once on one worker. A backlog of many images after a database update is worked off at the pace of that one replica.

### H-818 · Memory is bounded softly and by the pod

`GOMEMLIMIT` asks the analyser to stay within `memory_limit` (512 MiB by default), and the pod's limit (1 GiB by default) is the hard stop: a scan that exceeds it kills the worker pod, not only the scan. The job then fails over, but a hostile image can repeat this.

### H-871 · The analyser's own files are bounded by a margin, not enforced

The worker holds what it writes itself (the layout, from sizes it checked against the digests) and 64 MiB for what the analyser leaves below its working directory: its cache and temporary files, the analysis and the report. Nothing limits the analyser's writing at the operating system: `Limits.FileBytes` is declared but not applied (no `RLIMIT_FSIZE`), and the 512 MiB output limit is checked after the file exists. What makes it real: an image whose analysis writes more than 64 MiB beside the layout. It can then use room the budget gave to another user, and the volume's free-space check at the next reservation is the only guard before the kubelet's. That the pinned analyser keeps its files within the margin for an ordinary image was not measured.

### H-872 · The database allowance of the chart is a constant

The chart checks the spool budget against 2 GiB for the unpacked database; the database was 1.6 GiB on 2026-11-10 and changes with every update. At run time the exact size is held, so a database that outgrows the budget is refused when it is unpacked, not when the chart is installed: `vuln-image-scan` then ends with a `skipped` result naming the reason, every image stays `pending_scan`, and the signals are that result, the log and the counter `trove_vuln_image_scans_total{result="spool_busy"}`, which `TroveVulnerabilityImageScansFailing` covers after an hour. What makes it real: an installation that sized `worker.spool.sizeLimit` exactly to the check and a database that grows past 2 GiB.

### H-873 · A refused scan waits an hour after the limit is raised

A scan refused with `spool_too_small` is due again after an hour. An operator who raises the pool's `sizeLimit` or lowers `max_image_size` sees the images scanned within that hour, not at once; a rescan of the repository (R-VULN-011) brings them forward. What makes it real: the first install of a pool with a spool that is too small.
