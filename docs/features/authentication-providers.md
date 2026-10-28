# Authentication providers and human login

This page catalogs every way a human proves their identity to the system: the local username and
password, delegated single sign-on through OAuth 2.0 / OpenID Connect and SAML 2.0, directory
authentication through LDAP and Active Directory, user and group provisioning through SCIM 2.0,
header-based single sign-on behind a trusted reverse proxy, an Atlassian Crowd / Jira connector,
client certificates, multi-factor authentication, anonymous access, the login page and UI session,
the self-service user profile with personal credentials, and the browser-based login handoff to
command-line tools. Machine identities that exchange a CI-issued OIDC token are on
[oidc-ci-integration.md](oidc-ci-integration.md); the token types themselves on
[access-tokens.md](access-tokens.md); users, groups, permissions and the global security
settings (password policy, lockout, hiding unauthorized resources) on
[identity-and-access.md](identity-and-access.md).

## Login model

### AUTHN-001 · Delegated authentication with one internal credential
Priority: **MVP**

The system separates authentication (who the caller is) from authorization (what the caller may
do). Identity is verified either against the system's own user store (the *internal realm*) or by
an external identity provider (IdP). Whatever method succeeded, the system converts the result
into its own credential: a signed access token (a JWT) that carries a scope claim and an optional
expiry and is used as the bearer credential for every following UI, API and CLI request.
Permissions are then evaluated by the system's own permission model from the user and its
groups, independent of the IdP.

One identity may hold any number of independent tokens, each with its own scope and lifetime.
Issuing a new token never invalidates earlier ones; ending a token before its expiry requires an
explicit revocation, which the system implements as a list of revoked token IDs checked on every
validation (see [access-tokens.md](access-tokens.md)).

| Method | Typical use | Mechanism | Entry |
|---|---|---|---|
| Internal username and password | Bootstrap, break-glass, small installations | Password check against the internal store | AUTHN-003 |
| OAuth 2.0 / OpenID Connect | Browser single sign-on with an enterprise or public IdP | Authorization code flow, optionally with PKCE | AUTHN-007 … AUTHN-014 |
| SAML 2.0 | Browser single sign-on with an enterprise IdP | Signed assertion validated with the IdP certificate | AUTHN-015 … AUTHN-021 |
| LDAP / Active Directory | Directory-backed login and groups | Bind and search | AUTHN-022 … AUTHN-027 |
| SCIM 2.0 | Provisioning and de-provisioning pushed by the IdP | IdP calls the system's SCIM endpoints | AUTHN-028 … AUTHN-030 |
| Header-based SSO | Kerberos, NTLM or web-server modules in front of the system | Trusted request variable set by a reverse proxy | AUTHN-031 |
| Atlassian Crowd / Jira | Legacy central user management | Dedicated connector | AUTHN-032, AUTHN-033 |
| Client certificates (mTLS) | Restricted networks | Username from the certificate via a reverse proxy, or mutual TLS as an extra condition | AUTHN-034 |
| Browser handoff for CLIs | Interactive command-line tools | Any of the above in the browser, token pair handed to the tool | AUTHN-044 |
| CI OIDC token exchange | Pipelines, no human | Identity mappings over token claims | [oidc-ci-integration.md](oidc-ci-integration.md) |

Interactions: every method that persists users feeds AUTHN-005 (just-in-time users); every
external method switches internal password login off by default (AUTHN-003). A server-level
switch, off by default, lets realms defined by server-side extension scripts take part in the
authentication chain ([events-and-extensibility.md](events-and-extensibility.md)).

### AUTHN-002 · External base URL as a prerequisite for single sign-on
Priority: **MVP**

OAuth, SAML and header-based SSO only work when the system's externally visible base URL is
configured in the general settings (see [operations-and-monitoring.md](operations-and-monitoring.md)).
The base URL is the root of every redirect and callback URL that has to be registered at the IdP,
and it is what SAML IdPs are normally given as the service-provider entity ID. A SAML configuration without a
configured base URL makes login fail with a server error (HTTP 500).

Edge cases: in a multi-node deployment the SAML logout URL may contain a base-URL placeholder that
the system resolves per request, so a user is returned to the node they used (AUTHN-020).

### AUTHN-003 · Internal password login and the basic-authentication switch
Priority: **MVP**

Internal-realm users log in on the login page with username and password and may present the
same credentials as HTTP Basic authentication to the API and to package clients. As soon as any
external method (OAuth, SAML, LDAP, header SSO, Crowd) is switched on, internal password login is
turned off unless an administrator re-enables it. While it is disabled, Basic authentication with an internal password is refused on
both the UI and the API, which also stops scripts and plugins that rely on it.

<details>
<summary>Settings (global security settings, basic-authentication section, and server configuration)</summary>

| Setting | Meaning | Default |
|---|---|---|
| Basic authentication for internal users | Accept internal username/password on UI and API | Enabled at server level; switched off automatically as soon as an external method is in use |
| Internal users still allowed to use basic authentication | Allowlist of internal users exempt from the switch, intended as a failsafe | unspecified |
| Hide basic authentication on the login page | Removes the username/password form from the login page; a link below the provider buttons still reveals it. Offered only when basic authentication is disabled and an external SSO provider (SAML or OAuth) is configured | off |
| Allow token creation through the API with basic credentials | Lets users that authenticate with username and password create tokens through the API (for automation users) | off |
| Check basic logins against the password policy | Server level: every username/password authentication is also validated against the configured password policy | off |

</details>

Operations (administrator only): read whether basic authentication is disabled; enable or disable
it.

Edge cases:

- Lockout: if basic authentication is disabled before an external administrator exists, the
  system has no usable administrator. The safe order is: configure an external method,
  create a group for external administrators and give it administrator rights, optionally
  allowlist one or more internal failsafe users, then disable basic authentication.
- Recovery on a self-hosted installation: an operator re-enables basic authentication through an
  offline configuration import on the server and a restart; the UI cannot do it.
- Failed-login throttling, account locking, password policy, password expiry and the policy for
  encrypted client passwords apply to internal passwords; they are global security settings on
  [identity-and-access.md](identity-and-access.md).
- When multi-factor authentication is on, a server-level switch can block all Basic-credential
  authentication (AUTHN-035).

### AUTHN-004 · Fallback from an external directory to the internal password
Priority: **P2**

When a user exists both in an external directory (LDAP, Active Directory) and in the internal
store with a password, the system first tries the directory and, if that fails, may accept the
internal password. For Active Directory the order is explicit: directory first, internal
database second. A per-user flag *disable internal password* (user edit dialog; default off)
switches the fallback off for that user, forcing directory authentication. Users created through
the API with that flag set need no password.

Interactions: per-user flags are on [identity-and-access.md](identity-and-access.md). Keeping the
fallback leaves one account with two valid passwords (a "split identity"); it should be removed
once the external provider works.

### AUTHN-005 · Just-in-time user creation and transient users
Priority: **MVP**

Each external method (OAuth, SAML, LDAP, header SSO, Crowd, forwarded client certificates) has an
*auto-create users* switch.

