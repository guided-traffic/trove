package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/guided-traffic/trove/internal/api/edge"
	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/mirror"
	"github.com/guided-traffic/trove/internal/repository"
)

// The mirror's status and run operations (R-REPO-056, work package D of docs/developer/mirror.md). The status is read from
// PostgreSQL by whichever replica answers, so it is the same on every replica; the run records a request in the columns
// pass_requested_* of mirror_state and enqueues one job of the kind mirror.KindPass, both in the transaction that also
// writes the audit event mirror.pass_requested. No operation here writes another column of mirror_state: the engine owns
// them, and a pass started by the run reads the request when it claims the repository.
//
// Neither operation can be declared as one permission or one repository action, because each allows a disjunction the
// declaration forms cannot express (repositories:read-config, manage on the repository, or administration of its project;
// for the run the project's switch manageRepositories), so both declare only that the caller is authenticated and decide
// here, as the cache entry list does (Q-OPS-155, Q-API-199).

// MirrorServices are the services of the mirror operations; the zero value reads and writes through Services.Store.
type MirrorServices struct {
	// State reads the status row and records a request. Nil: the PostgreSQL implementation over Services.Store.
	State MirrorState
	// Repositories looks a repository up with its remote section. Nil: Repositories.Service.
	Repositories MirrorRepositories
}

// MirrorTarget is the repository a mirror operation addresses: its numeric ID, its class and, for a remote one, the
// effective remote section (R-REPO-050).
type MirrorTarget struct {
	ID       int64
	Key      string
	Class    repository.Class
	Settings repository.RemoteSettings
}

// MirrorRepositories looks a repository up by key; 404 repository_not_found when there is none.
type MirrorRepositories interface {
	Target(ctx context.Context, key string) (MirrorTarget, error)
}

// serviceTargets implements MirrorRepositories over the repository service.
type serviceTargets struct{ svc *repository.Service }

func (t serviceTargets) Target(ctx context.Context, key string) (MirrorTarget, error) {
	st, err := t.svc.Get(ctx, key)
	if err != nil {
		return MirrorTarget{}, err
	}
	id, err := parseRepositoryID(st.Repository.ID)
	if err != nil {
		return MirrorTarget{}, err
	}
	out := MirrorTarget{ID: id, Key: st.Repository.Name, Class: st.Repository.Class}
	if out.Class != repository.Remote {
		return out, nil
	}
	rs, ok := st.Repository.Remote()
	if !ok {
		return MirrorTarget{}, errs.E(errs.Internal, errs.CodeInternal, "The remote section of the repository could not be read.")
	}
	out.Settings = rs
	return out, nil
}

// MirrorState is the database access of the mirror operations.
type MirrorState interface {
	// Status reads the row of one repository, whether or not the dispatch has created its mirror_state row yet.
	Status(ctx context.Context, repoID int64) (MirrorRow, error)
	// RequestPass records a request of the run operation in tx: the columns pass_requested_* and no other.
	RequestPass(ctx context.Context, tx meta.Tx, repoID int64, requestedBy string, full bool) error
}

// MirrorRow is what the status reads: the mirror_state row (HasState false: none yet, every other member zero), the job
// that owns the pass in progress, the pending names and the repository's usage.
type MirrorRow struct {
	HasState bool

	NextPassAt     *time.Time
	LastFullPassAt *time.Time
	// LastPass is the stored summary of the last complete pass, a JSON object of the members of the schema MirrorPassSummary.
	LastPass []byte

	PassStartedAt     *time.Time
	PassKind          string
	PassMethod        string
	PassJobID         string
	PassDiscoveryDone bool
	PassNames         int64
	PassPackagesDone  int64
	PassPackagesFail  int64
	PassItemsStored   int64
	PassBytesStored   int64

	ConsecutiveFailures int
	LastErrorAt         *time.Time
	LastErrorCode       string
	LastError           string

	RateLimitedUntil    *time.Time
	RateLimitWaits      int64
	RateLimitWaitSecs   int64
	LastRateLimitAt     *time.Time
	LastRateLimitStatus *int

	BoundReached   string
	BoundReachedAt *time.Time

	// JobKind and JobState describe the job of PassJobID; empty when the row of the job is gone.
	JobKind  string
	JobState string

	Pending   int64
	UsedBytes int64
}

// pgMirrorState is the PostgreSQL implementation of MirrorState.
type pgMirrorState struct{ store Store }

