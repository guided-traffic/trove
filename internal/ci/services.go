package ci

import (
	"context"

	"github.com/guided-traffic/trove/internal/ci/claims"
	"github.com/guided-traffic/trove/internal/ci/subject"
	"github.com/guided-traffic/trove/internal/meta"
)

// EvaluateRequest is the body of the dry run (R-CI-019): a claim set, never a token, so nobody has to
// paste a live credential. No signature or time check runs unless Now and the time claims are given.
type EvaluateRequest struct {
	// SubjectKind is id_token (the default) or, from P2, job_token.
	SubjectKind string
	Claims      claims.Claims
	// Scope is the `scope` request parameter to narrow with, "" for none.
	Scope string
	// Now, when set, makes the dry run check exp, nbf and iat against it.
	Now *int64
}

// EvaluateResult is the decision trace of the dry run (R-CI-019, R-UI-036). It issues nothing and
// records no jti.
type EvaluateResult struct {
	CrossProject bool
	Trace        []TraceEntry
	// Selected are the references of the contributing mappings.
	Selected []string
	// Failure is the reason the exchange would fail with, "" when it would succeed.
	Failure Reason
	Detail  string
	// Grants are the rendered grants after narrowing; Scope the response's scope string.
	Grants     *claims.Rendered
	Scope      string
	TTLSeconds int64
	Warnings   []claims.Warning
}

// SnippetRequest is the query of GET …/snippets/{kind} (R-API-035, R-UI-035).
type SnippetRequest struct {
	// Kind is gitlab-ci in the MVP.
	Kind string
	// Mapping names the mapping the snippet is for; Format is npm or nuget.
	Mapping string
	Format  string
	// Project is the key of the project whose tier holds the mapping (a name is unique within an issuer's
	// tier, R-CI-009); empty for a mapping of the system tier.
	Project string
}