- On: the first successful login persists a user record in that external realm and adds it to the
  default groups (groups flagged to be joined automatically by new users).
- Off: no user record is written. On every request or session the identity is treated as a
  transient, in-memory user that receives only the permissions of the default groups. To give
  such a person individual permissions, an administrator has to create the user manually.

Edge cases:

- Directory group synchronization never creates users; users appear only after their first login
  (AUTHN-026).
- Group associations from LDAP via header SSO persist only when auto-create is on (AUTHN-031).
- A transient user cannot hold personal tokens or open a profile; personal credentials require a
  persisted user plus profile access (AUTHN-006).
- Default groups widen the rights of every transient identity, including CI token-exchange
  identities whose mapping names a user; they need regular review.

### AUTHN-006 · Profile access for externally created users
Priority: **MVP**

Each external method has an *allow created users access to the profile page* switch. When on,
users created by that method can open their profile without entering a password and create
personal credentials there (AUTHN-040, AUTHN-041, AUTHN-043). When off, such users cannot create
personal credentials, so they cannot authenticate package clients with a credential of
their own. For SAML users with auto-create on, the profile also lets them set an internal
password for later use. For users provisioned through SCIM, the corresponding switch in the SAML
configuration decides whether they may hold personal credentials.

Interactions: requires AUTHN-005 auto-create; personal tokens on
[access-tokens.md](access-tokens.md).

## OAuth 2.0 and OpenID Connect single sign-on

### AUTHN-007 · OAuth single sign-on general settings
Priority: **MVP**

When OAuth login is enabled, the login page shows one button per enabled provider. Clicking it
redirects the browser to that provider's login; a user already signed in at the provider is not
asked for credentials again, although some provider types first ask the user to approve that the
system may read their account data. After the callback the system creates or resolves the user (AUTHN-005) and starts a UI
session. Enabling OAuth disables internal password login by default (AUTHN-003).

| Setting | Meaning | Default |
|---|---|---|
| Enable OAuth | Shows all enabled providers on the login page; when off, only username/password login is offered | unspecified |
| Auto-create users | See AUTHN-005 | unspecified |
| Default provider for clients | The provider through which package clients (for example npm) log in; only a provider of the GitHub Enterprise type can be chosen (AUTHN-010) | unspecified |
| Allow created users access to the profile page | See AUTHN-006 | unspecified |

UI location: administration, authentication section, OAuth SSO page; the page lists the defined
providers with an action to add one.

Operations: whether a management API for OAuth settings exists is unspecified; configuration
happens in the UI or in the server's configuration file.

Edge cases: mapping ID-token or user-info claims to groups for OAuth/OIDC user login is
unspecified; group association from IdP data is defined only for SAML (AUTHN-018), LDAP
(AUTHN-025), header SSO and Crowd. Which claim becomes the username and which the e-mail
address is unspecified.

### AUTHN-008 · OpenID Connect provider definition
Priority: **MVP**

Any number of providers can be defined, several of the same type. A provider of the generic
OpenID Connect type needs all endpoints entered explicitly (no presets).

| Setting | Meaning | Default |
|---|---|---|
| Enabled | Provider appears on the login page | unspecified |
| PKCE enabled | Use the authorization code flow with PKCE (AUTHN-009); disables the client-secret field | unspecified |
| Provider name | Logical name, unique across the system | required |
| Provider type | Generic OpenID Connect, Google, GitHub (public), GitHub Enterprise, Cloud Foundry UAA | required |
| Client ID | Client identifier registered at the provider | required |
| Client secret | Secret registered at the provider; not used with PKCE | required unless PKCE |
| Authorization URL | Provider endpoint the browser is redirected to; may carry extra query parameters | required for the generic type |
| User-info (API) URL | Endpoint the system calls for account data not contained in the OAuth response | required for the generic type |
| Token URL | Endpoint where the system exchanges the authorization code | required for the generic type |

Behavior details:

- The redirect (callback) URL to register at the provider is a fixed login-response endpoint
  below the system's base URL (AUTHN-002).
- Extra query parameters can be appended to the authorization URL (for example a realm hint or
  `scope=openid profile email`); several parameters are joined with `&`.
- HTTPS endpoints with a certificate from a trusted CA work as is; a self-signed or private CA
  must first be added to the system's trust store.
- GitLab is a typical OAuth login source, but there is no GitLab preset; it is configured
  through the generic OpenID Connect type.

Interactions: AUTHN-011 (flow state across nodes), AUTHN-012 (binding internal accounts).

### AUTHN-009 · Authorization code flow with PKCE
Priority: **MVP**

