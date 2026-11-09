APT shows the status line of a failed request. The body, which `curl` shows, carries a `code`.

| Message or answer | Cause and remedy |
|---|---|
| `401 Unauthorized` on `apt-get update` | No credential in `/etc/apt/auth.conf.d/`, a revoked or expired token, or a `machine` line that does not match the URL of the source. |
| `404 Not Found` for `dists/<suite>/InRelease` | The suite has no published index yet (nothing was uploaded to it, or the first build is still due), or the credential may read single packages only and not the index. |
| `The following signatures couldn't be verified because the public key is not available` | The key file in `Signed-By` is not this repository's, or the repository's key was rotated and the file was not replaced. Download `signing-key.asc` again. |
| `Release file … is not valid yet`, or `OpenPGP signature verification failed … created after the --not-after date` | The clock of the client trails trove's by more than about a minute (about 70 s for the `gpgv` clients of Debian 12 and Ubuntu, about 60 s for the Debian 13 client). trove dates its releases a minute behind its own clock on purpose; correct the client's time. |
| `Hash Sum mismatch` | The client read the index while a newer one was published. Run `apt-get update` again; the client fetches index files by hash, so a retry succeeds. |
| `E: Version '2.10-3' for 'hello' was not found` | The version is not in the index of the suite: it was deleted, it is listed in another suite, or the index has not caught up with the upload yet. |
| `403` `deb_members_unreadable` for `InRelease` of a virtual repository | The index of a virtual repository is one for every caller, so reading it needs `read` without package restriction on every member. Ask for `read` on the members. |
| `404` after an upload | The index is built after the upload answered. Wait for the `publishedSeq` of the answer to reach its `changeSeq`. |
