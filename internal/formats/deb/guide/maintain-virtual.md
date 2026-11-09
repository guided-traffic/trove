### Members and what a build says about them

`GET …/deb/index` shows the state of each suite. A build that cannot merge a member keeps the previous index and says why in `reason`:

| Reason | Meaning and remedy |
|---|---|
| `member_unverified: <member>` | The member's stored release was not verified by a current upstream key, or the member has no keys. Set `deb.upstreamKeys` on the member; the next build fetches and verifies the release anew. |
| `member_expired: <member>` | The member's release has a `Valid-Until` that has passed. The archive must publish a new release. |
| `member_rollback: <member>` | The upstream served a release older than the one the last build merged. Taking the member out of the virtual repository and adding it back accepts the older release deliberately. |
| `member_unavailable: <member>` | The member could not be asked and trove has no stored copy of its release. |
| `no_signing_key` | The virtual repository has no primary signing key. |

### Uploads

An upload to the virtual repository goes to its deploy target (refused with `403` without one, because `deploy` has no target) and is listed in the virtual repository's index after the target has published and the virtual build has followed.

### The key

The virtual repository has its own signing key, rotated like a local repository's. Its clients trust that key and no member's.
