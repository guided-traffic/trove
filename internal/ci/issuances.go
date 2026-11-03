package ci

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/ci/claims"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The issuance records and the two retention tasks (R-CI-020, R-CI-018, R-CI-030, Q-CI-26), work package W5.
// The records are written by the exchange's transaction (W4); this service reads them, newest first with a
// keyset cursor, joins the token state (a token row deleted by its retention task reads as expired) and the usage
// summary from the audit store, and deletes what the retention settings let go.

// MaxCleanupBatch is the most rows one transaction of a retention task deletes (R-CI-030).
const MaxCleanupBatch = 1000

// IssuanceOptions configure [NewIssuances].
type IssuanceOptions struct {
	DB Database
	// Config is the ci section: ci.replay.retention_after_expiry and ci.issuances.retention.
	Config config.CI
}

// IssuanceStore implements [Issuances].
type IssuanceStore struct{ o IssuanceOptions }

var _ Issuances = (*IssuanceStore)(nil)

// NewIssuances creates the service.
func NewIssuances(o IssuanceOptions) (*IssuanceStore, error) {
	if o.DB == nil {
		return nil, errors.New("ci: the issuance service needs the database")
	}
	return &IssuanceStore{o: o}, nil
}

// IssuanceCursor is the position of a page: the (issued_at, token_id) of the last record of the previous page.
type IssuanceCursor struct {
	IssuedAt time.Time
	TokenID  string
}

// List returns a page of records, newest first (R-CI-020). f.Limit is the number of rows to read, one more than
// the page, so the caller knows whether a next page exists.
func (s *IssuanceStore) List(ctx context.Context, f IssuanceFilter) (IssuancePage, error) {
	after, err := decodeAfter(f.Cursor)
	if err != nil {
		return IssuancePage{}, err
	}
	var rows []q.CiIssuanceListRow
	err = s.o.DB.Read(ctx, func(db q.DBTX) error {
		var err error
		rows, err = q.New(db).CiIssuanceList(ctx, q.CiIssuanceListParams{
			Issuer: f.Issuer, ProjectID: f.ProjectID, ProjectPath: f.ProjectPath, PipelineID: f.PipelineID, JobID: f.JobID,
			Mapping: f.Mapping, Active: activeArg(f.Active), FromUs: microsOf(f.From), ToUs: microsOf(f.To),
			AfterUs: after.us, AfterID: after.id, MaxRows: limitArg(f.Limit),
		})
		return err
	})
	if err != nil {
		return IssuancePage{}, err
	}
	out := IssuancePage{Items: make([]Issuance, 0, len(rows))}
	for _, r := range rows {
		out.Items = append(out.Items, issuanceOf(r))
	}
	return out, nil
}

// ListForProject returns the records of tokens whose contributing mappings include a mapping of the project's
// tier, newest first (Q-CI-26). A record shows the project's mappings only: the names and IDs of the others and
// their contributions are left out, so a project administrator learns nothing of another tier's mappings.
func (s *IssuanceStore) ListForProject(ctx context.Context, project string, f IssuanceFilter) (IssuancePage, error) {
	after, err := decodeAfter(f.Cursor)
	if err != nil {
		return IssuancePage{}, err
	}
	var rows []q.CiIssuanceListForProjectRow
	err = s.o.DB.Read(ctx, func(db q.DBTX) error {
		qs := q.New(db)
		ids, err := qs.CiProjectIDByKey(ctx, project)
		if err != nil || len(ids) == 0 {
			return err
		}
		rows, err = qs.CiIssuanceListForProject(ctx, q.CiIssuanceListForProjectParams{
			ProjectID: ids[0], Issuer: f.Issuer, JobID: f.JobID, Active: activeArg(f.Active),
			AfterUs: after.us, AfterID: after.id, MaxRows: limitArg(f.Limit),
		})
		return err
	})
	if err != nil {
		return IssuancePage{}, err
	}
	out := IssuancePage{Items: make([]Issuance, 0, len(rows))}
	for _, r := range rows {
		out.Items = append(out.Items, onlyProject(issuanceOf(q.CiIssuanceListRow(r)), ProjectTier(project)))
	}
	return out, nil
}

// projectHiddenClaims are the claims of the snapshot a project's view leaves out (Q-CI-52): the person who started the
// pipeline, the token ID of the subject token and the runner. A project's mapping matched the job, so the job's
// identity (project, pipeline, job, ref, sha, environment, source) stays; who ran it and on what is the system
// administrators' business. A function, not a package variable, so no caller can change the list (R-ARCH-032).
func projectHiddenClaims() []string {
	return []string{"user_id", "user_login", "jti", "runner_id", "runner_environment"}
}

