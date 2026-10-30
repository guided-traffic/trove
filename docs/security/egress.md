# Egress

Status: implemented in M0 for the outbound HTTP clients of the process: an address check at connect time for every purpose except
the object store, a deny list and an allow list, and the refusal of zoned IPv6 addresses. Remote repositories, the proxy engine
and the callers that map a refusal to `502` are not built yet. The address check is the code the upstream fetches of M2 will use.

This page says which connections trove opens to addresses that other parties name, which check applies to each, and where
the check does not reach. It describes the code of [internal/outbound](../../internal/outbound).

## Which connections are checked

The process builds its HTTP clients from one factory ([factory.go](../../internal/outbound/factory.go)), one client per purpose.
The address policy is on for every purpose except `objectstore`:

| Purpose | Address policy | Why |
|---|---|---|
| `oidc_discovery`, `jwks`, `webhook` | on | addresses an operator or a provider names |
| `remote` (M2) | on | addresses that a repository's upstream names |
| `objectstore` | **off** ([factory.go](../../internal/outbound/factory.go), `clientSettings`) | the endpoint is operator configuration and, in development, a local service; see [H-56](../spec/architecture.md#h-56--the-object-store-endpoint-skips-the-egress-address-policy) |

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
`trove_outbound_egress_denied_total{kind}`. The error type is the one the proxy engine maps to `502 egress_denied`; that engine is
not built yet.

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
`NetworkPolicy` as the mitigation; the chart renders none ([H-41](../spec/operations-and-observability.md#h-41--egress-is-unrestricted-and-ingress-enforcement-is-verified-on-one-cni-only)).

`proxy.outbound.address_policy.allow_cidrs` (default empty) permits a range, and an allowed range wins over the deny list. Zoned
IPv6 addresses are refused before it is consulted, so an allow entry cannot open them.

## What is not covered by the check

- The NAT64 prefix is fixed at `64:ff9b::/96`. A cluster whose NAT64 gateway uses another prefix is not covered by step 5: see
  [H-78](#h-78--the-nat64-check-knows-only-the-well-known-prefix).
- The object store, by design ([H-56](../spec/architecture.md#h-56--the-object-store-endpoint-skips-the-egress-address-policy)).
- The OTLP span export ([H-53](../spec/operations-and-observability.md#h-53--span-export-bypasses-the-egress-address-policy)).
- The connection a proxy makes after trove checked the target ([H-40](../spec/operations-and-observability.md#h-40--outbound-address-checks-are-weaker-through-an-outbound-proxy)).
- The private ranges, which the default list does not deny ([H-6](../spec/repositories-and-proxy.md#h-6--remote-repositories-reach-private-networks)).
- The pod's other outbound paths: database, Valkey and Sentinel connections have their own addresses from configuration and are not
  checked by this policy. Their egress is the one of the pod network ([H-41](../spec/operations-and-observability.md#h-41--egress-is-unrestricted-and-ingress-enforcement-is-verified-on-one-cni-only)).

## Which hosts receive credentials

A remote repository's credentials, its custom headers and its client certificate go to the upstream origin and to the hosts of
`extraHosts` entries with `sendCredentials: true`, and to other origins of a redirect chain only with `forwardAuthOnRedirect`
([R-REPO-033](../spec/repositories-and-proxy.md), [R-REPO-039](../spec/repositories-and-proxy.md), Q-ARCH-199). The rule is enforced
in the client's round tripper, once per request and per redirect hop ([hop.go](../../internal/outbound/hop.go), `CredentialPolicy`),
not by the caller:

| Request | Credentials and custom headers | Client certificate |
|---|---|---|
| to the origin (scheme, host and port equal) | sent | presented |
| to a host matching a `sendCredentials` pattern | sent | presented |
| redirect to any other origin, `forwardAuthOnRedirect: false` | removed | not presented |
| redirect to any other origin, `forwardAuthOnRedirect: true` | sent | presented |
| first request to any other host | removed, whatever the flag says | not presented |

`net/http` copies the headers of the first request to every redirect and strips only a few of them on a change of domain; the
policy removes `Authorization` and every credential or custom header it knows on every hop and sets them again only where the
table allows, so a header that is not one of the standard sensitive ones does not travel. The certificate is withheld in the TLS
handshake from the decision stored in the request context ([factory.go](../../internal/outbound/factory.go), `certGetter`).
Pre-signed object-storage redirects therefore receive nothing by default.

## What this does not cover

### H-78 · The NAT64 check knows only the well-known prefix

Stage: the address check of every checked client after DNS resolution ([policy.go](../../internal/outbound/policy.go), `nat64`).

Dormant: live only in a cluster whose NAT64 gateway translates a prefix other than `64:ff9b::/96`, and where a name that the
gateway resolves to such an address leads to an internal IPv4 address. The check then sees an address outside the deny list
and permits it. Subject: the metadata and internal addresses that the deny list protects, reached through a translated address.

What makes it real: a network-specific NAT64 prefix in the cluster's DNS64 setup, together with an upstream name that resolves to
an internal IPv4 address through it. The fix is a configuration key for the prefix, which does not exist yet.