// Issuers is the issuer and signing-key service (R-CI-001, R-CI-002, R-CI-003, R-CI-005, Q-CI-16),
// built by work package W1. Every write that changes an issuer runs in one READ COMMITTED
// transaction that locks the ci_issuers row FOR UPDATE, increments version, calls cluster.Notify on
// the change area ci_mappings, records its audit event (ci.issuer.created, .updated, .disabled,
// .enabled, .deleted; the journal entry of the last three and of .updated as R-CI-020 says) and, when
// the issuer is disabled or deleted, revokes its unexpired tokens through [Revoker] in that same
// transaction. The network fetches (discovery, JWKS) run before and outside any transaction and
// hold no connection (R-ARCH-019).
//
// Errors are *errs.Error: 404 not_found, 409 already_exists, 409 issuer_managed_by_configuration,
// 409 has mappings (delete without cascade), 412 precondition_failed, 422 with the `code` of
// R-CI-002 (pin_claim_not_immutable, public_issuer_required, kind_not_available,
// discovery_unreachable, issuer_mismatch, no_usable_keys, algorithms_not_offered), 502 for an
// unreachable issuer on a forced refresh.
type Issuers interface {
	// List returns every issuer with its mapping count and key summary, ordered by name.
	List(ctx context.Context) ([]Issuer, error)
	// Get returns one issuer with its key summary.
	Get(ctx context.Context, name string) (Issuer, error)
	// Create creates an issuer with the defaults of R-CI-001 filled in (the audiences from
	// server.base_url). With validate, it fetches discovery and keys first and stores the key set
	// under the write rule of R-CI-003, so the other replicas load it through ci_issuer_keys.
	Create(ctx context.Context, actor Actor, in IssuerInput, validate bool) (Issuer, error)
	// Replace is a full replacement under If-Match (expected is the version the client read,
	// errs.AnyRevision for the deliberate blind write). name, issuer and kind are immutable. It writes
	// exactly one event, chosen by enabled: .disabled, .enabled or .updated (R-CI-020).
	Replace(ctx context.Context, actor Actor, name string, expected int64, in IssuerInput) (Issuer, error)
	// Delete refuses while mappings exist unless cascade, which deletes the mappings and their
	// principals in the same transaction (one ci.mapping.deleted each) and the project owners of the
	// issuer (the foreign key cascades). expected is optional (errs.AnyRevision). A configuration
	// issuer is deletable only after removedFromConfiguration.
	Delete(ctx context.Context, actor Actor, name string, cascade bool, expected int64) error
	// Keys returns the stored key set status without contacting the issuer.
	Keys(ctx context.Context, name string) (KeyStatus, error)
	// RefreshKeys forces a discovery and key refresh now on this replica, outside any transaction,
	// stores the result under the write rule of R-CI-003 (lock the ci_issuer_keys row alone, keep a
	// later fetched_at, notify ci_issuer_keys only when the content changed) and records
	// ci.keys.refreshed or ci.keys.refresh_failed.
	RefreshKeys(ctx context.Context, actor Actor, name string, trigger RefreshTrigger) (KeyStatus, error)

	// Choices lists the issuers read-only for a project Owner: name, kind, issuer URL and
	// public_issuer (Q-CI-26). It reads the in-process copy.
	Choices(ctx context.Context) ([]IssuerChoice, error)
	// Resolve returns the issuer whose `issuer` equals iss exactly, from the in-process copy
	// (cache ci_issuers, areas ci_mappings, SafetyTTL ci.mappings.cache_safety_ttl); ok is false for an
	// unknown issuer. It makes no query on a hit (R-CI-004 step 5).
	Resolve(ctx context.Context, iss string) (Issuer, bool, error)
	// LockShareTx re-reads the issuer row FOR SHARE in the exchange's transaction, bypassing the
	// copy (R-CI-007). The exchange compares version and MappingsVersion with what it evaluated.
	LockShareTx(ctx context.Context, tx meta.Tx, issuerID string) (Issuer, error)
	// Reload replaces the replica's copy of an issuer with a row the exchange read, under the fill
	// rule of R-ARCH-018.
	Reload(ctx context.Context, issuer Issuer)
	// KeySource returns the key lookup of an issuer for subject.Verify. It owns the unknown-kid
	// protocol of R-CI-003 (re-read at most once per issuer, replica and cluster.epoch_poll_interval,
	// single-flight; then a fetch at most once per ci.signing_keys.unknown_kid_refresh_interval) and
	// the fail-closed age check against ci.signing_keys.max_stale (subject.ErrKeysUnavailable).
	KeySource(issuer Issuer) subject.KeySource
	// RefreshDue is the body of the task ci-keys-refresh: it fetches every enabled issuer whose
	// next_refresh_at has passed or that has no row, each outside a transaction and within
	// ci.signing_keys.fetch_timeout, and stores each under the write rule. A failure is recorded in
	// last_error and does not stop the run. It returns the number refreshed.
	RefreshDue(ctx context.Context) (int, error)
	// ApplyDeclared creates or replaces the issuers of ci.issuers by name with source
	// "configuration" and disables, with removed_from_configuration, the configuration issuers no
	// longer declared (Q-CI-16). It runs in the transaction of `trove migrate` (no replica calls it),
	// fails with an error naming both when a declared name or URL belongs to an API issuer, and
	// notifies ci_mappings.
	ApplyDeclared(ctx context.Context, tx meta.Tx, declared []DeclaredIssuer) error
}