// onlyProject narrows a record to the contributions of one tier (Q-CI-26, Q-CI-52). The mapping names and IDs, the
// grants bundle and the cross-project flag follow the kept contributions. The two scope strings are left out: they
// list the items of every tier and cannot be rebuilt from one tier's contributions (the kept inline grants show what
// the project's mapping gave). The person, the subject token's ID and the client are left out of the record and of the
// claim snapshot.
func onlyProject(is Issuance, tier string) Issuance {
	var keep claims.Frozen
	keep.Version, keep.Issuer, keep.IssuerID = is.Grants.Version, is.Grants.Issuer, is.Grants.IssuerID
	keep.ProjectID, keep.ProjectPath, keep.CrossProject = is.Grants.ProjectID, is.Grants.ProjectPath, is.Grants.CrossProject
	is.MappingIDs, is.MappingNames = nil, nil
	for _, m := range is.Grants.Contributions {
		if m.Tier != tier {
			continue
		}
		keep.Add(m)
		is.MappingIDs = append(is.MappingIDs, m.ID)
		is.MappingNames = append(is.MappingNames, mappingName(m.Ref))
	}
	is.Grants = keep
	is.GrantedScope, is.RequestedScope = "", ""
	is.JTI, is.UserID, is.UserLogin, is.ClientIP, is.UserAgent = "", "", "", "", ""
	narrowed := make(map[string]any, len(is.Claims))
	for name, v := range is.Claims {
		if !slices.Contains(projectHiddenClaims(), name) {
			narrowed[name] = v
		}
	}
	is.Claims = narrowed
	return is
}

// mappingName is the mapping's name of a reference "ci:<issuer>/<mapping>".
func mappingName(ref string) string {
	for i := len(ref) - 1; i >= 0; i-- {
		if ref[i] == '/' {
			return ref[i+1:]
		}
	}
	return ref
}

// Get returns one record and its usage summary from the audit store: the events the token is the credential of
// within its lifetime, the last of them, and the package versions it published (R-CI-020). An unknown ID is 404.
func (s *IssuanceStore) Get(ctx context.Context, tokenID string) (Issuance, Usage, error) {
	var is Issuance
	var use Usage
	err := s.o.DB.Read(ctx, func(db q.DBTX) error {
		qs := q.New(db)
		rows, err := qs.CiIssuanceGet(ctx, tokenID)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return errs.E(errs.NotFound, errs.CodeNotFound, "The issuance record does not exist.")
		}
		is = issuanceOf(q.CiIssuanceListRow(rows[0]))
		// The window: the token's life, and a minute after its end for the events of requests that were
		// decided before it ended.
		from, to := is.IssuedAt.UnixMicro(), is.ExpiresAt.Add(time.Minute).UnixMicro()
		u, err := qs.CiIssuanceUsage(ctx, q.CiIssuanceUsageParams{TokenID: tokenID, FromUs: from, ToUs: to})
		if err != nil {
			return err
		}
		use = Usage{Requests: u.Requests, PackagesDeployed: []string{}}
		if u.LastUs > 0 {
			t := time.UnixMicro(u.LastUs).UTC()
			use.LastUsedAt = &t
		}
		deployed, err := qs.CiIssuanceDeployed(ctx, q.CiIssuanceDeployedParams{TokenID: tokenID, FromUs: from, ToUs: to})
		if err != nil {
			return err
		}
		for _, d := range deployed {
			name := d.Repository + "/" + d.Package
			if d.Version != "" {
				name += "@" + d.Version
			}
			if !slices.Contains(use.PackagesDeployed, name) {
				use.PackagesDeployed = append(use.PackagesDeployed, name)
			}
		}
		return nil
	})
	return is, use, err
}

// CleanupReplay is the body of ci-replay-cleanup (R-CI-018, R-CI-030): it deletes the replay rows whose
// subject_expires_at + the issuer's clock skew + ci.replay.retention_after_expiry has passed, in batches of at
// most MaxCleanupBatch rows, each in its own transaction, in the order of the expiry index, until a batch
// deletes fewer or ctx ends. A run that stops part-way leaves only rows the next run deletes.
func (s *IssuanceStore) CleanupReplay(ctx context.Context) (int64, error) {
	secs := int64(s.o.Config.Replay.RetentionAfterExpiry / time.Second)
	return s.batches(ctx, func(qs *q.Queries) (int64, error) {
		return qs.CiReplayDeleteExpired(ctx, q.CiReplayDeleteExpiredParams{RetentionSecs: secs, MaxRows: MaxCleanupBatch})
	})
}

// RetainIssuances is the body of ci-issuance-retention (R-CI-020, R-CI-030): it deletes the records whose
// expires_at is older than ci.issuances.retention, in the same batches.
func (s *IssuanceStore) RetainIssuances(ctx context.Context) (int64, error) {
	secs := int64(s.o.Config.Issuances.Retention / time.Second)
	return s.batches(ctx, func(qs *q.Queries) (int64, error) {
		return qs.CiIssuanceDeleteExpired(ctx, q.CiIssuanceDeleteExpiredParams{RetentionSecs: secs, MaxRows: MaxCleanupBatch})
	})
}

