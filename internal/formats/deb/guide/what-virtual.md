A virtual repository merges the package indexes of its members into one{{if .Repository}}: `{{.Repository}}` is the address your client adds as a source{{end}}. The members are local and remote Debian repositories of the project; the client sees one archive, and trove signs its indexes with the key of the virtual repository, not with a member's.

### What a client reads

The index of a suite lists the packages of the members in the order of the member list, priority members first. A package that two members list with the same version and architecture is listed once, from the first member. A name that a claim gives to one member comes only from that member, in every version, so a remote member cannot offer a higher version of a name your local repository owns. The `Filename` of every entry is `members/<member>/<path>`: the client downloads each file from exactly the member whose entry it read, and checks the digest of that entry.

### What a member must be

A remote member needs upstream keys (`deb.upstreamKeys`), because only a release that trove verified is merged. A suite that a member does not have contributes nothing; it is no failure. The virtual repository serves the suites of `deb.suites` and the suites of its local members.

### When the index changes

The index is built by a job: after a local member publishes (at the earliest `deb.virtual_index_interval` after the last build), after a change of the members, the claims or the settings, and when the repository's `virtual.mergedMetadataTtlSeconds` has passed, so that a change of a remote member reaches the clients without a request. A build whose inputs did not change publishes nothing.