// Mappings is the identity-mapping service (R-CI-009, R-CI-019), built by work package W3. Every
// write runs in one READ COMMITTED transaction that takes FOR UPDATE on the issuer's row first,
// increments the issuer's mappings_version, bumps the mapping's version, notifies the change area
// ci_mappings, records its event (ci.mapping.created, .updated, .disabled, .enabled, .deleted with
// the before and after record, journaled as R-CI-020 says) and keeps the mapping's principal in step:
// create inserts the principal row (kind ci_mapping, name <issuer>/<mapping>), enabled mirrors to
// the principal's status, delete removes it. Disabling and deleting revoke the mapping's unexpired
// tokens through [Revoker] in the same transaction. A write checks the save-time rules of
// claims.Check, then the rules that need the database (the policies and groups exist, the
// ci.mappings.max_per_issuer limit, the priority is free in the tier: 409).
//
// Errors: 404 not_found (issuer or mapping), 409 already_exists, 409 priority_taken, 412
// precondition_failed, 422 with the `code` of R-CI-010 to R-CI-014 and the pointer of the member.
type Mappings interface {
	// List returns the issuer's mappings in evaluation order (tier, priority).
	List(ctx context.Context, issuer string) ([]Mapping, error)
	// Get returns one mapping.
	Get(ctx context.Context, issuer, name string) (Mapping, error)
	// Create creates a system-tier mapping and returns it with the warnings of the save.
	Create(ctx context.Context, actor Actor, issuer string, in MappingInput) (MappingResult, error)
	// Replace is a full replacement under If-Match; name is immutable. One event, chosen by enabled.
	Replace(ctx context.Context, actor Actor, issuer, name string, expected int64, in MappingInput) (MappingResult, error)
	// Delete deletes the mapping, revokes its tokens and deletes its principal. expected is optional.
	Delete(ctx context.Context, actor Actor, issuer, name string, expected int64) error
	// The project forms (Q-CI-26) are the same operations on the tier "project:<key>". The HTTP handler
	// has already narrowed to a project the caller is an Owner of (requireProject);
	// the service enforces the bounds of a project mapping on every write: the name is bare and
	// unique within the project's tier (R-CI-009), no groups, policies only the project's own, inline grants whose repositories are the
	// project's (`@any` and patterns mean the project's repositories), never manage, and then the rules
	// of claims.Check with IssuerFacts.Project set. The write takes the issuer row FOR UPDATE and bumps
	// mappings_version like a system write; its events carry `project`.

	// ListIn returns the project's mappings of every issuer, ordered by issuer, priority.
	ListIn(ctx context.Context, project string) ([]Mapping, error)
	// GetIn returns one project mapping; a mapping of another tier is 404.
	GetIn(ctx context.Context, project, issuer, name string) (Mapping, error)
	// CreateIn creates a mapping of the project's tier on the issuer named by in.Issuer.
	CreateIn(ctx context.Context, actor Actor, project string, in MappingInput) (MappingResult, error)
	// ReplaceIn is the full replacement under If-Match; name and issuer are immutable.
	ReplaceIn(ctx context.Context, actor Actor, project, issuer, name string, expected int64, in MappingInput) (MappingResult, error)
	// DeleteIn deletes the mapping, revokes its tokens and deletes its principal.
	DeleteIn(ctx context.Context, actor Actor, project, issuer, name string, expected int64) error
	// EvaluateIn is the dry run over the project's tier only, so the trace shows nothing of another
	// tier (MappingSet.OnlyTier).
	EvaluateIn(ctx context.Context, project, issuer string, req EvaluateRequest) (EvaluateResult, error)
	// CountIn returns the number of CI mappings of a project; the project service refuses to delete a
	// project with any (409 project_not_empty).
	CountIn(ctx context.Context, project string) (int, error)

	// DeleteForIssuerTx deletes every mapping of an issuer and its principal in the caller's transaction,
	// for the cascade of an issuer's deletion (R-CI-002, work package W1 calls it): one
	// ci.mapping.deleted per mapping with the journal entry, the mapping's tokens are left to the
	// issuer's revocation in the same transaction, and the issuer row stays locked by the caller, so the
	// store neither locks it nor bumps its mappings_version. It returns the number deleted.
	DeleteForIssuerTx(ctx context.Context, tx meta.Tx, actor Actor, issuerID, issuerName string) (int, error)

	// Evaluate is the dry run (R-CI-019): [Select] on the issuer's mapping set with the request's
	// claims, then the scope narrowing and the TTL. It issues nothing, records no jti and writes no
	// audit event.
	Evaluate(ctx context.Context, issuer string, req EvaluateRequest) (EvaluateResult, error)
	// Snippet renders the CI snippet of a mapping as text/plain (R-API-035, R-UI-035).
	Snippet(ctx context.Context, issuer string, req SnippetRequest) (string, error)

	// Set returns the issuer's mapping set from the in-process copy (cache ci_mappings, SafetyTTL
	// ci.mappings.cache_safety_ttl) with the versions it was read at; the exchange's first
	// evaluation uses it.
	Set(ctx context.Context, issuerID string) (MappingSet, error)
	// SetTx reads the mapping set from PostgreSQL in the exchange's transaction, with every
	// mapping row FOR SHARE (R-CI-007), and refreshes the copy.
	SetTx(ctx context.Context, tx meta.Tx, issuerID string) (MappingSet, error)
}