// timeOf is the UTC time of a nullable timestamp, nil when it is NULL. The driver's types stay out of this package
// (R-ARCH-026), so the caller passes the two members.
func timeOf(valid bool, t time.Time) *time.Time {
	if !valid {
		return nil
	}
	u := t.UTC()
	return &u
}

func (p pgMirrorState) Status(ctx context.Context, repoID int64) (MirrorRow, error) {
	var r q.MirrorStatusReadRow
	err := p.store.Read(ctx, func(db q.DBTX) error {
		rows, err := q.New(db).MirrorStatusRead(ctx, q.MirrorStatusReadParams{RepoID: repoID, MaxAttempts: mirror.MaxPackageAttempts})
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			return errors.New("the mirror status read returned no row")
		}
		r = rows[0]
		return nil
	})
	if err != nil {
		return MirrorRow{}, err
	}
	row := MirrorRow{
		HasState:          r.HasState,
		NextPassAt:        timeOf(r.NextPassAt.Valid, r.NextPassAt.Time),
		LastFullPassAt:    timeOf(r.LastFullPassAt.Valid, r.LastFullPassAt.Time),
		LastPass:          r.LastPass,
		PassStartedAt:     timeOf(r.PassStartedAt.Valid, r.PassStartedAt.Time),
		PassKind:          r.PassKind.String,
		PassMethod:        r.PassMethod.String,
		PassDiscoveryDone: r.PassDiscoveryDone.Bool,
		PassNames:         r.PassNames.Int64,
		PassPackagesDone:  r.PassPackagesDone.Int64,
		PassPackagesFail:  r.PassPackagesFailed.Int64,
		PassItemsStored:   r.PassItemsStored.Int64,
		PassBytesStored:   r.PassBytesStored.Int64,

		ConsecutiveFailures: int(r.ConsecutiveFailures.Int32),
		LastErrorAt:         timeOf(r.LastErrorAt.Valid, r.LastErrorAt.Time),
		LastErrorCode:       r.LastErrorCode.String,
		LastError:           r.LastError.String,

		RateLimitedUntil:  timeOf(r.RateLimitedUntil.Valid, r.RateLimitedUntil.Time),
		RateLimitWaits:    r.RateLimitWaits.Int64,
		RateLimitWaitSecs: r.RateLimitWaitSeconds.Int64,
		LastRateLimitAt:   timeOf(r.LastRateLimitAt.Valid, r.LastRateLimitAt.Time),

		BoundReached:   r.BoundReached.String,
		BoundReachedAt: timeOf(r.BoundReachedAt.Valid, r.BoundReachedAt.Time),

		JobKind: r.JobKind.String, JobState: r.JobState,
		Pending: r.Pending, UsedBytes: r.UsedBytes,
	}
	if r.LastRateLimitStatus.Valid {
		st := int(r.LastRateLimitStatus.Int32)
		row.LastRateLimitStatus = &st
	}
	if r.PassJobID.Valid {
		row.PassJobID = r.PassJobID.String()
	}
	return row, nil
}

func (pgMirrorState) RequestPass(ctx context.Context, tx meta.Tx, repoID int64, requestedBy string, full bool) error {
	ptx, ok := postgres.Unwrap(tx)
	if !ok {
		return errors.New("the mirror request needs a transaction of internal/meta/postgres")
	}
	return q.New(ptx).MirrorRequestPass(ctx, q.MirrorRequestPassParams{RepoID: repoID, RequestedBy: requestedBy, FullPass: full})
}

func (s *Server) mirrorState() (MirrorState, error) {
	if st := s.o.Mirror.State; st != nil {
		return st, nil
	}
	if s.o.API.Store == nil {
		return nil, unavailable("The mirror status")
	}
	return pgMirrorState{store: s.o.API.Store}, nil
}

// requireMirrorReader decides who reads the status: a holder of repositories:read-config, an administrator of the
// repository's project, or a caller with manage on the repository (R-REPO-056, as the full configuration read of
// R-REPO-010). The exposure ceiling of the request's endpoint binds a configuration reader like everyone (R-EP-013), and
// a repository hidden from a caller with neither answers 404 (R-AUTHZ-016) through the decision on manage.
func (s *Server) requireMirrorReader(ctx context.Context, key string) error {
	ctx, acc, err := requestHolder(ctx, s.o.Repositories.Authz, key)
	if err != nil {
		return err
	}
	if acc == nil {
		return errNoDecisionPoint()
	}
	cfg, err := s.readsConfig(ctx, key)
	if err != nil {
		return err
	}
	if cfg {
		return exposed(acc, key)
	}
	return hiddenOr(ctx, acc, key, acc.Decide(ctx, manageCoordinate(key)).Err())
}

