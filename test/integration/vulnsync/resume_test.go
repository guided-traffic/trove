//go:build integration

package vulnsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/guided-traffic/trove/internal/jobs"
	vsync "github.com/guided-traffic/trove/internal/vuln/sync"
)

// fakeRun is the run handle of a job that survives its attempt: the checkpoint and the progress are kept, as the job row keeps
// them, so that a second call resumes where the first stopped with the attempt unchanged.
type fakeRun struct {
	mu       sync.Mutex
	cp       json.RawMessage
	saves    int
	progress []int64
	result   any
}

func (r *fakeRun) LoadCheckpoint(v any) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.cp) == 0 {
		return false, nil
	}
	return true, json.Unmarshal(r.cp, v)
}

func (r *fakeRun) SaveCheckpoint(_ context.Context, v any) error {
	b, err := json.Marshal(v)
	r.mu.Lock()
	r.cp, r.saves = b, r.saves+1
	r.mu.Unlock()
	return err
}

func (r *fakeRun) SaveProgress(_ context.Context, done, _ int64, unit string) error {
	r.mu.Lock()
	r.progress = append(r.progress, done)
	r.mu.Unlock()
	if unit != "records" {
		return fmt.Errorf("unit %q", unit)
	}
	return nil
}

func (r *fakeRun) SetResult(v any) error { r.result = v; return nil }

// batches records the (ecosystem, batch) pairs a run committed.
type batches struct {
	mu   sync.Mutex
	seen []string
	// release is the pair after which the context is cancelled with jobs.ErrReleased, "" for none.
	release string
	cancel  context.CancelCauseFunc
}

func (b *batches) hook(eco string, n int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := fmt.Sprintf("%s/%d", eco, n)
	b.seen = append(b.seen, key)
	if key == b.release && b.cancel != nil {
		b.cancel(jobs.ErrReleased)
	}
}

func (b *batches) take() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := slices.Clone(b.seen)
	b.seen = nil
	return out
}