// Revoker ends CI tokens (R-CI-017, R-CI-020), built by work package W5 (TokenRevoker in revoke.go). A
// revocation sets revoked_at and the reason on the api_token rows (the reasons of the token table,
// Q-CI-38: mapping_deleted for a mapping, admin for an issuer and the bulk revoke), notifies the change
// area auth with the token hashes so the token cache on every
// replica evicts them within R-AUTH-023's bound, and records ci.token.revoked, at most 1000 token
// IDs per event, with the cause and the journal entry (R-OPS-014). The Tx forms run inside the
// caller's transaction: the writes of W1 and W3 must not leave a window in which a token of a
// disabled or deleted object is usable.
type Revoker interface {
	// RevokeMappingTx revokes the unexpired tokens issued through a mapping (disable, delete).
	RevokeMappingTx(ctx context.Context, tx meta.Tx, actor Actor, mapping Mapping, cause RevokeCause) (Revoked, error)
	// RevokeIssuerTx revokes every unexpired token issued through an issuer (disable, delete).
	RevokeIssuerTx(ctx context.Context, tx meta.Tx, actor Actor, issuerID, issuerName string, cause RevokeCause) (Revoked, error)
	// RevokeMatching is the bulk revoke of POST /api/v1/ci/issuances/revoke: one transaction, the
	// filter of [RevokeFilter], cause bulk.
	RevokeMatching(ctx context.Context, actor Actor, f RevokeFilter) (Revoked, error)
}

// Issuances reads the issuance records and runs the retention tasks (R-CI-020, R-CI-018, R-CI-030),
// built by work package W5. The records are written by the exchange transaction (W4), never here.
type Issuances interface {
	// List returns a page of records, newest first, with their token state joined.
	List(ctx context.Context, f IssuanceFilter) (IssuancePage, error)
	// ListForProject returns the records of tokens whose contributing mappings include a mapping of the
	// project's tier, newest first (Q-CI-26); the filter's issuer is optional. A record shows the
	// project's mappings only, not other mappings that contributed.
	ListForProject(ctx context.Context, project string, f IssuanceFilter) (IssuancePage, error)
	// Get returns one record and its usage summary from the audit store.
	Get(ctx context.Context, tokenID string) (Issuance, Usage, error)
	// CleanupReplay is the body of ci-replay-cleanup: it deletes ci_subject_token_uses rows whose
	// subject_expires_at + the issuer's clock skew + ci.replay.retention_after_expiry has passed, in
	// batches of at most 1000 rows, each in its own transaction, in the order of the expiry index
	// (R-CI-030). Idempotent; stops between batches when ctx ends.
	CleanupReplay(ctx context.Context) (int64, error)
	// RetainIssuances is the body of ci-issuance-retention: it deletes ci_token_issuances rows whose
	// expires_at is older than ci.issuances.retention, in the same batches.
	RetainIssuances(ctx context.Context) (int64, error)
}