func manageCoordinate(key string) authz.Coordinate {
	return authz.Coordinate{Repository: key, Kind: authz.KindWrite, Actions: []authz.Action{authz.Manage}}
}

// hiddenOr turns the refusal of manage into the answer of R-AUTHZ-016: 404 where the caller cannot even read the
// repository and the repository hides its existence, otherwise the refusal itself. The decision on manage alone cannot say
// it, because the table of R-AUTHZ-016 answers a refused manage with 403 whatever else the caller holds; the read decides
// whether the repository is visible. It returns nil for a nil refusal.
func hiddenOr(ctx context.Context, acc *edge.Access, key string, refusal error) error {
	if refusal == nil {
		return nil
	}
	if d := acc.Decide(ctx, authz.Coordinate{Repository: key, Kind: authz.KindMetadata, Actions: []authz.Action{authz.Read}}); d.Outcome == authz.DenyHidden {
		return d.Err()
	}
	return refusal
}

// requireMirrorWriter decides who requests a pass: a holder of repositories:write-config, an administrator of the
// repository's project with manageRepositories, or a caller with manage on the repository (R-REPO-056, as an update of
// R-REPO-011). An administrator of a project holds manage on every repository of the project whatever its switches say
// (Q-AUTHZ-183), so the third way admits it too: the switch gates the changes of a configuration, and a run changes none
// (Q-API-199). A caller that none of these fits is answered 403, or 404 where the repository is hidden from it
// (R-AUTHZ-016); a holder of repositories:read-config sees every repository and is answered 403.
func (s *Server) requireMirrorWriter(ctx context.Context, key string) error {
	ctx, acc, err := requestHolder(ctx, s.o.Repositories.Authz, key)
	if err != nil {
		return err
	}
	if acc == nil {
		return errNoDecisionPoint()
	}
	held, err := s.holds(ctx, capabilityWriteConfig)
	if err != nil {
		return err
	}
	if held {
		return exposed(acc, key)
	}
	if pa, ok := s.o.Access.(ProjectAccess); ok {
		if _, admin, err := pa.AdministersRepository(ctx, key, authz.ProjectManageRepositories); err != nil {
			return err
		} else if admin {
			return exposed(acc, key)
		}
	}
	refusal := acc.Decide(ctx, manageCoordinate(key)).Err()
	if refusal == nil {
		return nil
	}
	cfg, err := s.readsConfig(ctx, key)
	if err != nil {
		return err
	}
	if cfg {
		return refusal
	}
	return hiddenOr(ctx, acc, key, refusal)
}

// exposed answers 404 for a repository the endpoint of the request does not expose (R-EP-013, R-EP-014).
func exposed(acc *edge.Access, key string) error {
	if len(acc.Exposed([]string{key})) == 0 {
		return authz.NotFound()
	}
	return nil
}

// mirrorRemote loads the repository and answers 400 not_applicable for a class other than remote.
func (s *Server) mirrorRemote(ctx context.Context, key string) (MirrorTarget, error) {
	repos := s.o.Mirror.Repositories
	if repos == nil {
		svc, err := s.repositoryService()
		if err != nil {
			return MirrorTarget{}, err
		}
		repos = serviceTargets{svc: svc}
	}
	t, err := repos.Target(ctx, key)
	if err != nil {
		return MirrorTarget{}, err
	}
	if t.Class != repository.Remote {
		return MirrorTarget{}, errs.E(errs.Invalid, errs.CodeNotApplicable,
			"Only a remote repository has a mirror; "+strconv.Quote(key)+" is a "+string(t.Class)+" repository.")
	}
	return t, nil
}

// GetRepositoryMirror implements GET /api/v1/repositories/{key}/mirror (R-REPO-056).
func (s *Server) GetRepositoryMirror(w http.ResponseWriter, r *http.Request, key string) {
	ctx := r.Context()
	state, err := s.mirrorState()
	if err == nil {
		err = s.requireMirrorReader(ctx, key)
	}
	var t MirrorTarget
	if err == nil {
		t, err = s.mirrorRemote(ctx, key)
	}
	var row MirrorRow
	if err == nil {
		row, err = state.Status(ctx, t.ID)
	}
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "api.getRepositoryMirror"))
		return
	}
	writeJSON(w, r, http.StatusOK, buildMirrorStatus(t.Settings, row))
}

