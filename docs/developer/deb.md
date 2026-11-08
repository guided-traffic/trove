# Debian (APT) repositories: the OpenPGP library and the counting stub archive

The Debian format is built in stages (task T-17, [work packages](../projektplan/apt-work-packages.md), spec
[formats/debian.md](../spec/formats/debian.md)). This page holds what stage 1 added: the OpenPGP leaf package
[internal/openpgp](../../internal/openpgp) and the counting stub archive [test/debstub](../../test/debstub). The format itself,
its data port and its key store are described here when they land.

| Part | Where | Requirements |
|---|---|---|
| The signer and the verifier | [internal/openpgp](../../internal/openpgp) | R-DEB-015, R-DEB-016, R-DEB-020, R-DEB-034, Q-DEB-10, Q-DEB-24, Q-DEB-35, Q-DEB-40 |
| The generator of installable `.deb` files | [test/debstub/debgen](../../test/debstub/debgen) | R-DEB-035 |
| The counting stub archive | [test/debstub](../../test/debstub) | R-DEB-035 |

## internal/openpgp

The only importer of `github.com/ProtonMail/go-crypto` (pinned once in [go.mod](../../go.mod), with `github.com/cloudflare/circl`
that it requires); the counting stub also imports it, on purpose (below). The package is a leaf in the
[layer table](../../internal/archtest/layers.go): standard library and the module only, no clock (every function takes the time it
decides against), no log line, no state.

| File | Responsibility |
|---|---|
| [openpgp.go](../../internal/openpgp/openpgp.go) | `Limits` and `DefaultLimits`, the error classes, `Key` and `Certificate`, `BoundSince` |
| [secret.go](../../internal/openpgp/secret.go) | `Generate`, `ParseSecret` and the import rules of R-DEB-015, `Key.SecretArmored` |
| [certs.go](../../internal/openpgp/certs.go) | `ParseCertificates` (the rules of `deb.upstreamKeys`), `ArmorCertificates` |
| [sign.go](../../internal/openpgp/sign.go) | `ClearSign`, `DetachSign`, `Key.EarliestSignature` (Q-DEB-40) |
| [verify.go](../../internal/openpgp/verify.go) | `VerifyClear`, `VerifyDetached`, the failure reasons |
| [armor.go](../../internal/openpgp/armor.go) | armor blocks: split, decode, encode |
| [testdata](../../internal/openpgp/testdata/README.md) | the archive keyring of `debian:13` |

