package bastionmain

import (
	"context"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
)

// TestR_BAS_016_DownloadEventsAreChargedToTheRegistrationByTheirCount: one request of the intake carries up to
// max_batch events, so the request rate alone would let one Bastion token write tens of thousands of
// artifact.downloaded rows a second (Q-BAS-117). Every batch is charged to its registration by its events, in
// units of 100, on a limiter of R-ARCH-034 built as the composition root builds it; a batch the budget does not
// hold is refused 429 rate_limited with Retry-After, records nothing and loses nothing (the Bastion keeps it and
// sends it again). Another registration has a budget of its own.
func TestR_BAS_016_DownloadEventsAreChargedToTheRegistrationByTheirCount(t *testing.T) {
	ls, err := cluster.NewLimiterSet(cluster.LimiterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	dl := config.BastionsDownloads{MaxBatch: 1000, MaxAge: 720 * time.Hour, EventsPerSecond: 200, EventsBurst: 2000}
	lim := ls.NewLimiter("bastion.download_events", cluster.ScopeCluster, dl.LimiterRate(), dl.LimiterBurst())
	rec := &capture{}
	in := NewIntake(IntakeOptions{
		Audit: rec, MaxBatch: 1000, MaxAge: 720 * time.Hour, Record: true, Now: func() time.Time { return now },
		Events: func(ctx context.Context, bastionID string, events int) (bool, time.Duration) {
			v := lim.TakeNAt(ctx, "bastion:"+bastionID, dl.Units(events), time.Time{})
			return v.Allowed, v.RetryAfter
		},
	})
	batch := func(n int) iface.DownloadsRequest {
		req := iface.DownloadsRequest{}
		for range n {
			req.Events = append(req.Events, oneEvent(now.Add(-time.Minute)))
		}
		return req
	}
	for i := range 2 {
		if out, err := in.Accept(context.Background(), recogn, batch(1000)); err != nil || out.Recorded != 1000 {
			t.Fatalf("batch %d within the budget: %+v %v", i, out, err)
		}
	}
	_, err = in.Accept(context.Background(), recogn, batch(1000))
	e, ok := errs.As(err)
	if !ok || e.Status() != 429 || e.Code != errs.CodeRateLimited || e.RetryAfter < time.Second {
		t.Fatalf("a batch beyond the budget: %v", err)
	}
	if n := len(rec.named(audit.EventArtifactDownloaded)); n != 2000 {
		t.Fatalf("%d events recorded, want the 2000 of the two admitted batches", n)
	}
	if n := len(rec.named(audit.EventBastionDownloadsDropped)); n != 0 {
		t.Fatalf("a refused batch was counted as lost (%d events): the Bastion sends it again", n)
	}
	other := recogn
	other.Bastion, other.BastionID = "standby", "0192f1c4-0000-7000-8000-0000000000b2"
	if _, err := in.Accept(context.Background(), other, batch(1000)); err != nil {
		t.Fatalf("another registration's budget: %v", err)
	}
	// A batch that reports only lost events costs the budget nothing.
	if _, err := in.Accept(context.Background(), recogn, iface.DownloadsRequest{Dropped: 3}); err != nil {
		t.Fatalf("a report of lost events: %v", err)
	}
}