Per provider, the login can use the authorization code flow with Proof Key for Code Exchange
([RFC 7636](https://www.rfc-editor.org/rfc/rfc7636)): the system sends a code challenge with the
authorization request, receives a one-time authorization code on the callback and redeems it at
the token endpoint together with the original code verifier to obtain the ID token. This proves
that the party redeeming the code started the flow and defeats code interception. With PKCE the
client secret is not used. PKCE is offered for the Google, Cloud Foundry UAA and generic OpenID
Connect types, not for the GitHub types.

Edge cases: the code-challenge method is unspecified.

### AUTHN-010 · Preset provider types: Google, GitHub, Cloud Foundry UAA
Priority: **P2**

Besides the generic type, providers can be created from presets whose endpoints follow the
third-party services' own URLs.

<details>
<summary>Per-type fields and preset endpoints</summary>

| Field | GitHub (public) | GitHub Enterprise | Google | Cloud Foundry UAA |
|---|---|---|---|---|
| PKCE | no | no | yes | yes |
| Domain filter | no | no | required: the Google Workspace domain whose accounts may authenticate | no |
| Docker login support | no | yes | no | no |
| npm login support | no | yes | no | no |
| Base URL (server used for authentication) | `https://github.com/` | the GitHub Enterprise server URL | — | — |
| Authorization URL | `https://github.com/login/oauth/authorize` | `<server>/login/oauth/authorize` | `https://accounts.google.com/o/oauth2/v2/auth` | `<uaa>/oauth/authorize` |
| User-info URL | `https://api.github.com/user` | `<server>/api/v3/user` | `https://openidconnect.googleapis.com/v1/userinfo` | `<uaa>/userinfo` |
| Token URL | `https://github.com/login/oauth/access_token` | `<server>/login/oauth/access_token` | `https://oauth2.googleapis.com/token` | `<uaa>/oauth/token` |

The Google column holds the endpoints of the configuration-file form. Open: the UI pre-fills
either these or Google's earlier endpoints
(`https://accounts.google.com/o/oauth2/auth`, `https://www.googleapis.com/oauth2/v1/userinfo`,
`https://www.googleapis.com/oauth2/v3/token`); the spec decides.

</details>

Edge cases:

- With the public GitHub type, every GitHub account that can reach the system can log in, also
  accounts outside the organization, unless the provider is restricted to an organization.
  Leaving the organization restriction empty is a security vulnerability; the restriction is not
  part of the field list above, and its semantics are unspecified.
- Only the GitHub Enterprise type supports client logins (`docker login`, `npm login`) through the
  provider and can be the default client provider (AUTHN-007). How the client flow works on the
  wire is unspecified.

### AUTHN-011 · Login-flow state across cluster nodes
Priority: **MVP**

The OAuth round trip leaves the system and comes back on the callback. The callback must reach the
node that started the flow, otherwise the login fails; a high-availability deployment therefore
needs session affinity on the OAuth callback path at the load balancer.

Interactions: [ha-and-deployment.md](ha-and-deployment.md) (load balancer); the same concern
applies to SAML logout redirects (AUTHN-020).

### AUTHN-012 · Binding an internal account to an OAuth identity
Priority: **P2**

A user with an internal-realm account (not one from LDAP, SAML or another external realm) can
link it to an account at an OAuth provider so that both logins reach the same user. In the
profile, after unlocking it with the internal password, a binding section lists the providers
with a bind action per provider.

### AUTHN-013 · Automatic pairing of SSO users by e-mail address
Priority: **P2**

In hosted (SaaS) deployments, an OAuth SSO login whose e-mail address matches an existing user is
paired with that user automatically; there is no setting. Whether the e-mail address must be
verified by the IdP is unspecified, as is the behavior on self-hosted installations.

### AUTHN-014 · Personal sign-up through public OAuth providers and invitations
Priority: **Out of scope**

During a trial of a hosted (SaaS) service, an administrator can allow sign-up through public
providers (Google, GitHub) and invite users by e-mail, who then join with a password or a public
OAuth account. One invitation per e-mail address; resending invalidates the earlier invitation
link, and an invitation link expires after 30 days by default (server-level setting). Trial and
subscription handling of a hosted service, without meaning for a self-hosted product.

## SAML 2.0 single sign-on

### AUTHN-015 · SAML service-provider login
Priority: **P2**

The system acts as a SAML 2.0 service provider and supports the Web Browser SSO profile and the
Single Logout profile. Login flow:

1. An unauthenticated user opens the system (or clicks the SSO login link).
2. The system builds an `AuthnRequest`, encodes it and redirects the browser to the configured
   login URL of the IdP (HTTP-Redirect binding). The system keeps its issued requests in a cache
   whose entries expire 300 seconds after they are written (server-level setting); how a response
   arriving after that is treated is unspecified.
3. The IdP authenticates the user (credentials or its own session) and returns a signed response
   to the system's assertion consumer endpoint via the browser (HTTP-POST binding).
4. The system verifies the signature with the IdP certificate stored in the configuration (DSA or
   RSA keys), checks the issuer and, where enabled, the audience restriction; on success it
   creates or resolves the user and redirects to the destination page.

Requirements and limits:

- A signed assertion is mandatory. Signing the response as well is recommended, yet an IdP setup
  that signs only the assertion works; a signed response is strictly required only for encrypted
  assertions (AUTHN-017).
- `AuthnRequest`s are not signed by the system.
- Audience-restriction verification is always on for new installations and cannot be switched
  off; installations upgraded from releases that predate it keep it off if SAML was already
  configured.
- Each configuration has its own assertion consumer endpoint whose URL ends with the
  configuration name, so the IdP entry must change when a configuration is renamed.
- The service-provider entity ID registered at the IdP is typically the system's base URL
  (AUTHN-002); it is distinct from the IdP entity ID stored in the configuration.
- The username is taken from the NameID unless a NameID attribute is configured (AUTHN-016).
- When anonymous access is enabled (AUTHN-036), the system does not redirect to the IdP
  automatically; users sign in through the SSO login link.
- An internal login page stays reachable at a fixed URL, so internal users can bypass SAML when
  the IdP fails or is misconfigured.

<details>
<summary>Service-provider metadata for upload to the IdP</summary>

| Element | Value |
|---|---|
| Entity ID | The system's ID in the federation, typically the base URL |
| Assertions signed (wanted) | true |
| Authentication requests signed | false |
| NameID format | `urn:oasis:names:tc:SAML:1.1:nameid-format:unspecified` |
| Assertion consumer service | One endpoint, HTTP-POST binding, URL below the base URL ending with the configuration name |

This is a template to fill in; whether the system serves its metadata at an endpoint is
unspecified.

</details>

Edge cases: IdP-specific setups cover Microsoft Entra ID, Google Workspace, Okta, JumpCloud and
Keycloak. For Okta, a username derived from the e-mail prefix is configured at the IdP, not
in the system.

### AUTHN-016 · SAML configurations
Priority: **P2**

A global switch enables SAML as a whole; below it, any number of named configurations (one per
IdP or tenant), each individually enabled. Installations moving from a single configuration to
several should delete the old one and recreate it under a name other than "Default"; renaming
means updating the IdP as well.

<details>
<summary>Configuration settings</summary>

| Setting | Meaning | Default |
|---|---|---|
| Name | Display name of the configuration; part of the assertion consumer URL | required |
| Enabled | This configuration accepts logins | unspecified |
| Login URL | IdP single sign-on endpoint the system redirects to | required |
| Logout URL | IdP single logout endpoint the system redirects to on logout (AUTHN-020) | required |
| IdP entity ID (issuer) | Must equal the issuer in the IdP's response | required |
| IdP certificate | X.509 certificate with the IdP's signing public key | required |
| Verify audience restriction | Validate the audience restriction of the assertion | on (new installations; see AUTHN-015) |
| Use encrypted assertion | See AUTHN-017 | unspecified |
| Auto-create users | See AUTHN-005 | unspecified |
| Allow created users access to the profile page | See AUTHN-006 | unspecified |
| Auto-redirect to the IdP | See AUTHN-019 | unspecified |
| Auto-associate groups | See AUTHN-018 | unspecified |
| Group attribute | Name of the assertion attribute carrying group names (AUTHN-018) | unspecified |
| E-mail attribute | Assertion attribute whose value becomes the user's e-mail address when auto-create is on or an internal user of that name exists | unspecified |
| NameID attribute | Assertion attribute used as the username instead of the NameID | unspecified |
| LDAP group settings | Names of LDAP group settings consulted after login (AUTHN-021) | unspecified |

</details>

Operations (administrator only):

| Operation | Inputs | Output |
|---|---|---|
| List configurations | — | All configurations (200) |
| Create configuration | Configuration; name, login URL, logout URL, certificate and issuer required | Created configuration (201); 400 on invalid input |
| Get configuration | Name | Configuration (200); 404 if unknown |
| Update configuration | Name, complete configuration (full replacement) | Updated configuration (200); 400 on invalid input |
| Delete configuration | Name | 204 |
| Get general SAML settings | — | Whether SAML is enabled and whether a SAML certificate is present (which certificate is meant is unspecified) |
| Enable or disable SAML | Enabled flag (required) | 204; 400 on invalid input |

UI location: administration, authentication section, SAML SSO page: list of configurations with
add, edit and delete, and the global enable toggle.

Edge cases:

- When the e-mail attribute is used, users must confirm their e-mail address with a one-time code
  at their first SAML login. The requirement is not limited to hosted deployments, but its
  rationale is administration through a hosted account portal, so whether self-hosted
  installations enforce it is unspecified.
- Configuration can also be supplied through the server's configuration file; one way of doing so
  silently ignores the NameID attribute, which then has to be set through the bootstrap
  configuration instead.

### AUTHN-017 · Encrypted SAML assertions
Priority: **P2**

When enabled, the system generates an X.509 service-provider certificate. The administrator
downloads it and uploads it to the IdP, which then encrypts the assertion with an algorithm chosen
at the IdP. One certificate serves all SAML configurations. Refreshing it affects every
configuration with encryption enabled; the new certificate must be uploaded to each IdP.

The system decrypts an encrypted assertion only when the enclosing SAML response carries a valid
signature. An IdP that signs only the assertion causes HTTP 401 at login; signing both response
and assertion is recommended.

### AUTHN-018 · Group association from SAML attributes
Priority: **P2**

With *auto-associate groups* on, the system reads the configured group attribute from the
assertion and adds the user to every group whose name matches one of its values; existing group
memberships stay untouched.

- Matching is case-sensitive and only against existing groups; no group is created. Groups must
  be created in the system beforehand with the names the IdP sends.
- Setting the group attribute without switching auto-association on has no effect.
- A server-level switch, on by default, persists the associations. Only additions are
  synchronized; a group removed at the IdP is not removed from the user automatically.
- Earlier releases did not persist: the association lasted only for the browser session, did not
  show in the group administration and did not apply to API logins with personal credentials.
  Whether turning the server-level switch off restores exactly that behavior is unspecified.
- Because associations are stored server-side, they no longer enlarge the payload of tokens the
  user creates (a reference token's payload is limited to 4,000 characters).

### AUTHN-019 · Automatic redirect to the SAML IdP
Priority: **P2**

With *auto-redirect* on for a configuration:

- the login link of the UI leads straight to that configuration's login URL;
- when a UI session expires the browser is sent to the SAML login URL, but not after a manual
  logout. The preference is remembered in a browser cookie that is set when the user chooses the
  SAML button on the first login.

A legacy mode (a feature toggle in the server configuration) restores an earlier redirect
implementation; it can be used only when exactly one configuration has auto-redirect set. How the
current implementation differs from the legacy one is unspecified. Anonymous access suppresses the
automatic redirect (AUTHN-015).

### AUTHN-020 · SAML single logout
Priority: **P2**

Service-provider-initiated logout: the user logs out, the system ends its session, builds a
`LogoutRequest` and redirects to the configured logout URL (HTTP-Redirect binding); the IdP ends
its session and sends the browser on to the URL configured at the IdP (responses via HTTP-POST).

- Signed logout requests are not supported.
- Without a logout URL pointing at the IdP's single logout endpoint the user stays signed in at
  the IdP after leaving the system, a known security gap.
- Setting the logout URL to the system's own login page logs the user out of the system only.
- In multi-node deployments the logout URL can contain a base-URL placeholder so that the user
  returns to the node in use.

### AUTHN-021 · LDAP group lookup for SAML users
Priority: **P2**

After a successful SAML login of a user who also exists in a directory, the system can fetch the
user's groups from LDAP (AUTHN-025) and associate them alongside the SAML groups. Two steps enable
it: a server-level switch, off by default, and, per SAML configuration, the list of LDAP group
settings to consult. The list can only be set through the update operation of AUTHN-016, which
replaces the whole configuration, so a client reads the configuration, adds the list and writes it
back. This replaces an earlier server-side plugin.

## LDAP and Active Directory

### AUTHN-022 · LDAP connection settings
Priority: **P2**

The system authenticates users against one or more LDAP servers, each described by a named
setting. Active Directory and the LDAP endpoint of Microsoft Entra ID use the same settings.

<details>
<summary>LDAP setting fields</summary>

| Setting | Meaning | Default |
|---|---|---|
| Key (settings name) | Unique ID of the setting | required |
| Enabled | Setting is used for login | true |
| LDAP URL | `ldap://host:port/<base DN>` or `ldaps://…`; the base DN is the root for searches and direct binds. For Active Directory the base DN may be omitted if a search base is set | required |
| User DN pattern | Pattern for direct binds relative to the base DN; `{0}` is replaced by the username, e.g. `uid={0},ou=People` | unspecified |
| Search filter | LDAP search filter ([RFC 2254](https://www.rfc-editor.org/rfc/rfc2254)) with `{0}` as the username, e.g. `uid={0}`; Active Directory: `sAMAccountName={0}` | unspecified |
| Search base | Context relative to the base DN; several bases separated by `\|`; optional, but strongly advised for Active Directory to avoid slow searches | unspecified |
| Search subtree | Search the whole subtree below URL plus search base | true |
| Manager DN | DN of the account used for searches; needs read access to group attributes such as `memberOf` when LDAP groups are used | unspecified |
| Manager password | Password of the manager DN | unspecified |
| E-mail attribute | Attribute mapped to the e-mail of auto-created users (`mail` in Active Directory) | unspecified |
| Auto-create users | See AUTHN-005 | unspecified |
| Allow created users access to the profile page | See AUTHN-006 | unspecified |
| Paged results | Use the paged-results control; the server must support it | unspecified |
| LDAP poisoning protection | Filter out users exposed to LDAP poisoning (recommended on); exact checks unspecified | unspecified |

</details>

Operations (administrator only):

| Operation | Inputs | Output |
|---|---|---|
| List settings | — | All settings (200) |
| Create setting | Setting; key and URL required | Created setting (201); 400 on invalid input |
| Get setting | Key | Setting (200); 404 if unknown |
| Update setting | Complete setting including its key | Updated setting (200); 400 on invalid input |
| Delete setting | Key | 204; 404 if unknown |
| Test connection | Complete, unsaved setting | 204 on success; 400 when the test fails |

UI location: administration, authentication section, LDAP page: list of settings with add, edit,
delete and a connection test.

Enabling LDAP disables internal password login by default (AUTHN-003). Whether read operations
return the manager password is unspecified.

### AUTHN-023 · LDAP login flow
Priority: **P2**

Two ways to find and verify the user:

- Search and bind (recommended): the system binds with the manager DN, searches for the user's DN
  with the search filter below the search base(s), then binds again as that DN using the password
  the user entered. The user is authenticated if this second bind succeeds.
- Direct bind: the system forms the DN from the user DN pattern and binds with it. It works only
  when the directory allows anonymous binding and users can be addressed by a direct DN; neither
  holds by default for Active Directory, where the pattern should stay empty.

After authentication the user's groups are taken from the LDAP group settings (AUTHN-025). On a
failure (which kind is unspecified) the user's directory group associations are removed by
default (server-level setting, AUTHN-024). The per-user internal-password fallback is AUTHN-004.

### AUTHN-024 · LDAPS, timeouts and directory tuning
Priority: **P2**

- LDAPS: an `ldaps://` URL with a certificate from a trusted CA works as is; a self-signed or
  private CA must be added to the system's trust store. Hosted (SaaS) deployments do not accept
  self-signed directory certificates.
- Server-level settings apply to all LDAP settings:

<details>
<summary>Server-level directory settings</summary>

| Setting | Meaning | Default |
|---|---|---|
| Connect timeout | Time to wait for a connection to the directory | 10,000 ms |
| Read timeout | Time to wait for a response after a request was sent | 15,000 ms |
| Connection pool idle timeout | Time an idle pooled connection may stay open | -1 (meaning unspecified) |
| Referral strategy | `follow`, `ignore` or `throw` | `follow` |
| Group page size | Batch size when fetching groups | 1,000 |
| Paging block-list cache expiry | Expiry of a block-list cache kept by the paging logic; what is put on the list is unspecified | 10 min |
| Force full DN in group member attribute | Special group-search filter for servers that do not accept a UID in the member attribute | off |
| Clear groups on failure | Remove a user's directory group associations in case of a failure; which failure is unspecified | on |
| Unlimited returned attributes | When off, group searches request only the needed attributes | off |
| Group filter threshold | Above this number of groups, extra filters are added for performance | 0 |
| Revalidate on token login | Refresh the directory user also when it authenticates with a token or API key (AUTHN-027) | on |

</details>

### AUTHN-025 · LDAP group settings and mapping strategies
Priority: **P2**

A group setting tells the system where and how to find groups in a directory, bound to one LDAP
connection setting. Three mapping strategies:

| Strategy | Directory shape |
|---|---|
| Static | Group entries list their members (e.g. `groupOfNames`/`member`, `groupOfUniqueNames`/`uniqueMember` holding user DNs); users do not know their groups |
| Dynamic | User entries carry an attribute listing their groups (e.g. `memberOf`, or a custom attribute with group DNs or names) |
| Hierarchy | Group membership is encoded in the user's DN: each `ou` (or custom attribute) on the path is a group, e.g. `uid=user1,ou=developers,ou=uk,…` puts the user in `developers` and `uk` |

<details>
<summary>Group setting fields</summary>

| Setting | Meaning | Default |
|---|---|---|
| Name | Unique name of the group setting | required |
| LDAP setting | Key of the LDAP connection setting used for group retrieval | required |
| Group base DN | Search base for group entries, relative to the base DN of the LDAP URL (not to the connection setting's search base); used when importing | unspecified |
| Group name attribute | Attribute of the group entry holding the group name, e.g. `cn`; used when importing | unspecified |
| Group member attribute | Multi-value attribute holding member DNs or IDs, e.g. `member`, `uniqueMember`; for the dynamic strategy the user attribute, e.g. `memberOf` | unspecified |
| Search subtree | Search below the group base DN recursively | unspecified |
| Force attribute search | Enforce a dynamic search of group attributes; exact semantics unspecified | unspecified |
| Filter | LDAP filter selecting group entries, e.g. `(objectClass=group)`; used when importing | unspecified |
| Description attribute | Attribute of the group entry holding the description; used when importing | unspecified |
| Strategy | Static, dynamic or hierarchy | unspecified |

</details>

Operations (administrator only): list group settings (200); create (name and LDAP setting
required; 201); get by name (200, 404); update with the complete group setting (200); delete by
name (204, 404); read the template of a mapping strategy by strategy name (member attribute,
filter, name attribute, description).

UI location: administration, authentication section, LDAP page, group settings list.

Edge cases:

- Nested Active Directory groups: dynamic strategy with membership attribute
  `msds-memberOfTransitive`, name attribute `cn`, filter `(objectClass=group)`; or static strategy
  with the matching-rule-in-chain attribute `member:1.2.840.113556.1.4.1941:`. Requires Windows
  Server 2012 R2 or later on the directory side.
- OpenLDAP provides `memberOf` only through an overlay, not by default; earlier releases therefore
  could not use the dynamic strategy with OpenLDAP, current releases support it.
- The UI requires the group setting to be enabled before it takes effect; the management API's
  group setting carries no separate enabled flag, so how the API expresses this is unspecified.

### AUTHN-026 · Importing and synchronizing directory groups
Priority: **P2**

Directory groups become system groups by import; afterwards permissions are granted to them like
to any group, while membership stays external and is decided only by the directory at login.

- UI: select a group setting, refresh to list the directory groups it finds, see each group's sync
  state (new, already imported, outdated because e.g. its DN changed) and import or re-import a
  selection. Imported groups appear in the group list marked as external.
- API, group creation: create a group in the LDAP realm, giving the directory group name, the
  strategy and the full group DN as realm attributes. Group names must be lower case; upper or
  mixed case breaks synchronization.
- Synchronizing groups never creates users (AUTHN-005).

Operations (administrator only):

| Operation | Inputs | Output |
|---|---|---|
| Search directory groups | Group setting; optional username to list only that user's groups | Groups, each with name, description, DN and whether an update is required |
| Import groups | Group setting and the list of groups to import | 204 |
| Refresh a group setting | Group setting name; mode: update existing groups, import new ones, or both (default both); optional username (default: all users) | Plain text stating how many groups were imported; 404 if the group setting is unknown |

### AUTHN-027 · Authentication caching and directory revalidation
Priority: **P2**

- For non-UI requests authenticated against an external system such as LDAP, the system caches
  the result (user, groups, permissions) and reuses it instead of querying the directory again
  until the entry has been idle for the configured time, default 300 seconds. UI logins are not
  cached. For Docker clients the cache takes effect only with token-based Docker authentication,
  where the client presents the token obtained in its first request on every later request.
- User and group lookups during authentication are cached as well, each entry for 300 seconds
  after it was written; the user cache holds at most 10,000 entries (server-level settings).
- Directory users who authenticate with a token or API key are refreshed from the directory by
  default (server-level switch). A server-level limit of 1,800 seconds decides when a user's token
  counts as outdated for this refresh; the exact semantics are unspecified.

## SCIM provisioning

### AUTHN-028 · SCIM 2.0 user and group provisioning
Priority: **P1**

The system implements the part of SCIM 2.0 ([RFC 7643](https://www.rfc-editor.org/rfc/rfc7643),
[RFC 7644](https://www.rfc-editor.org/rfc/rfc7644)) needed to manage users, groups and
membership from an IdP. SCIM complements a login method (SAML or OAuth); it does not log anyone
in. Verified IdPs: Okta and Microsoft Entra ID. Request and response bodies use
`application/scim+json`; only the two schema-definition reads answer with `application/json`.

<details>
<summary>SCIM operations</summary>

| Resource | Operation | Inputs and notes | Responses | Required privilege |
|---|---|---|---|---|
| `/ResourceTypes` | Discover supported resource types (User, Group) | — | 200 | administrator or identities-read scope |
| `/Schemas`, `/Schemas/{id}` | Discover schema definitions | schema ID | 200, 404 | administrator or identities-read scope |
| `/ServiceProviderConfig` | Discover supported features and authentication schemes | — | 200 | administrator or identities-read scope |
| `/Users` | List users or filter | `filter` (e.g. `userName eq "alice"`), `startIndex`, `count`, `excludedAttributes` | 200 list response | administrator or identities-read |
| `/Users` | Create user | user resource | 201, 400, 409 if it exists | administrator or identities-write |
| `/Users/{userName}` | Get user | addressed by username; `excludedAttributes` | 200, 404 | administrator or identities-read |
| `/Users/{userName}` | Replace user (PUT) | full resource; renaming is not supported | 200, 400, 404 | administrator or identities-write |
| `/Users/{userName}` | Partial update (PATCH) | used to disable a user (`active`) | 200, 400, 404 | administrator or identities-write |
| `/Users/{userName}` | Delete user | — | 204, 404 | administrator or identities-delete |
| `/Groups` | List groups or filter | `filter` (e.g. `displayName eq "devs"`), `startIndex`, `count`, `excludedAttributes` | 200 list response | administrator or identities-read |
| `/Groups` | Create group | group resource | 201, 400, 409 if it exists | administrator or identities-write |
| `/Groups/{name}` | Get group | addressed by name; `excludedAttributes` | 200, 404 | administrator or identities-read |
| `/Groups/{name}` | Replace group (PUT) | full resource; renaming is not supported | 200, 400, 404 | administrator or identities-write |
| `/Groups/{name}` | Partial update (PATCH) | membership changes; renaming is not supported | 200 or 204, 400, 404 | administrator or identities-write |
| `/Groups/{name}` | Delete group | — | 204, 404 | administrator or identities-delete |

Paths are relative to the system's SCIM base URL, which the SCIM page of the UI displays. List
responses carry `totalResults`, `itemsPerPage`, `startIndex` and `Resources`. User resources
expose `id`, `userName`, `active`, `emails` (with `value`, `primary`), `groups` and `meta`; group
resources expose `id`, `displayName`, `description`, `members` and `meta`.

</details>

Edge cases: usernames are the unique user key and are case-insensitive. The user list in the UI
shows each user's SCIM status (enabled, disabled, locked). The discovery resources were added
later than the user and group resources.

### AUTHN-029 · SCIM connector token
Priority: **P1**

The IdP authenticates to the SCIM endpoints with a bearer token restricted to reading, writing and
deleting users and groups; it grants nothing else and cannot be used to generate another token.

- UI: the SCIM page shows the SCIM base URL with a copy action, an expiry selector (preset values
  or custom; default one year, adjustable in the server configuration), a generate action that
  shows the token once for copying, and a revoke action.
- API: the general token-creation operation with the identities read/write/delete scope and a
  subject such as a dedicated SCIM service user ([access-tokens.md](access-tokens.md)).
- Revoking and recreating the token periodically, and updating the IdP accordingly, is the
  recommended practice.

### AUTHN-030 · Effects of de-provisioning on credentials
Priority: **P1**

SCIM closes the gap between login and lifecycle: SAML and OIDC only authenticate at login, while
the IdP pushes a disable or delete through SCIM as it happens.

- Disabling or deleting a user revokes that user's identity tokens.
- A disabled user cannot refresh tokens. Without SCIM, an external user removed from the IdP can
  keep refreshing tokens created earlier; SCIM is therefore strongly recommended whenever
  refreshable tokens are used.
- Administrators can additionally revoke a departed user's tokens explicitly
  ([access-tokens.md](access-tokens.md)).

## Header-based single sign-on

### AUTHN-031 · Trusted reverse-proxy authentication
Priority: **P2**

The system trusts a request variable set by an authenticating web server in front of it (for
example Apache HTTP Server with Kerberos, NTLM, SSPI or LDAP modules) and takes the username from
it without checking a password. Available on self-hosted installations only and not
recommended.

| Setting | Meaning | Default |
|---|---|---|
| Proxied by a trusted HTTP server | Master switch: trust the identity set on incoming requests | unspecified |
| Remote-user request variable | Name of the request variable or header holding the username | `REMOTE_USER` (the CGI convention) |
| Auto-create users | See AUTHN-005 | unspecified |
| Allow created users access to the profile page | See AUTHN-006; such users can create personal credentials and set a password for later use | unspecified |
| Associate LDAP groups | Associate the user with the groups returned by the configured LDAP group lookup; persistent only with auto-create on | unspecified |

Operations (administrator only, self-hosted only): read the configuration; create or update it
with the complete configuration (upsert; 200, 400 on invalid input).

UI location: administration, authentication section, HTTP SSO page.

Edge cases and trust boundary:

- The system has to listen only for the reverse proxy, and both have to run on one host;
  otherwise any client that reaches the system directly can forge the identity.
- The value can arrive as a servlet request attribute (AJP or JK connectors) or as an HTTP header
  set by the proxy (e.g. copying the authenticated user into a `REMOTE_USER` header). Any request
  attribute name can be configured, so custom filters can feed their own identity source.
- Header-SSO users can still use personal credentials for clients and the API.
- Enabling it disables internal password login by default (AUTHN-003).
- Open: the "temporary user" behavior belongs either to the auto-create switch, consistent with
  AUTHN-005, or to the profile-access switch; the spec decides.

## Atlassian Crowd and Jira

### AUTHN-032 · Crowd / Jira user-management connector
Priority: **P2**

Login requests are delegated to an Atlassian Crowd server (or a Jira user server), and with Crowd
the system takes part in Crowd's single sign-on. With Jira as the user server, SSO is not
available.

<details>
<summary>Connector settings</summary>

| Setting | Meaning | Default |
|---|---|---|
| Enable integration | Switch the connector on | unspecified |
| User-management server | Crowd or Jira (UI only; the management API has no such field) | unspecified |
| Server URL | Full URL of the Crowd or Jira server | unspecified |
| Application name | Application name registered for the system in Crowd/Jira | unspecified |
| Application password | Application password registered in Crowd/Jira | unspecified |
| Session validation interval | Minutes during which a session is not revalidated against Crowd; 0 means the Crowd token lives as long as the session | 0 |
| Use default proxy | Reach Crowd through the system's default outbound proxy, if one is defined | unspecified |
| Auto-create users | See AUTHN-005 | unspecified |
| Allow created users access to the profile page | See AUTHN-006 | unspecified |
| Custom cookie token key | String; semantics unspecified | unspecified |
| Direct authentication | Boolean; semantics unspecified | unspecified |
| Override all groups upon login | Boolean; semantics unspecified | unspecified |

Server-level settings: Crowd groups are fetched in batches of 1,000; validation factors (client
remote address and forwarded-for header) are not checked when validating the Crowd SSO token
unless switched on (default off).

</details>

Operations (administrator only): read the configuration; create or update it with the complete
configuration (upsert; 200, 400 on invalid input). The UI offers a connection test before saving.
Further Crowd client properties can be set in the server configuration and then override UI
values. Enabling it disables internal password login by default (AUTHN-003).

### AUTHN-033 · Crowd group import
Priority: **P2**

Like LDAP group import (AUTHN-026): refresh lists the Crowd groups, the table shows the sync state
of each, and selected groups are imported as external groups whose membership Crowd alone
controls. Permissions are managed on them like on any group. The Crowd group settings must be
enabled.

## Client certificates

### AUTHN-034 · Mutual TLS client authentication
Priority: **P2**

The client presents a certificate in the TLS handshake. Two deployment shapes exist:

- Self-hosted, as a login method: a reverse proxy in front of the system handles the TLS
  handshake and forwards the client certificate to the system in a request header. The system
  extracts the username from the certificate subject's common name with a regular expression and
  treats the request as that user. Server-level settings:

  | Setting | Meaning | Default |
  |---|---|---|
  | Enabled | Accept forwarded client certificates | off |
  | Certificate header name | Request header carrying the client certificate | unspecified |
  | Cache period | How long a successful certificate authentication stays valid | 100 s |
  | Username extraction | Regular expression applied to the subject CN | `(.*)` |
  | Auto-create users | See AUTHN-005 | on |
  | Allow created users access to the profile page | See AUTHN-006 | off |
  | Associate LDAP groups | Add the groups returned by LDAP | off |

- Hosted (SaaS), as an extra condition: the certificate never replaces username/password or a
  token. *Optional* mode accepts requests with and without a client certificate (for migration);
  *enforced* rejects every API and browser request without one, so browsers and clients must be
  set up first. The customer's CA (X.509, PEM) is handed over through a secure channel together
  with the maximum verification depth of the chain (default 2). CA revocation is not supported.
  The recommended rollout is optional first, then enforced.

Edge cases: a companion CLI accepts a client certificate and key in PEM format and can combine
them with a password or token; its package-manager integrations do not support client
certificates.

## Multi-factor authentication

### AUTHN-035 · One-time-password second factor
Priority: **P2**

When enabled, every user logging in to the UI with basic credentials must also enter a one-time
code from an authenticator app (for example Google Authenticator). The switch is global
and applies to all users.

- Enrollment on the first login after activation: the user enters username and password, scans a
  QR code with the authenticator app, then enters a code to finish enrollment and log in.
- Later logins: username and password, then a current code.
- Reset: an administrator resets a user's enrollment from the user edit dialog; the user enrolls
  again on the next login.
- Recovery: an administrator who lost their second factor is recovered by bootstrapping new
  administrator credentials on the server (recovery file read at service start) and then resetting
  the enrollment.
- The enrollment and login flows both start from the username/password form. Whether the system
  also asks for its own second factor after an SSO login is unspecified; for SSO users,
  multi-factor policies are expected to be enforced by the IdP.

| Setting | Meaning | Default |
|---|---|---|
| Enabled authenticators | List of second-factor providers; the feature is off while the list is empty | empty (off) |
| Application name | Name shown for the system in the authenticator app | the product name |
| Block Basic credentials under MFA | When MFA is on, refuse every authentication with Basic credentials (API and clients) | off |

UI location: administration, user-management settings, multi-factor section. In hosted (SaaS)
deployments the switch sits in the hosted account portal instead.

Edge cases: with the default settings, Basic-credential requests are not blocked while MFA is on;
whether they are subject to a second factor in any other way is unspecified.

## Anonymous access, login page and UI session

### AUTHN-036 · Anonymous access
Priority: **P1**

When anonymous access is enabled (server-level default: disabled), requests without credentials
run as a built-in anonymous user whose rights are granted through permission policies like any
user's. When it is disabled, unauthenticated requests are refused. A request without credentials
for a protected resource is answered with 401 so the client authenticates; an authenticated but
unauthorized request gets 403, or 404 instead of 403 when the system hides the existence of
unauthorized resources ([identity-and-access.md](identity-and-access.md)).

- Defaults: the anonymous user is not part of the built-in "all repositories" and "all remote
  repositories" permission policies; the recommended setup is a dedicated read-only policy on
  selected repositories. Broad anonymous rights expose existing and future repositories.
- Projects: adding the anonymous user to a project is blocked by default; a server-level switch
  allows it.
- Delegated administration: a setting lets non-administrators who manage permission policies
  assign the anonymous user, restricted to the actions an administrator approved.

| Anonymous access | Delegated assignment allowed | Administrators | Delegated managers |
|---|---|---|---|
| Enabled | yes | Add, edit, remove the anonymous user with any action | Add, edit, remove it with approved actions only |
| Enabled | no | Add, edit, remove with any action | No changes to the anonymous user |
| Disabled | — | Edit or remove it in existing policies only; cannot add it | No changes to the anonymous user |

Deleting a whole permission policy is not restricted by these rules.

- A per-anonymous-user option makes the login page the start page for anonymous visitors.
- With anonymous access on, anonymous users can download and populate remote caches as far as
  their permissions reach.
- Anonymous access suppresses the automatic SAML redirect (AUTHN-015, AUTHN-019).

UI location: global security settings (switch); user administration, anonymous user (start-page
option).

### AUTHN-037 · Login page presentation
Priority: **MVP**

The login page offers the enabled SSO providers as buttons (one per OAuth provider and SAML
configuration) and, unless hidden, the username/password form.

| Setting | Meaning | Default |
|---|---|---|
| Hide basic authentication | See AUTHN-003 | off |
| Hide SSO providers | Do not list providers; the user types the provider name to log in. Only available when basic authentication is hidden. Meant for many providers or tenants that should not see each other's providers | unspecified |

Edge cases: a direct URL to the internal login page always reaches the username/password form
(AUTHN-015).

### AUTHN-038 · UI session behavior
Priority: **MVP**

- *Remember me*: when chosen on the login form, the system stores a cookie in the browser for 7
  days that logs the user in automatically when the UI is opened again; after it expires the user
  logs in again. An operator can disable the option for everyone through a server-level setting
  (default: allowed; restart required), forcing a login every time.
- UI sessions are backed by session tokens. A server-level switch, off by default, blocks
  simultaneous logins: creating a new session token for a user revokes that user's earlier
  session tokens.
- Session expiry triggers the SAML auto-redirect (AUTHN-019); SAML logout ends the system session
  first (AUTHN-020); the Crowd connector revalidates sessions per its interval (AUTHN-032).
- Session idle and absolute timeouts are unspecified.

## User profile self-service

### AUTHN-039 · User profile page
Priority: **MVP**

Every persisted user has a profile page, opened from the user menu in the UI header ("edit
profile"). It groups the user's own credentials and details: personal identity tokens
(AUTHN-040), SSH public keys (AUTHN-041), e-mail address and password (AUTHN-042), bindings to
OAuth accounts (AUTHN-012) and the legacy API key (AUTHN-043).

- Unlocking: editing requires entering the current password and choosing *unlock*; changes are
  saved explicitly. Users from external methods (OAuth, SAML) can be given access to their API key
  and SSH keys without unlocking (AUTHN-006).
- Per-user flag *can update profile* (user administration; on by default for users created
  through the management API): when on, the user may change their
  profile details. It excludes the password, which only an administrator can
  change; how this squares with the self-service password change of AUTHN-042 is unspecified.
  Leaving the flag off suits shared accounts, for example a department user whose single password
  is known to several people.
- After unlocking, the page also shows the user's password in encrypted form (masked, with view
  and copy actions) for clients that should not hold the clear-text password; the policy on
  accepting such encrypted passwords is a global security setting on
  [identity-and-access.md](identity-and-access.md).

Interactions: transient users (AUTHN-005) have no profile. The profile has no section
for personal GPG or other signing keys; signing keys are managed system-wide
([identity-and-access.md](identity-and-access.md)).

### AUTHN-040 · Personal identity tokens
Priority: **MVP**

Users create tokens for themselves on the profile page; such *identity tokens* carry exactly the
user's own permissions. Flow: generate, optionally enter a description, see the new token once and
copy it, close. The token list shows each token with its description; a revoke action removes one
after confirmation.

- The profile issues the short reference form of the token (64 characters, fixed recognizable
  prefix), so clients that cannot handle long bearer tokens or speak only Basic authentication can
  use it.
- Usable as `Authorization: Bearer <token>`, as the password in HTTP Basic authentication with the
  username, and in the legacy API-key header (AUTHN-043).
- Stored hashed; never retrievable again from UI, API or database.
- A user can hold many identity tokens; users and administrators can list and revoke them.
- Deleting or disabling the user revokes them (AUTHN-030).
- Expiry: the global default token expiry applies, default 31,536,000 seconds (365 days); see
  [access-tokens.md](access-tokens.md).
- Limit: a reference token's payload may not exceed 4,000 characters; many or long group names
  can hit it (SAML group associations no longer count, AUTHN-018).

Requires profile access (AUTHN-006) for externally created users.

### AUTHN-041 · SSH public keys
Priority: **P2**

Users register SSH public keys (RSA) on their profile to authenticate over SSH. SSH authentication
is supported for the Git LFS client and for a companion CLI; when that CLI drives Maven, Gradle,
npm, Docker, Go or NuGet, or issues raw HTTP calls on the user's behalf, SSH keys cannot be used.

- Several keys per user: after unlocking, *add new SSH key* takes an alias and the public key; each
  key can then be copied or deleted. Earlier releases allowed one key per user.
- SSH authentication must be enabled on the server (server key management, see
  [identity-and-access.md](identity-and-access.md)).
- The CLI addresses the server with an `ssh://host:port` URL without any context path.
- Users from external methods can be given access to their SSH key field without unlocking
  (AUTHN-039).

### AUTHN-042 · Password self-service and reset
Priority: **P2**

- Change: after unlocking, the user can change their e-mail address and set a new password
  (entered twice).
- Not available: users of an external directory that are not created internally have no password
  to change.
- Forgotten password: the login dialog offers *forgot password*; after the user enters their
  username, the system e-mails a reset link to the address stored for that user.
- E-mail verification: when SAML is used with an e-mail attribute, users verify their e-mail
  address on their first login (AUTHN-016).
- Administrator operations (on [identity-and-access.md](identity-and-access.md)): change a user's
  password (allowed to administrators and to the user themselves), expire passwords, unlock users.

### AUTHN-043 · Legacy API key
Priority: **P2**

A single per-user secret that authenticates REST and client calls, either in a dedicated request
header or as the password in Basic authentication. It never expires, is stored retrievably and
exists at most once per user, so one key is shared by all of a user's clients. It has reached end
of life: creation is blocked by default in current releases and usage can be blocked globally.

| Setting | Meaning | Default |
|---|---|---|
| Block API-key creation | Users cannot create new keys | on (open: off is the alternative; the spec decides) |
| Block API-key authentication | Existing keys are no longer accepted | off |

Operations: create own key, get own key, revoke own key; administrator: revoke a given user's key,
revoke all keys. Administrators can identify users still authenticating with a key (login log
entries record the method; hosted deployments offer a CSV download of such users).

UI location: profile (generate, after unlocking); global security settings (remove all keys,
block usage).

Interactions: reference tokens are accepted in the same request header and as the Basic password
(AUTHN-040), so clients built around the key can move to tokens without new keys being issued.

## Command-line login

### AUTHN-044 · Browser login handoff for command-line tools
Priority: **P1**

An interactive command-line tool obtains credentials through the browser, so no password is typed
into the terminal and every login method of the UI (password, SAML, OAuth, MFA) works unchanged.

1. A fresh client session ID and a short verification code (4 characters) are generated for this
   attempt; the tool prints the code. Which side generates them is unspecified.
2. It prints, and may open, a UI login URL that carries the session ID, the client's name and a
   flag that a code check is required.
3. The user logs in in the browser by any method.
4. The UI then shows a verification screen asking for the code the client printed; the user types
   it and confirms. On success the UI tells the user to return to the tool.
5. Meanwhile the tool polls the server for the token belonging to its session (100 attempts at
   3-second intervals, about 5 minutes). On success it stores an access token and a refresh token
   in its configuration and uses them from then on.

Edge cases:

- A new session and a new code are created on every attempt; an expired attempt cannot be
  resumed.
- Wrong credentials or a locked account show the normal login error in the browser.
- Not usable in CI or headless environments; there, tokens or CI OIDC exchange apply
  ([oidc-ci-integration.md](oidc-ci-integration.md)).
- The server keeps client-login entries in a cache with a 300-second lifetime (server-level
  setting); how this relates to the 5-minute polling window is unspecified.
- The same CLI, given username and password instead, exchanges them for an access token rather
  than storing the password, and refreshes access tokens automatically unless told not to
  ([builds-and-ci.md](builds-and-ci.md)).

## Notes for implementers

- Enabling the first external provider silently switches internal password authentication off
  (AUTHN-003). Bootstrap order matters: an external administrator must exist first, or the
  installation is locked out.
- Group association from SAML and directories only matches groups that already exist
  (case-sensitive for SAML, lower case for directory groups created through the API) and is
  additive for SAML: removing a user from a group at the IdP does not remove the association
  (AUTHN-018, AUTHN-026).
- Users without auto-create are transient and receive the default groups on every request; default
  groups are therefore an implicit grant to every such SSO identity and to every CI identity whose
  mapping names a user (AUTHN-005).
- Without SCIM, de-provisioning at the IdP does not stop token refresh; disabling or deleting the
  user in the system (for example through SCIM) revokes their identity tokens. SCIM cannot rename
  users or groups (AUTHN-028, AUTHN-030).
- An encrypted assertion inside an unsigned response is rejected with 401, while an unencrypted
  assertion signed alone is accepted; IdP templates that sign only the assertion break encrypted
  setups (AUTHN-015, AUTHN-017).
- The assertion consumer URL and the OAuth callback are derived from the base URL and, for SAML,
  the configuration name; changing either breaks the IdP registration (AUTHN-002, AUTHN-015).
- An unauthenticated request for a protected resource gets 401, not 403/404; many package clients
  send credentials only after such a challenge. The switch that hides unauthorized resources
  replaces 403 with 404 only, so the 401 challenge stays. Anonymous access
  additionally suppresses the automatic SAML redirect (AUTHN-036).
- An OAuth login fails if the callback lands on another node, so HA setups need affinity on the
  callback path; keeping the flow state (state, PKCE verifier) in shared storage removes that
  requirement (AUTHN-011).
- Header-based SSO and certificate forwarding are only as safe as the network binding: the trusted
  variable or header must be unreachable for clients that bypass the proxy (AUTHN-031, AUTHN-034).
- With the second factor enabled, Basic-credential requests are still accepted unless the
  server-level blocking switch is on (AUTHN-035); the CLI browser handoff runs the full UI login,
  second factor included (AUTHN-044).