**Keys.** `Generate(uid, created)` makes a version 4 key with public-key algorithm 22 (EdDSALegacy) over Ed25519, no expiry, no
subkeys, and a binding self-signature made at `created` (the module's `Config.Time`), so `BoundSince` equals `Created`. The caller
passes the PostgreSQL time minus one hour (Q-DEB-30). The module's default, algorithm 27, is never made or accepted: GnuPG 2.2 and
2.4 cannot read it. `Key` and `Certificate` carry `Fingerprint` (40 upper-case hex digits), `Algorithm`, `Bits`, `Created` and
`BoundSince`; a `Key` prints, formats and logs as its fingerprint and never shows the private part.

**Import** (`ParseSecret`, R-DEB-015) refuses, as `ErrUnsupportedKey` with the reason in the text: a version other than 4,
algorithm 27 or any algorithm but 22 and RSA, an EdDSA curve other than Ed25519, RSA below 3072 bits, a key whose secret part is
held elsewhere, a revoked key, one that cannot sign, a key with an expiry (the current self-signature of any identity or the newest
direct-key signature sets a lifetime), a creation or earliest binding self-signature later than `now`, and a signing subkey. An
unparseable or oversized block, two keys in one import, a missing or wrong passphrase answer `ErrInvalid`. A signed test message
proves the key before it is accepted. The returned `Key` keeps the primary key alone.

**Signing** (`ClearSign`, `DetachSign`) uses SHA-512, signature version 4, and refuses with `ErrTooEarly` a time before
`Key.EarliestSignature()`, one second after the later of `Created` and `BoundSince` (Q-DEB-40); the build waits for that time.
`ClearSign` signs the text byte for byte, so a `Release` that ends in a newline is signed with it. `DetachSign` writes all
signatures into one armored block. The signatures carry the module's random salt notation and are not deterministic.

**Verification** (`VerifyClear`, `VerifyDetached`, R-DEB-020, Q-DEB-35) succeeds when one signature of a configured certificate is
acceptable, and ignores signatures by other keys: the hash is SHA-256 or stronger; the signature time is at most
`openpgp.ClockSkew` (10 minutes) after `now`; the signing key (primary key or a signing subkey) is valid now and at the signature's
time, was created before the signature and is not revoked; and the signature is correct. A `Hash` armor header is checked only when
present (a signature whose hash it does not name is ignored). The message is one cleartext message with nothing before or after it.
A failure is a `*VerifyError`, which `errors.Is(err, ErrVerify)` matches and whose `Reason` (`openpgp.Reason(err)`) is the metric
label of `trove_deb_upstream_signature_failures_total`: `bad_signature`, `key_invalid`, `signature_expired`, `future`,
`weak_hash`, `hash_header`, `unknown_key`, `malformed`, `no_signature` or `too_many`. The text `VerifyClear` returns is the
dash-unescaped lines joined by newlines as the signature covers them: for an `InRelease` written by `gpg` that is the `Release`
without its last line ending, for one written by `ClearSign` the `Release` itself.

**Certificates** (`ParseCertificates`) reads one or more armor blocks (anything between them but white space is refused), 1 to 32
certificates in at most 256 KiB. It refuses a block that does not parse (the module's own reader would skip it), secret key
material, and a certificate that is revoked, expired or not yet valid at `now`, cannot sign, or has an RSA key below 2048 bits, as
`ErrInvalid`. Debian 13's archive keyring (9 certificates, 55,918 bytes binary, 75,803 armored) is a test fixture.

**Tests.** [key_test.go](../../internal/openpgp/key_test.go) (generated key shape, import rules, binding times, bounds, certificate
rules), [verify_test.go](../../internal/openpgp/verify_test.go) (signatures checked by the module's own reader and by the verifier,
co-signing, the rules of R-DEB-020 with signers the test builds with the module directly), and the fuzz targets
`FuzzParseCertificates`, `FuzzParseSecret` and `FuzzVerifyClear` in [fuzz_test.go](../../internal/openpgp/fuzz_test.go) (run with
`go test ./internal/openpgp -run xxx -fuzz FuzzVerifyClear`; the format conformance kit's `fuzz.go` is about routes, not parsers).

**Verified with the real clients** (2026-11-08, `apt-get update --error-on=any` against `file:` repositories on `debian:13`,
`debian:12`, `ubuntu:24.04`, `ubuntu:22.04`): `InRelease` and `Release.gpg` alone by one key; co-signed by two keys with a keyring of
either; an imported key whose only self-signature is 30 s old, signing 29 s after it; a keyring with another key fails and APT names
the missing key. Not done in this stage: the rotation table through trove's HTTP path (WP10), the key store, API and creation hook
(WP2's next stage).

## test/debstub: the counting stub archive

A deterministic APT archive that is not trove: it serves generated content over TLS only, counts every request, and fails on
command. Everything is generated in code (no recorded third-party data); two starts produce the same package, `Packages` and
`Sources` bytes (only the `Release` `Date` and the signatures differ). The stub signs with the OpenPGP module directly and does
not import `internal/openpgp` outside its tests, so trove's verifier is tested against an independent signer; its tests verify
every document it serves with trove's verifier.

| File | Responsibility |
|---|---|
| [stub.go](../../test/debstub/stub.go) | `Config`, `Server`, `New`, the archives, `NewGeneration`, `Generation`, `Reset`, `ResetAll`, `SetMode`, `Archives`, `Release`, `Files` |
| [corpus.go](../../test/debstub/corpus.go) | the package set (`defs`), the source packages, the layout constants |
| [archive.go](../../test/debstub/archive.go) | an archive: its indexes, generations, by-hash tree, pdiffs and `Release` rendering |
| [keys.go](../../test/debstub/keys.go) | the test keys from fixed seeds, `Keys`, `Key`, `Keyring`, the signing of `InRelease` and `Release.gpg` |
| [serve.go](../../test/debstub/serve.go) | the request path: count, gate, resolve, sign (cached per generation and mode), answer with validators |
| [mode.go](../../test/debstub/mode.go) | the switches (`Mode`) and the classes of request |
| [throttle.go](../../test/debstub/throttle.go) | the rate-limit answers of R-REPO-059 |
| [counts.go](../../test/debstub/counts.go), [inflight.go](../../test/debstub/inflight.go) | the counters and the requests open at once (R-REPO-055) |
| [control.go](../../test/debstub/control.go) | the control API below `/-/stub/` |
| [tls.go](../../test/debstub/tls.go) | `Start` (TLS or nothing), a throwaway CA, a certificate reloaded when its files change |
| [debgen](../../test/debstub/debgen) | `Build(Spec)`: an installable `.deb` (`ar` of `debian-binary`, `control.tar.gz`, `data.tar.gz` holding one script that prints a fixed line); `Hello(name, version, arch)` for the tests that upload |
| [cmd/debstub](../../test/debstub/cmd/debstub), [Containerfile](../../test/debstub/Containerfile) | the binary and the image (`make deb-stub-image`, tag `DEB_STUB_IMAGE`, default `trove-e2e-debstub:dev`) |
| [deploy/dev/debstub.yaml](../../deploy/dev/debstub.yaml) | the manifest for kind: Service `debstub`, a certificate of the dev CA, the control token in Secret `trove-debstub`; no step of the e2e harness applies it yet |

### What it serves

All below one host.

| Path | Content |
|---|---|
| `/debian/dists/stable/`, `/debian/dists/ratel/` | the suite `stable` (codename `ratel`, both names serve the same release): components `main` and `contrib`, `binary-amd64`, `binary-arm64`, `binary-all`, `main/source/` (`Sources`, one source package); `InRelease`, `Release`, `Release.gpg`; each index plain and `.gz`; `by-hash/{MD5Sum,SHA1,SHA256,SHA512}/` next to each |
| `/debian/dists/testing/`, `/debian/dists/badger/` | the suite `testing` (codename `badger`) with newer versions and the package `trove-newpkg` |
| `/debian/dists/bulk/` | one component, `binary-amd64`: `Config.BulkParagraphs` (70,000) synthetic paragraphs, about 24 MB plain, built on the first request; the files they name are not served |
| `/debian/pool/<component>/<prefix>/<source>/` | the `.deb` files and the source files |
| `/flat/binary/` | a flat repository with a directory: `deb <host>/flat binary/`; `Filename` is `binary/<file>`; `Suite` and `Codename` are `binary` |
| `/flatroot/` | a flat repository with the root as directory: `deb <host>/flatroot ./`; APT asks for `/flatroot/./InRelease`, which the stub serves and counts as written; `Filename` is `<file>` |

The packages (all `trove-hello` style: the installed script `/usr/bin/<name>` prints `hello from <name> <version> (<arch>)`):
`trove-hello` (amd64, arm64; depends on `libtrove1 (>= 1.2)`), `trove-hello-doc` (all, source `trove-hello`), `trove-tools` (amd64,
arm64; an alternative dependency), `trove-config` (all; provides `trove-config-default`), `libtrove1` (amd64, arm64; source
`libtrove`), and in `contrib` `trove-extra` (amd64) and `trove-extra-data` (all). A generation adds `trove-zz-g<NNN>`. The flat
repositories hold `trove-flat`. `Packages` paragraphs are the control file plus `Filename`, `Size`, `MD5sum`, `SHA1`, `SHA256` and
`SHA512`. Every file validates: `dpkg` installs a generated package (checked on `debian:13`), and the four client images read the
stub's tree, install from it and run the script (Client and library behaviour in the spec).

**Release.** `Origin`, `Label`, `Suite`, `Codename`, `Date`, `Acquire-By-Hash: yes`, `Architectures` (`all amd64 arm64`),
`Components`, `Description`, and `MD5Sum`, `SHA1`, `SHA256`, `SHA512` lists of every index file, the pdiff `Index` included.
`Date` is the stub's clock minus a minute, and at least a second after the previous generation's. `InRelease` is framed as `gpg`
writes it. The three signed documents are made once per generation and signing configuration, so repeated requests see equal bytes
and conditional requests are answered 304 (`ETag` and `Last-Modified`, `Range` is honored; `Mode.NoValidators` withholds them).

**Generations** (`NewGeneration`, `POST /-/stub/generation`): a package is added to `main/binary-amd64/Packages` (to the one
`Packages` of a flat repository), the `Release` gets a later `Date`, the previous generation's files stay reachable by hash, and the
changed `Packages` gets a pdiff: `Packages.diff/Index` (`SHA256-Current`, `-History`, `-Patches`, `-Download`) and one patch per
generation, an `ed` script that appends the new paragraph (`Packages.diff/<date>.gz`, also by hash). `ResetAll` returns every
archive to its first generation.

### The test keys

Fixed seeds, algorithm 22, version 4; the certificate is reproducible (a test pins the fingerprints). Created on fixed dates and
not "an hour back" (Q-DEB-70).

| Key | Fingerprint | Created | Use |
|---|---|---|---|
| `a` | `744FA9DC6DDF29FCBBDCFC3F0F1B0C0EE2D15353` | 2026-01-01 | the archive's key; default signer |
| `b` | `1740FB0FDF7BEDEAE568BFDFA14B934907B0A66E` | 2026-01-01 | a second key, for co-signing |
| `c` | `112CAA63A6EDBB4E9727D9D67BF765B76B7E6242` | 2026-01-01 | a key nobody configured |
| `d` | `AA0B4230C41119252C6CFF1C942F62DCF3C4BBE2` | 2024-01-01, one year | an expired key; its signatures are dated 2024-06-01 |

`Server.Keyring("a", "b")`, `GET /-/stub/keyring?names=a,b` and `debstub keyring a` give the armored certificates for
`deb.upstreamKeys`. trove refuses `d` as a configured key (it is expired); a verifier test hands its certificate over directly.

### The switches (`Mode`, `POST /-/stub/mode`, R-DEB-035)

| Field | Effect |
|---|---|
| `down` | `503`, `reset` (connection cut) or `hang` (held until the client leaves or two minutes) |
| `delay_ms`, `fail_status` | per class (`release`, `index`, `diff`, `by-hash`, `pool`, `other`, `all`): hold the answer; answer a 5xx |
| `no_by_hash` | by-hash paths answer 404 and `Acquire-By-Hash` is left out |
| `no_inrelease` | `InRelease` answers 404; `Release` and `Release.gpg` stay |
| `no_hash_header` | `InRelease` without the `Hash` armor header |
| `sign_with` | the signing keys, `["a"]` by default; `["a","b"]` co-sign; `["c"]` an unknown key; `["a","c"]` a known and an unknown; `["d"]` an expired key with a backdated signature |
| `sign_hash` | `sha512` (default), `sha256`, `sha1` |
| `signature_skew_seconds` | added to the time of every signature (not of `d`): a signature from the future |
| `broken_signature` | `InRelease`'s text is changed after signing, `Release.gpg` signs other bytes |
| `date` | `older` (an hour before the generation's) or `ahead` (20 minutes after the clock) |
| `valid_until` | `passed` (ten minutes ago) or `6d` (six days after the `Date`) |
| `not_found` | archives (suite ids and codenames, `flat`, `flatroot`) whose every path answers 404 |
| `wrong_package` | package file names (or `all`) whose bytes differ from their index entry |
| `index_mismatch` | the plain `Packages` and `Sources` paths (and `.gz`) differ from what the `Release` lists; by-hash stays right |
| `no_validators` | no `ETag` or `Last-Modified`, conditional headers ignored |

`Throttle` (`POST /-/stub/throttle`) answers the n-th request, a run of them or all requests for a period with `429`, `429-date`,
`503`, `503-slowdown`, `403-quota` or `429-quota` (the answers the mirror recognizes, R-REPO-054, R-REPO-059), per class.

### Counters and the control API

`Counts()` keeps one entry per method, host, path as written, query and conditionality, with the kind (class) and the statuses; a
cut answer counts under `0`. Helpers: `Requests`, `OfKind`, `Prefixed`, `Conditional`, `Delivered`, `NotModified`. `Inflight()` is
the number of requests open now and the most at once since the last reset.

Below `/-/stub/` with `Authorization: Bearer <token>` (`Config.ControlToken`, `-control-token-file`; without a token the API does
not exist; control requests are not counted): `GET counts`, `GET inflight`, `POST reset[?all=true]`, `GET|POST mode`,
`GET|POST throttle`, `GET archives`, `POST generation {"archive"}`, `GET release?archive=`, `GET keys`, `GET keyring?names=`,
`GET files`.

### Running it

```sh
go run ./test/debstub/cmd/debstub serve -self-signed localhost,127.0.0.1 -ca-out /tmp/ca.crt -listen 127.0.0.1:8443
make deb-stub-image            # the container image, tag $(DEB_STUB_IMAGE)
```

In a Go test, `debstub.New(cfg)`, `stub.Start("127.0.0.1:0", ss.ServerConfig())` with `ss, _ := debstub.NewSelfSigned(debstub.LoopbackHosts...)`;
`Running.URL` is `https://deb.localhost:<port>` and the name resolves through `Running.DialContext` or `Running.DialOverride()`
(trove's `outbound.WithDialOverride`), as with the other stubs.

What it does not do: bzip2, LZMA, xz and zstd indexes, `Contents` and translation files, `dep11` and uploads;
the xz handling of trove is tested with its own unit tests and WP8's bound test (Q-DEB-73).
