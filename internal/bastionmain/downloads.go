package bastionmain

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

var errNotPostgres = errors.New("bastionmain: the transaction does not come from the PostgreSQL store")

var (
	sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	uuidPattern   = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// IntakeOptions configure [NewIntake].
type IntakeOptions struct {
	// DB resolves the kinds of the principals the events name.
	DB Reader
	// Audit receives the events; nil discards them.
	Audit audit.Recorder
	// MaxBatch and MaxAge are bastions.downloads.max_batch and bastions.downloads.max_age.
	MaxBatch int
	MaxAge   time.Duration
	// Record is audit.record_downloads. Off, the intake answers and records nothing (R-BAS-016).
	Record bool
	Now    func() time.Time
}

// Intake accepts the download events a Bastion served from its cache (R-BAS-016).
type Intake struct {
	o IntakeOptions
}

// NewIntake returns the intake.
func NewIntake(o IntakeOptions) *Intake {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Audit == nil {
		o.Audit = audit.Nop{}
	}
	return &Intake{o: o}
}

// Records reports audit.record_downloads: whether the intake records the events it accepts. The handshake tells
// the Bastion, which then does not send (R-BAS-010).
func (in *Intake) Records() bool { return in.o.Record }

// Outcome says what became of a batch.
type Outcome struct {
	// Recorded events were handed to the audit writer as artifact.downloaded.
	Recorded int
	// Expired events were older than bastions.downloads.max_age, Invalid ones violated the contract of the
	// event (no repository or path, a zero time, a SHA-256 that is no digest); both count as dropped.
	Expired, Invalid int
	// Disabled is true when audit.record_downloads is off: nothing was recorded.
	Disabled bool
}

// Accept records the events of a batch (R-BAS-016). A batch above max_batch is refused as a whole (422); an
// event whose time lies in the future is recorded at the time of receipt, one older than max_age or one that
// breaks the contract of the event is dropped and counted. A batch that reports lost events, or loses events
// here, writes one bastion.downloads.dropped with the total, so the loss is in the audit trail itself.
func (in *Intake) Accept(ctx context.Context, rec Recognition, req iface.DownloadsRequest) (Outcome, error) {
	if !in.o.Record {
		return Outcome{Disabled: true}, nil
	}
	if len(req.Events) > in.o.MaxBatch {
		return Outcome{}, errs.E(errs.Invalid, errs.CodeInvalid, "The batch holds more events than bastions.downloads.max_batch allows.",
			errs.WithFields(errs.FieldError{Pointer: "/events", Code: "too_many", Detail: "at most " + strconv.Itoa(in.o.MaxBatch) + " events per request"}))
	}
	if req.Dropped < 0 {
		return Outcome{}, errs.E(errs.Invalid, errs.CodeInvalid, "The count of dropped events is negative.",
			errs.WithFields(errs.FieldError{Pointer: "/dropped", Code: "negative", Detail: "must not be negative"}))
	}
	now := in.o.Now()
	kinds, err := in.kinds(ctx, req.Events)
	if err != nil {
		return Outcome{}, err
	}
	var out Outcome
	for _, e := range req.Events {
		switch {
		case !validEvent(e):
			out.Invalid++
			continue
		case now.Sub(e.At) > in.o.MaxAge:
			out.Expired++
			continue
		}
		at := e.At
		if at.After(now) {
			at = now
		}
		ev := audit.Event{
			Type: audit.EventArtifactDownloaded, Outcome: audit.Success, At: at,
			Actor:      downloadActor(e, kinds),
			Source:     audit.Source{ClientAddr: e.ClientAddress, UserAgent: e.UserAgent},
			Repository: e.Repository, Path: e.Path,
			Details: mustJSON(map[string]any{"path": e.Path, "sha256": e.SHA256, "origin": "bastion", "bastion": rec.Bastion, "replica": e.Replica}),
		}
		in.o.Audit.Record(ctx, ev)
		out.Recorded++
	}
	if lost := req.Dropped + out.Expired + out.Invalid; lost > 0 {
		d := map[string]any{"count": lost, "reported": req.Dropped, "expired": out.Expired, "invalid": out.Invalid, "replica": rec.Replica}
		if req.DroppedSince != nil && req.Dropped > 0 {
			d["since"] = req.DroppedSince.UTC().Format(time.RFC3339)
		}
		in.o.Audit.Record(ctx, audit.Event{
			Type: audit.EventBastionDownloadsDropped, Outcome: audit.Success,
			Actor:   BastionActor(rec),
			Subject: audit.Subject{Kind: "bastion", ID: rec.BastionID, Name: rec.Bastion},
			Details: mustJSON(d),
		})
	}
	return out, nil
}

// validEvent reports whether the event keeps the contract of R-BAS-016: a time, a repository, a path and the
// digest of the bytes served.
func validEvent(e iface.DownloadEvent) bool {
	return !e.At.IsZero() && e.Repository != "" && e.Path != "" && sha256Pattern.MatchString(e.SHA256) && len(e.Repository) <= 1024
}

// kinds resolves the principals the batch names to their kinds, one query for the batch. A principal that no
// longer exists has no entry.
func (in *Intake) kinds(ctx context.Context, events []iface.DownloadEvent) (map[string]string, error) {
	var ids []string
	for _, e := range events {
		if uuidPattern.MatchString(e.PrincipalID) && !slices.Contains(ids, e.PrincipalID) {
			ids = append(ids, e.PrincipalID)
		}
	}
	out := map[string]string{}
	if len(ids) == 0 || in.o.DB == nil {
		return out, nil
	}
	err := in.o.DB.Read(ctx, func(db q.DBTX) error {
		rows, err := q.New(db).BastionPrincipalKinds(ctx, ids)
		if err != nil {
			return err
		}
		clear(out)
		for _, r := range rows {
			out[r.ID] = r.Kind
		}
		return nil
	})
	return out, err
}

// downloadActor is the actor of a download the Bastion served: the caller, by the principal and token the
// Bastion reports, or anonymous. The kind comes from the principal table; a principal that is gone was a
// user or a service account, and the event keeps its ID.
func downloadActor(e iface.DownloadEvent, kinds map[string]string) audit.Actor {
	if e.PrincipalID == "" && e.TokenID == "" {
		return audit.Actor{Kind: "anonymous"}
	}
	kind := "user"
	switch kinds[e.PrincipalID] {
	case "service_account":
		kind = "service_account"
	case "ci_mapping":
		kind = "ci"
	}
	return audit.Actor{Kind: kind, ID: e.PrincipalID, TokenID: e.TokenID, AuthMethod: "bearer"}
}
