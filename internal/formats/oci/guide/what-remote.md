A remote repository serves the images of an upstream registry{{if .Repository}} through `{{.Repository}}`{{end}}: Docker Hub, GitHub Container Registry, a cloud registry or another trove. Clients pull from trove and never from the upstream, so a workload keeps starting when the upstream is slow, rate limited or gone, as long as trove holds what it needs. Nothing can be pushed to a remote repository: a push, a mount or a delete answers `405` `UNSUPPORTED`.

### Two modes

In mode **pullThrough** trove fetches a manifest or a layer the first time a client asks for it and keeps it. A tag is looked up again after the metadata period (five minutes by default): trove asks the upstream with a `HEAD` request first and downloads the manifest again only when the tag moved, so an unchanged tag costs no pull from the upstream's quota. In mode **mirror** trove copies the tags and platforms the repository's mirror settings select ahead of time and serves them when the upstream is unreachable.

The upstream URL is the registry's origin, optionally with a path that every image name is requested below: `https://ghcr.io/acme` serves `app` from `ghcr.io/acme/app`.

### Docker Hub

Docker Hub names an official image with a `library/` prefix. For a remote whose upstream is Docker Hub, `nginx` and `library/nginx` are the same image and one cached copy. No other upstream gets that mapping.

Docker Hub limits how many manifests an address may pull per period, and an anonymous caller has the smallest allowance. All clients behind trove share the one address trove pulls from, so **authenticate a Docker Hub remote** with a Docker Hub user and access token whenever more than a few pulls per hour go through it, and always for a mirror. A rate-limited upstream shows up as `503` with code `TOOMANYREQUESTS` and a `Retry-After` header when trove has no stored copy; a stored copy is served as long as it lasts.

### Digests are verified

A manifest or layer is stored only when its SHA-256 matches the digest it was asked by, or the digest the upstream announced for a tag. A mismatch answers `502` and stores nothing.