// R-VULN-004 acceptance (the part the integration tier reaches; the drain of a node is the e2e tier's): a full sync released
// by a drain saves its checkpoint, records no failure and leaves the stored state as it was; the same job resumes at the
// checkpoint when the archive's ETag is unchanged, and starts over when it changed.
func TestR_VULN_004_AReleasedRunResumesAtItsCheckpoint(t *testing.T) {
	// 2,500 records per ecosystem are three batches of 1,000.
	e := newEnv(t, options{Filler: 2500})
	svc := e.worker.App.VulnSync()
	rec := &batches{}
	svc.SetBatchHook(rec.hook)
	run := &fakeRun{}

	ctx, cancel := context.WithCancelCause(context.Background())
	rec.release, rec.cancel = "NuGet/1", cancel
	err := svc.RunOSV(ctx, run, vsync.Params{}, false)
	cancel(nil)
	if !errors.Is(err, jobs.ErrReleased) {
		t.Fatalf("a released run returned %v, want the release as its cause", err)
	}
	if got := rec.take(); !slices.Equal(got, []string{"NuGet/1"}) {
		t.Fatalf("batches before the release: %v", got)
	}
	var cp struct {
		Ecosystem string `json:"ecosystem"`
		Ident     string `json:"ident"`
		Batches   int    `json:"batches"`
		Written   int    `json:"written"`
	}
	if _, err := run.LoadCheckpoint(&cp); err != nil || cp.Ecosystem != "NuGet" || cp.Batches != 1 || cp.Written != 1000 || cp.Ident == "" {
		t.Fatalf("checkpoint %+v (%v)", cp, err)
	}
	if got := e.stateOf("state", "osv", "NuGet"); got != "empty" {
		t.Errorf("a half imported ecosystem is %q, not empty", got)
	}
	if got := e.stateOf("failures", "osv", "NuGet"); got != "0" || e.stateOf("last_error", "osv", "NuGet") != "" {
		t.Errorf("a release was recorded as a failure (%s, %q)", got, e.stateOf("last_error", "osv", "NuGet"))
	}
	if got := e.advisories("NuGet"); got != 1000 {
		t.Errorf("%d records after one batch", got)
	}

	// The same job continues at the checkpoint: batches 2 and 3 of NuGet, then npm from the start.
	rec.release, rec.cancel = "", nil
	if err := svc.RunOSV(context.Background(), run, vsync.Params{}, false); err != nil {
		t.Fatal(err)
	}
	want := []string{"NuGet/2", "NuGet/3", "npm/1", "npm/2", "npm/3"}
	if got := rec.take(); !slices.Equal(got, want) {
		t.Fatalf("batches of the resumed run: %v, want %v", got, want)
	}
	for _, eco := range []string{"npm", "NuGet"} {
		if got := e.stateOf("state", "osv", eco); got != "complete" {
			t.Errorf("%s state %q", eco, got)
		}
		if got, want := e.advisories(eco), int64(e.stub.Info().Ecosystems[eco].Records); got != want {
			t.Errorf("%s: %d records, want %d", eco, got, want)
		}
	}
	if got := e.stateOf("data_epoch", "osv", "NuGet"); got != "1" {
		t.Errorf("the epoch is %s: a resumed run counts as one run that changed records", got)
	}

	// A checkpoint of another archive is not continued.
	e.st.Exec("DELETE FROM vuln_advisory WHERE ecosystem = 'NuGet'")
	e.st.Exec("UPDATE vuln_source_state SET state = 'empty', etag = NULL, watermark = NULL WHERE source = 'osv' AND ecosystem = 'NuGet'")
	run2 := &fakeRun{}
	ctx, cancel = context.WithCancelCause(context.Background())
	rec.release, rec.cancel = "NuGet/2", cancel
	if err := svc.RunOSV(ctx, run2, vsync.Params{}, true); !errors.Is(err, jobs.ErrReleased) {
		t.Fatalf("second release: %v", err)
	}
	cancel(nil)
	rec.take()
	e.touch("NuGet", e.stub.IDs("NuGet")[0]) // the archive of the source changes: another ETag
	rec.release, rec.cancel = "", nil
	if err := svc.RunOSV(context.Background(), run2, vsync.Params{}, true); err != nil {
		t.Fatal(err)
	}
	got := rec.take()
	if len(got) < 1 || got[0] != "NuGet/1" {
		t.Fatalf("a run on a changed archive continued at the checkpoint: %v", got)
	}
	if got, want := e.advisories("NuGet"), int64(e.stub.Info().Ecosystems["NuGet"].Records); got != want {
		t.Errorf("NuGet: %d records, want %d", got, want)
	}
}

// R-VULN-004: a released run that has written part of an archive leaves the alias groups and the flags of what it wrote to the
// run that finishes it: the finished data has the canonical identifier of every group, as a run without a release has.
func TestR_VULN_004_AResumedRunFinishesTheAliasGroupsOfTheBatchesBeforeTheRelease(t *testing.T) {
	e := newEnv(t, options{Filler: 1500})
	svc := e.worker.App.VulnSync()
	rec := &batches{}
	svc.SetBatchHook(rec.hook)
	run := &fakeRun{}
	ctx, cancel := context.WithCancelCause(context.Background())
	rec.release, rec.cancel = "npm/1", cancel
	// The alias group of the fixture sorts into the first batch of the archive (a name with a lower id than the filler's).
	if err := svc.RunOSV(ctx, run, vsync.Params{}, false); !errors.Is(err, jobs.ErrReleased) {
		t.Fatalf("release: %v", err)
	}
	cancel(nil)
	rec.release, rec.cancel = "", nil
	if err := svc.RunOSV(context.Background(), run, vsync.Params{}, false); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"GHSA-aaaa-bbbb-cccc", "CVE-2026-0001"} {
		if got := e.text("SELECT canonical_id FROM vuln_advisory WHERE ecosystem = 'npm' AND id = $1", id); got != "GHSA-aaaa-bbbb-cccc" {
			t.Errorf("%s: canonical identifier %q", id, got)
		}
	}
}