// buildMirrorStatus is the status document of R-REPO-056 from the repository's remote section and the row read.
func buildMirrorStatus(rs repository.RemoteSettings, row MirrorRow) gen.MirrorStatus {
	out := gen.MirrorStatus{
		Mode:           gen.MirrorStatusMode(modeOf(rs)),
		State:          mirrorStateOf(rs, row),
		NextPassAt:     row.NextPassAt,
		LastFullPassAt: row.LastFullPassAt,
		Errors: gen.MirrorErrors{
			ConsecutiveFailures: row.ConsecutiveFailures, LastAt: row.LastErrorAt,
			LastCode: strPtr(row.LastErrorCode), LastMessage: strPtr(row.LastError),
		},
		RateLimit: gen.MirrorRateLimit{
			Waits: row.RateLimitWaits, WaitSeconds: row.RateLimitWaitSecs, LastAt: row.LastRateLimitAt,
			LastStatus: row.LastRateLimitStatus, Until: aheadOf(row.RateLimitedUntil, time.Now()),
		},
		Bound: gen.MirrorBound{At: row.BoundReachedAt, MaxBytes: rs.Mirror.MaxBytes, UsedBytes: row.UsedBytes},
	}
	if row.BoundReached != "" {
		k := gen.MirrorBoundKind(row.BoundReached)
		out.Bound.Reached = &k
	}
	if row.PassStartedAt != nil {
		p := gen.MirrorPass{
			Kind: gen.MirrorPassKind(row.PassKind), StartedAt: *row.PassStartedAt, NamesDiscovered: row.PassNames,
			PackagesDone: row.PassPackagesDone, PackagesPending: row.Pending, PackagesFailed: row.PassPackagesFail,
			ItemsStored: row.PassItemsStored, BytesStored: row.PassBytesStored, DiscoveryDone: row.PassDiscoveryDone,
		}
		if row.PassMethod != "" {
			m := gen.MirrorMethod(row.PassMethod)
			p.Method = &m
		}
		if row.PassJobID != "" && row.JobKind != "" {
			ref := jobReference(row.PassJobID, row.JobKind, row.JobState)
			p.Job = &ref
		}
		out.Pass = &p
	}
	out.LastCompletePass = decodeLastPass(row.LastPass)
	return out
}

func modeOf(rs repository.RemoteSettings) string {
	if rs.Mirrored() {
		return repository.ModeMirror
	}
	return repository.ModePullThrough
}

// aheadOf returns t only while it lies ahead of now: a wait that is over is no rate limit of the status (R-REPO-056).
func aheadOf(t *time.Time, now time.Time) *time.Time {
	if t == nil || !t.After(now) {
		return nil
	}
	return t
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// mirrorStateOf derives the state of R-REPO-056. The first that holds wins: off (mode pullThrough), paused, failing (three
// or more failures in a row), bound_reached, running (a pass in progress whose job runs), waiting (a pass in progress and no
// job that runs it), scheduled (Q-API-196).
func mirrorStateOf(rs repository.RemoteSettings, row MirrorRow) gen.MirrorStatusState {
	switch {
	case !rs.Mirrored():
		return gen.MirrorStatusStateOff
	case rs.Mirror.Paused:
		return gen.MirrorStatusStatePaused
	case row.ConsecutiveFailures >= mirror.FailingFailures:
		return gen.MirrorStatusStateFailing
	case row.BoundReached != "":
		return gen.MirrorStatusStateBoundReached
	case row.PassStartedAt == nil:
		return gen.MirrorStatusStateScheduled
	case row.JobState == string(gen.JobStateRunning) || row.JobState == string(gen.JobStateCancelling):
		return gen.MirrorStatusStateRunning
	default:
		return gen.MirrorStatusStateWaiting
	}
}

// decodeLastPass reads the summary the engine stored for the last complete pass. A missing, null or unreadable summary is
// no last pass: the status must not fail for a document it only shows (Q-API-197).
func decodeLastPass(raw []byte) *gen.MirrorPassSummary {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var sum gen.MirrorPassSummary
	if err := json.Unmarshal(raw, &sum); err != nil || sum.Outcome == "" {
		return nil
	}
	sum.StartedAt, sum.FinishedAt = sum.StartedAt.UTC(), sum.FinishedAt.UTC()
	return &sum
}

// RunRepositoryMirror implements POST /api/v1/repositories/{key}/mirror/run (R-REPO-056).
func (s *Server) RunRepositoryMirror(w http.ResponseWriter, r *http.Request, key string, _ gen.RunRepositoryMirrorParams) {
	ctx := r.Context()
	state, err := s.mirrorState()
	if err == nil && (s.idem == nil || s.o.API.Queue == nil) {
		err = unavailable("The mirror run")
	}
	if err == nil {
		err = s.requireMirrorWriter(ctx, key)
	}
	var in gen.MirrorRunRequest
	var body []byte
	if err == nil {
		body, err = readOptionalBody(w, r, s.bodyLimit())
	}
	if err == nil && body != nil {
		if err = requireJSON(r, MediaJSON); err == nil {
			err = StrictUnmarshal(body, &in)
		}
	}
	var t MirrorTarget
	if err == nil {
		t, err = s.mirrorRemote(ctx, key)
	}
	switch {
	case err != nil:
	case !t.Settings.Mirrored():
		err = errs.E(errs.Conflict, errs.CodeMirrorOff, "The repository "+strconv.Quote(key)+" runs in mode pullThrough and has no mirror; set remote.mode to mirror first.")
	case t.Settings.Mirror.Paused:
		err = errs.E(errs.Conflict, errs.CodeMirrorPaused, "The mirror of "+strconv.Quote(key)+" is paused; set remote.mirror.paused to false first.")
	default:
		err = mirrorRateLimitRefusal(ctx, state, t, time.Now())
	}
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "api.runRepositoryMirror"))
		return
	}
	full := in.Full != nil && *in.Full
	err = s.idem.Do(w, r, body, IdempotentOp{Run: func(ctx context.Context, tx meta.Tx) (Outcome, error) {
		return s.enqueueMirrorPass(ctx, r, tx, state, t, full)
	}})
	if err != nil {
		httpserver.Problem(w, r, err)
	}
}

