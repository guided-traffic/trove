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

### H-819 · The working space of a scan is not counted in the spool's budget

The scan works below the spool volume, but the spool's admission does not count those files. The unpacked database was 1.6 GiB on 2026-11-10 and the largest image is 3 GiB by default, which with the default `worker.spool.sizeLimit` of 5 GiB leaves little for the spool's own files: a mirror pass or an advisory archive at the same moment can run the volume out and have the pod evicted. An installation that scans large images or raises `max_image_size` raises the size limit of the pool that claims the queue `vuln` with it; the chart does not check the sum.

### H-818 · Memory is bounded softly and by the pod

`GOMEMLIMIT` asks the analyser to stay within `memory_limit` (512 MiB by default), and the pod's limit (1 GiB by default) is the hard stop: a scan that exceeds it kills the worker pod, not only the scan. The job then fails over, but a hostile image can repeat this.