// batches runs one deletion per transaction until it deletes fewer than a full batch. Each deletion is one
// statement outside an explicit transaction, so it is its own transaction and the connection is never idle in a
// transaction between a BEGIN and the statement (R-ARCH-019); it runs through Read for its retry on a failover,
// which is safe because the deletion is idempotent, and so a repeated run has no other effect.
func (s *IssuanceStore) batches(ctx context.Context, del func(*q.Queries) (int64, error)) (int64, error) {
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		var n int64
		err := s.o.DB.Read(ctx, func(db q.DBTX) error {
			var err error
			n, err = del(q.New(db))
			return err
		})
		if err != nil {
			return total, err
		}
		total += n
		if n < MaxCleanupBatch {
			return total, nil
		}
	}
}

// ActiveTokens counts the unexpired, unrevoked CI tokens per issuer, 0 for an issuer without one: the database
// gauge trove_ci_tokens_active (R-CI-020, R-OPS-007), which every replica evaluates once per
// metrics.db_gauge_interval and dashboards aggregate with max.
func ActiveTokens(ctx context.Context, db Database) (map[string]int64, error) {
	out := map[string]int64{}
	err := db.Read(ctx, func(db q.DBTX) error {
		rows, err := q.New(db).CiTokensActive(ctx)
		for _, r := range rows {
			out[r.Name] = r.Active
		}
		return err
	})
	return out, err
}

func issuanceOf(r q.CiIssuanceListRow) Issuance {
	is := Issuance{
		TokenID: r.TokenID, Issuer: r.Issuer, IssuerID: r.IssuerID, MappingIDs: r.MappingIds, MappingNames: r.MappingNames,
		SubjectKind: r.SubjectKind, JTI: r.Jti, ProjectID: r.ProjectID, ProjectPath: r.ProjectPath, NamespaceID: r.NamespaceID,
		PipelineID: r.PipelineID, JobID: r.JobID, Ref: r.Ref, RefType: r.RefType, PipelineSource: r.PipelineSource,
		Environment: r.Environment, SHA: r.Sha, UserID: r.UserID, UserLogin: r.UserLogin, RequestedScope: r.RequestedScope,
		GrantedScope: r.GrantedScope, CrossProject: r.CrossProject, IssuedAt: time.UnixMicro(r.IssuedUs).UTC(),
		ExpiresAt: time.UnixMicro(r.ExpiresUs).UTC(), ClientIP: r.ClientIp, UserAgent: r.UserAgent, State: r.State,
	}
	switch r.RefProtected {
	case "true":
		t := true
		is.RefProtected = &t
	case "false":
		f := false
		is.RefProtected = &f
	}
	if r.RevokedUs > 0 {
		t := time.UnixMicro(r.RevokedUs).UTC()
		is.RevokedAt = &t
	}
	// The snapshot and the bundle are the stored JSON; a record that does not decode shows them empty rather
	// than failing the list (the token's use decodes its own copy and fails closed).
	_ = json.Unmarshal(r.Claims, &is.Claims)
	_ = json.Unmarshal(r.Grants, &is.Grants)
	return is
}

// after is a decoded cursor.
type after struct {
	us int64
	id string
}

// IssuanceSortKey is the sort key of a record, the values the API's opaque cursor carries (R-API-010): the
// issue instant in microseconds and the token ID. IssuanceFilter.Cursor is the key joined by a comma.
func IssuanceSortKey(is Issuance) []string {
	return []string{strconv.FormatInt(is.IssuedAt.UnixMicro(), 10), is.TokenID}
}

func decodeAfter(cursor string) (after, error) {
	if cursor == "" {
		return after{}, nil
	}
	usText, id, ok := strings.Cut(cursor, ",")
	us, err := strconv.ParseInt(usText, 10, 64)
	if !ok || err != nil || us <= 0 || !principal.IsID(id) {
		return after{}, errs.E(errs.Invalid, errs.CodeInvalidCursor, "The cursor is not valid for this request. Start the traversal again without a cursor.")
	}
	return after{us: us, id: id}, nil
}

func activeArg(b *bool) string {
	switch {
	case b == nil:
		return ""
	case *b:
		return "true"
	}
	return "false"
}

func microsOf(t *time.Time) int64 {
	if t == nil {
		return 0
	}
	return t.UnixMicro()
}

func limitArg(n int) int32 {
	if n <= 0 || n > 1001 {
		n = 101
	}
	return int32(n) //nolint:gosec // bounded above
}