// enqueueMirrorPass enqueues the pass first, so that a pass that is queued or running answers 409 before anything is
// written (a refusal outcome commits with the idempotency row), then records the request and the audit event in the same
// transaction.
func (s *Server) enqueueMirrorPass(ctx context.Context, r *http.Request, tx meta.Tx, state MirrorState, t MirrorTarget, full bool) (Outcome, error) {
	res, err := s.o.API.Queue.Enqueue(ctx, tx, jobs.EnqueueSpec{
		Kind: mirror.KindPass, Args: mirror.PassArgs{Repository: t.Key, RepositoryID: t.ID, Full: full}, DedupKey: mirror.PassKey(t.ID),
		TargetKind: "repository", TargetKey: t.Key, CreatedBy: callerID(ctx),
	})
	if err != nil {
		return Outcome{}, err
	}
	ref := jobReference(res.Job.ID, res.Job.Kind, res.Job.State)
	if !res.Created {
		return Outcome{}, errs.E(errs.Conflict, errs.CodeMirrorPassRunning,
			"A pass of the mirror of "+strconv.Quote(t.Key)+" is queued or running: job "+res.Job.ID+".", errs.WithExtension("job", ref))
	}
	if err := state.RequestPass(ctx, tx, t.ID, callerID(ctx), full); err != nil {
		return Outcome{}, err
	}
	details, _ := json.Marshal(map[string]any{"full": full, "job": res.Job.ID})
	if err := s.recorder().RecordTx(ctx, tx, audit.Event{
		Type: audit.EventMirrorPassRequested, Actor: ActorFrom(httpserver.PrincipalFrom(ctx)), Outcome: audit.Success, Source: auditSource(r),
		Subject: audit.Subject{Kind: "repository", ID: strconv.FormatInt(t.ID, 10), Name: t.Key}, Repository: t.Key, Details: details,
	}); err != nil {
		return Outcome{}, err
	}
	return Outcome{
		Status: http.StatusAccepted,
		Header: map[string]string{"Location": jobLocation(r, res.Job.ID)},
		Body:   gen.JobReferenceEnvelope{Job: ref},
	}, nil
}

// mirrorRateLimitRefusal answers 409 mirror_rate_limited while the upstream's wait lies ahead: a pass that is started now would
// be skipped until it is over (R-REPO-054, Q-REPO-187).
func mirrorRateLimitRefusal(ctx context.Context, state MirrorState, t MirrorTarget, now time.Time) error {
	row, err := state.Status(ctx, t.ID)
	if err != nil {
		return err
	}
	until := aheadOf(row.RateLimitedUntil, now)
	if until == nil {
		return nil
	}
	return errs.E(errs.Conflict, errs.CodeMirrorRateLimited,
		"The upstream of "+strconv.Quote(t.Key)+" asked the mirror to wait until "+until.UTC().Format(time.RFC3339)+"; request the pass again after it.",
		errs.WithExtension("until", until.UTC().Format(time.RFC3339)))
}
