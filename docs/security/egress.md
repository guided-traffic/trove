# Egress

Status: implemented in M0 for the outbound HTTP clients of the process: an address check at connect time for every purpose except
the object store, a deny list and an allow list, and the refusal of zoned IPv6 addresses. In M2 the upstream clients of remote
repositories use it: the proxy engine ([internal/proxy](../../internal/proxy)) answers a refused fetch `502 egress_denied`, counts it in
`trove_remote_egress_denied_total{repo}` and records the security event `proxy.egress_denied`, and the upstream test reports a
refusal as `egress_denied`. Credentials of a remote repository travel over `https` only, unless its upstream itself is `http`.

This page says which connections trove opens to addresses that other parties name, which check applies to each, and where
the check does not reach. It describes the code of [internal/outbound](../../internal/outbound).

## Which connections are checked

The process builds its HTTP clients from one factory ([factory.go](../../internal/outbound/factory.go)), one client per purpose.
The address policy is on for every purpose except `objectstore`, and a Bastion's client of `bastion_main` comes from a factory of its own
whose policy denies nothing:

| Purpose | Address policy | Why |
|---|---|---|
| `oidc_discovery`, `jwks` | on | addresses an operator or a provider names: the login's identity providers and the CI issuers' discovery and keys ([ci/keys_fetch.go](../../internal/ci/keys_fetch.go)) |
| `webhook` | on | declared for the webhooks; no client of this purpose is built |
| `remote` | on | addresses that a repository's upstream, its redirects and its upstream metadata name, and the advisory provider of an npm audit forward (`Engine.Exchange`, [exchange.go](../../internal/proxy/exchange.go)); the clients are built in [proxy/upstream](../../internal/proxy/upstream/clients.go) |
| `remote` (the import command) | on | `trove admin import` builds its two clients, to the source registry and to the target repository, with the purpose `remote` and the policy of the process's `proxy.outbound`; the source's address and every file address it names are checked in the dialer ([import.md](import.md)) |
| `vuln_source` | on | the advisory sources of the vulnerability scanning, at the base URL of `vulnerabilities.sources` ([vuln/sync/service.go](../../internal/vuln/sync/service.go), `Purpose`; [wire_vuln_sync.go](../../internal/app/wire_vuln_sync.go)) |
| `bastion_main` | **in effect off**: the Bastion builds its own factory with an empty deny list, so only a zoned IPv6 address is refused ([bastion/mainclient/client.go](../../internal/bastion/mainclient/client.go), `factoryClient`) | the main installation a Bastion asks is operator configuration (`bastion.main.url`) and may sit in a private range; see [H-446](../spec/bastion.md#h-446--the-connection-to-the-main-installation-is-not-under-the-egress-address-policy) |
| `objectstore` | **off** ([factory.go](../../internal/outbound/factory.go), `defaults`) | the endpoint is operator configuration and, in development, a local service; see [H-56](../spec/architecture.md#h-56--the-object-store-endpoint-skips-the-egress-address-policy) |

Every S3 client is of purpose `objectstore`, because each is built through `s3.NewFromFactory`
([blob/s3/client.go](../../internal/blob/s3/client.go)): the blob store and the journal shipper of the replicas, and, in the Jobs of the
backup lane, the backup target client (`backup.target`, [target.go](../../internal/blob/s3/target.go), `NewTarget`), the backup's read of
the blob bucket (`backup.source`, [backup_create.go](../../cmd/trove/backup_create.go)) and the journal reader of a restore and of the
post-restore ([journal/replay/open.go](../../internal/journal/replay/open.go), `OpenJournal`). None of them passes the address policy, so
the endpoint of `backup.target` is as unchecked as `storage.s3.endpoint`.

The OTLP span export does not use the factory at all; it uses the SDK's transport ([H-53](../spec/operations-and-observability.md#h-53--span-export-bypasses-the-egress-address-policy)).

## How the check works

The check runs in the dialer's `Control` hook, after DNS resolution and before `connect`, on the literal address that is about to be
dialed ([policy.go](../../internal/outbound/policy.go), `control`; [factory.go](../../internal/outbound/factory.go), where the hook is set).
A name that resolves to a denied address is refused even when its first answer was public (a DNS rebinding). For a request through
an outbound proxy, trove resolves the target itself and checks the addresses before it hands the request to the proxy
(`checkedProxy`); what the proxy connects to after that is not visible to trove ([H-40](../spec/operations-and-observability.md#h-40--outbound-address-checks-are-weaker-through-an-outbound-proxy)).

The order of the rules in `check` ([policy.go](../../internal/outbound/policy.go)):

1. A **zoned IPv6 address** (`fe80::1%eth0`, `::1%1`) is refused, whatever `allow_cidrs` says. A zone scopes an address to one
   interface of this host, and `netip.Prefix.Contains` is false for every zoned address, so no CIDR rule would see it.
2. An **IPv4-mapped** IPv6 address is unmapped and checked as the IPv4 address (`::ffff:127.0.0.1` is checked as `127.0.0.1`).
3. An **allow** rule that contains the address permits it, even inside a denied range.
4. A **deny** rule that contains it refuses it.
5. An address of the **NAT64 prefix** `64:ff9b::/96` that is not decided by 1 to 4 is checked as the IPv4 address in its last
   32 bits (`64:ff9b::a9fe:a9fe` is checked as `169.254.169.254`).

Everything else is permitted. A refusal is an `EgressDeniedError` that names the address and the rule, and it counts in
`trove_outbound_egress_denied_total{kind}`. The proxy engine maps it to `502 egress_denied` ([classify.go](../../internal/proxy/classify.go)),
without naming the address in the answer.

## The default deny list

`proxy.outbound.address_policy.deny_cidrs` has this default ([config/outbound.go](../../internal/config/outbound.go)):

| Range | What it is |
|---|---|
| `127.0.0.0/8`, `::1/128` | loopback |
| `0.0.0.0/8`, `::/128` | unspecified and "this network" |
| `::/96` | the deprecated IPv4-compatible range |
| `169.254.0.0/16`, `fe80::/10` | link-local, including the instance metadata address `169.254.169.254` |
| `fd00:ec2::254/128` | the metadata address of one cloud |
| `100.100.100.200/32`, `168.63.129.16/32` | the metadata addresses of two other clouds |
| `224.0.0.0/4`, `ff00::/8` | multicast |
| `255.255.255.255/32` | broadcast |

The default list does **not** contain the private ranges `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16` or the unique-local `fc00::/7`.
An upstream that resolves to a private address is therefore reached by default. That is recorded as
[H-6](../spec/repositories-and-proxy.md#h-6--remote-repositories-reach-private-networks), which also names an egress
`NetworkPolicy` as the mitigation; the chart renders none ([H-516](#h-516--the-chart-restricts-no-egress-of-its-pods)).

`proxy.outbound.address_policy.allow_cidrs` (default empty) permits a range, and an allowed range wins over the deny list. Zoned
IPv6 addresses are refused before it is consulted, so an allow entry cannot open them.

## What is not covered by the check

- The NAT64 prefix is fixed at `64:ff9b::/96`. A cluster whose NAT64 gateway uses another prefix is not covered by step 5: see
  [H-78](#h-78--the-nat64-check-knows-only-the-well-known-prefix).
- The object store and the backup target, by design ([H-56](../spec/architecture.md#h-56--the-object-store-endpoint-skips-the-egress-address-policy)).
- The OTLP span export ([H-53](../spec/operations-and-observability.md#h-53--span-export-bypasses-the-egress-address-policy)).
- The connection a proxy makes after trove checked the target ([H-40](../spec/operations-and-observability.md#h-40--outbound-address-checks-are-weaker-through-an-outbound-proxy)).
- The private ranges, which the default list does not deny ([H-6](../spec/repositories-and-proxy.md#h-6--remote-repositories-reach-private-networks)).
- The pod's other outbound paths: database, Valkey and Sentinel connections have their own addresses from configuration and are not
  checked by this policy. Their egress is the one of the pod network ([H-516](#h-516--the-chart-restricts-no-egress-of-its-pods)).

## Which hosts receive credentials

A remote repository's credential goes to the upstream origin and to the hosts of `extraHosts` entries with `sendCredentials: true`, and
to other origins of a redirect chain only with `forwardAuthOnRedirect` ([R-REPO-033](../spec/repositories-and-proxy.md),
[R-REPO-039](../spec/repositories-and-proxy.md), Q-ARCH-199). The rule is enforced in the client's round tripper, once per request and
per redirect hop ([hop.go](../../internal/outbound/hop.go), `CredentialPolicy`), not by the caller. Today the rows apply to the credential
only: a client certificate (R-REPO-034) and custom headers (R-REPO-036) are refused by the configuration with `422 not_applicable`
([validate_remote.go](../../internal/repository/validate_remote.go)), so no remote has one; the columns for them state what the policy
would do with them:

| Request | Credentials and custom headers | Client certificate |
|---|---|---|
| to the origin (scheme, host and port equal) | sent | presented |
| to a host matching a `sendCredentials` pattern | sent | presented |
| redirect to any other origin, `forwardAuthOnRedirect: false` | removed | not presented |
| redirect to any other origin, `forwardAuthOnRedirect: true` | sent | presented |
| first request to any other host | removed, whatever the flag says | not presented |
| any hop that is not `https` while the origin is `https` | removed, whatever the rows above say | not presented |

The last row is checked first (`CredentialPolicy.allowed`, Q-ARCH-224): an `https` upstream's credential never travels in cleartext,
not to its own host over `http`, not to a `sendCredentials` host, not along a forwarded redirect. An `http` upstream is the operator's
choice of cleartext, and its hops keep the rows above.

`net/http` copies the headers of the first request to every redirect and strips only a few of them on a change of domain; the
policy removes `Authorization` and every credential or custom header it knows on every hop and sets them again only where the
table allows, so a header that is not one of the standard sensitive ones does not travel. The certificate is withheld in the TLS
handshake from the decision stored in the request context ([factory.go](../../internal/outbound/factory.go), `certGetter`).
Pre-signed object-storage redirects therefore receive nothing by default.

The write of an Owner of a project without `repositories:write-config` cannot move a stored credential to another host: a change of `url`, a new `sendCredentials` host or
`forwardAuthOnRedirect` turned on needs a new value in the same write
([Q-REPO-127](../spec/repositories-and-proxy.md#q-repo-127--a-project-administrator-gives-an-upstream-secret-as-a-value-and-a-stored-one-stays-with-its-hosts)).

The advisory provider of an npm audit forward is reached through the same client and rows: a provider on the upstream origin, or on a
`sendCredentials` host, gets the credential, and one on any other host gets none
([data-and-secret-flow.md](data-and-secret-flow.md#the-audit-forward)).

## Requests of a mirror's discovery

A remote repository in mode `mirror` discovers the packages of its upstream through requests that the format's lister names
([R-REPO-051](../spec/repositories-and-proxy.md), [Q-REPO-151](../spec/repositories-and-proxy.md#q-repo-151--a-discovery-request-reaches-the-upstream-the-extrahosts-entries-and-the-hosts-the-lister-names-and-the-last-never-get-a-credential)).
The lister sends none itself: every request goes through `Upstream.Get`, which is [Engine.Get](../../internal/proxy/get.go) behind the
host of [internal/formathost/mirror_host.go](../../internal/formathost/mirror_host.go). Besides the rules of the client above it
applies these:

| Request | Allowed host | Credentials, custom headers, client certificate |
|---|---|---|
| to the upstream origin | yes | as for a client: the credential sent (custom headers and a client certificate cannot be configured) |
| to a host of `extraHosts` | yes | as for a client: sent only with `sendCredentials` |
| to a host the lister names for this upstream (`MirrorHosts`, for example npm's replication host of the public registry) | yes, by host name, and never over `http` while the upstream is `https` | **never**: not even when an `extraHosts` entry with `sendCredentials` matches the host |
| to any other host | no: `502 foreign_host_denied`, nothing is sent | n/a |

The check of the host is [Client.AllowedMirror](../../internal/proxy/upstream/client.go), before the request takes its slot. The
refusal of the credential for a discovery host is a mark on the request's context
([outbound.WithoutCredentials](../../internal/outbound/hop.go)): the credential policy removes the credential (and would remove custom
headers and withhold a client certificate, which no remote can configure today) on every hop of a marked request, so a redirect from the
discovery host keeps the mark. A format
sets the `Accept` header and nothing else (`Upstream.Get` takes no other header), and the policy removes any `Authorization` header on
every hop, as for every request of a remote.

The other rules of a background request, which are security relevant because they decide what a pass can do to clients: its outcomes
never move or reset the circuit that turns clients away ([R-REPO-054](../spec/repositories-and-proxy.md)), so an upstream that
throttles the pass cannot make clients see `upstream_assumed_offline`; the populate event of what it stores names the system as actor
and `mirror` as reason ([R-REPO-057](../spec/repositories-and-proxy.md)); and the body of a request that names no bound is read up to
32 MiB ([Q-REPO-152](../spec/repositories-and-proxy.md#q-repo-152--the-body-of-a-discovery-request-that-names-no-bound-is-read-up-to-32-mib)).

## The upstream test

`POST /api/v1/projects/{projectKey}/repositories/{repoKey}/upstream-test` and `POST /api/v1/projects/{projectKey}/upstream-tests` send one
request through a remote repository's client ([proxy/upstream/probe.go](../../internal/proxy/upstream/probe.go)). The client is the one real fetches use, so the address check, the
trust store, the source network, the redirect limit and the credential policy apply unchanged; a refusal is reported as the result's
`error.code` `egress_denied` with the address and the rule, and counts in `trove_outbound_egress_denied_total{kind="remote"}`. The
operation needs `repositories:write-config`, or the Owner role in the project the path names; the saved test reads the repository by
its project and key, so an Owner tests only the repositories of its project. The unsaved test of an Owner without the capability takes no
`secretRef`, no `sourceNetwork` and no `outboundProxy.mode` but `default` (Q-REPO-127, [Q-REPO-128](../spec/repositories-and-proxy.md#q-repo-128--a-project-administrators-remote-repository-takes-the-installations-outbound-path)),
so it reaches the network through the installation's path and sends no secret of the installation. A repository in offline mode, and every
repository while `proxy.global_offline` is on, is not contacted. `Client.Allowed` ([client.go](../../internal/proxy/upstream/client.go)) refuses a URL taken from upstream metadata whose host is
neither the upstream's nor in `extraHosts` (`502 foreign_host_denied`), and one that leaves an `https` upstream for `http` (`502
insecure_redirect`, as a redirect to `http` is refused), before any address is resolved (Q-ARCH-224).

## Addresses that come from upstream documents

The engine builds the upstream URL of a fetch from three sources, and each passes `Client.Allowed` ([proxy/fetch.go](../../internal/proxy/fetch.go),
`request`) before a request is sent: the repository's URL and the canonical path; the URL map (`remote_url_map`), which names the URL a document
recorded for a path of its own package, for an artifact and for a metadata path alike (R-REPO-043); and, for a format whose addressing follows a
discovery document, the addresses that document names (the format's adapter reads them from the parsed document the engine hands it,
Q-ARCH-268). No client path chooses a host. A discovery document is a cache entry like any other: a caller who may not fill the cache never causes
its fetch, and a probe of the shadowing check resolves it as the system, so a first publish can cause one request to the upstream's discovery
document and store it.

## What this does not cover

The gaps of the spec pages whose mechanism this page describes:

- [H-446](../spec/bastion.md#h-446--the-connection-to-the-main-installation-is-not-under-the-egress-address-policy): a Bastion's connection to its main installation is not under the egress address policy.
- [H-6](../spec/repositories-and-proxy.md#h-6--remote-repositories-reach-private-networks): remote repositories reach private networks.
- [H-40](../spec/operations-and-observability.md#h-40--outbound-address-checks-are-weaker-through-an-outbound-proxy): outbound address checks are weaker through an outbound proxy.
- [H-53](../spec/operations-and-observability.md#h-53--span-export-bypasses-the-egress-address-policy): the span export bypasses the egress address policy.
- [H-56](../spec/architecture.md#h-56--the-object-store-endpoint-skips-the-egress-address-policy): the object-store endpoint, and with it the backup target, skips the egress address policy.
- [H-504](../spec/repositories-and-proxy.md#h-504--npms-replication-host-is-contacted-although-it-is-not-an-extrahosts-entry): npm's replication host is contacted although it is not an `extraHosts` entry.
- [H-971](#h-971--the-upstream-test-tells-its-caller-whether-a-credential-is-accepted-from-troves-address): the upstream test tells its caller whether a credential is accepted, from trove's address.

### H-78 · The NAT64 check knows only the well-known prefix

Stage: the address check of every checked client after DNS resolution ([policy.go](../../internal/outbound/policy.go), `nat64`).

Dormant: live only in a cluster whose NAT64 gateway translates a prefix other than `64:ff9b::/96`, and where a name that the
gateway resolves to such an address leads to an internal IPv4 address. The check then sees an address outside the deny list
and permits it. Subject: the metadata and internal addresses that the deny list protects, reached through a translated address.

What makes it real: a network-specific NAT64 prefix in the cluster's DNS64 setup, together with an upstream name that resolves to
an internal IPv4 address through it. The fix is a configuration key for the prefix, which does not exist yet.

### H-282 · The upstream test tells a holder of `repositories:write-config` what it sees of an address

Stage: the result of the upstream test ([repository/upstream.go](../../internal/repository/upstream.go), `UpstreamTestResult`).

Live: the address check refuses the denied ranges, but the private ranges are reachable by default
([H-6](../spec/repositories-and-proxy.md#h-6--remote-repositories-reach-private-networks)). For a URL the holder names, the result reports the
HTTP status, the subject and expiry of the certificate, the duration and a code that tells a refused connection from a timeout and an
unknown name. Subject: hosts and ports of the internal networks that the pods can reach, probed one request at a time by a principal who
may already configure a remote repository.

What makes it real: a principal with the capability and an installation whose cluster restricts no egress of trove's pods
([H-516](#h-516--the-chart-restricts-no-egress-of-its-pods)). The body of
the response is never returned, at most 64 KiB are read and dropped, and the messages never repeat the URL; the status and the timing are
the channel.

### H-971 · The upstream test tells its caller whether a credential is accepted, from trove's address

Stage: the upstream tests of [R-REPO-024](../spec/repositories-and-proxy.md#r-repo-024--remote-repository-settings) and
[R-REPO-064](../spec/repositories-and-proxy.md#r-repo-064--the-upstream-test-follows-a-token-handshake-and-names-the-token-service)
([repository/upstream.go](../../internal/repository/upstream.go)).

Subject: the accounts of an upstream registry and of its token service. A holder of `repositories:write-config`, or the Owner of a
project, can send a typed credential to a registry, or since R-REPO-064 to its token service, and read `authenticated`; the registry
sees trove's egress address, not the caller's, so its per-address limits and blocks fall on trove. Live for upstreams that answer
the probe with Basic and, since R-REPO-064, for the token service of a registry whose host receives the credential
(`Clients.tokenCheck` in [probe.go](../../internal/proxy/upstream/probe.go)). What bounds it: the management API's rate limits
([R-API-016](../spec/api-and-ui.md#r-api-016--rate-limits)), the request log that records the call
([R-OPS-010](../spec/operations-and-observability.md#r-ops-010--request-log)), and that the caller must already be able to configure a
repository; the test is not an audit event. What makes it real: a caller
who guesses another account's password through trove.


### H-516 · The chart restricts no egress of its pods

Stage: the pod network of every pod the chart deploys (the serving Deployments, the migration Job, the backup and restore
pods); the chart's policies are ingress rules only
([networkpolicy.yaml](../../deploy/helm/trove/templates/networkpolicy.yaml)), by the owner's rule of 2026-11-04
([ADR-154](../decisions.md#adr-154--network-policies-are-ingress-rules-between-the-charts-pods-each-with-its-port)).

Live for every installation whose cluster operator adds no egress policy of their own. Subject: every address the pod
network reaches from a trove pod, the cloud metadata endpoints (`169.254.169.254`) and in-cluster services among them,
reached by a process that is no longer trove's own code, or by a connection the address check does not cover: the object
store ([H-56](../spec/architecture.md#h-56--the-object-store-endpoint-skips-the-egress-address-policy)), the span export
([H-53](../spec/operations-and-observability.md#h-53--span-export-bypasses-the-egress-address-policy)), and the database,
Valkey and Sentinel connections, whose addresses come from configuration.

What remains is the address policy of this page, which every outbound client that trove builds applies at connect time in
every role, and the cluster operator's own policy. The second layer an egress policy of the chart would have added was weighed
and dropped with the owner's rule; R-HA-042 lists what each role must reach for an operator who writes one.

What makes it real: a flaw that lets an attacker run code in a trove process or steer a connection outside the outbound
clients of `internal/outbound`, in a cluster without an egress policy.

The chart `trove-bastion` follows the same rule since the owner's decision of 2026-11-05; the gap of its pods, which are built
to face the internet, is [H-522](bastion.md#h-522--the-chart-restricts-no-egress-of-the-bastion-pods).


### H-524 · A discovery host learns what a mirror asks it

Stage: the requests of a mirror's pass to a host the format's lister names ([Client.AllowedMirror](../../internal/proxy/upstream/client.go);
[Requests of a mirror's discovery](#requests-of-a-mirrors-discovery)).

Live for every mirror of an upstream whose lister names such a host: npm's replication host `replicate.npmjs.com`, for a mirror whose
`remote.url` is the public registry (the lister is built). The request carries no credential, no custom header and no client certificate, but it carries the path and query, the
`Accept` header, trove's `User-Agent` and the source address of the pod. Subject: the discovery host sees which key ranges and
change sequences the mirror reads and when, so it learns that this installation mirrors the upstream, the include patterns' literal
prefixes (a lister narrows its ranges by them) and the pace of the passes.

What makes it real: an operator of the discovery host who correlates source addresses with an installation. The names a mirror asks
for are names of the public upstream. The include patterns' literal prefixes are the exception: the replication host receives each as
`startkey` and `endkey`, so a prefix that is an internal scope or package name (`@acme/*` of a company that publishes `@acme/*` privately) tells
it that the name exists in that organisation's configuration, though not whether any package is published. The mirror never sends the
names of the installation's own packages (the engine's held names are not asked of the replication host), and a mirror of any other
upstream sends nothing to it.

### H-525 · A redirect of a discovery request is followed to any host the egress policy admits

Stage: the redirect rules of the client a discovery request goes through ([hop.go](../../internal/outbound/hop.go),
`WithMaxRedirects` in [clients.go](../../internal/proxy/upstream/clients.go)).

Live: the host check of a discovery request ([Client.AllowedMirror](../../internal/proxy/upstream/client.go)) is made on the URL the
format passes, and a redirect is followed under the rules of R-REPO-039 as for a client: at most `proxy.max_redirects` hops, never
from `https` to `http`, and each address through the egress policy, but to any host. The redirected request carries no credential
(the mark of [outbound.WithoutCredentials](../../internal/outbound/hop.go) holds on every hop for a discovery host, and the policy of
the client holds for the upstream and `extraHosts` hosts). Subject: what a discovery host that redirects can make a pod request, a
GET without a body to an address the policy admits, and whose answer a format reads as a discovery document up to 32 MiB.

What makes it real: an upstream or discovery host that is hostile or compromised, and private ranges that the address policy admits
([H-6](../spec/repositories-and-proxy.md#h-6--remote-repositories-reach-private-networks)). The answer is parsed by the format and
returned to nobody.
