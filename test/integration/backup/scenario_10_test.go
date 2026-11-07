//go:build integration

package backup

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/restore"
	"github.com/guided-traffic/trove/internal/blob/s3"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/journal/replay"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/outbound"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// realJournal is the journal of cmd/trove/backup_restore.go: the reader of the source's bucket and prefix, which the manifest
// names, and the replayer over the restore's own session.
func realJournal(cfg *config.Config) func(context.Context, restore.JournalEnv) (restore.Journal, error) {
	return func(ctx context.Context, e restore.JournalEnv) (restore.Journal, error) {
		s3cfg := cfg.Storage.S3
		s3cfg.Bucket, s3cfg.Prefix = e.SourceBucket, e.SourcePrefix
		reader, err := replay.OpenJournal(ctx, s3cfg, outbound.New(cfg.Proxy.Outbound), e.CredentialsFile)
		if err != nil {
			return nil, err
		}
		return realJournalSource{reader: reader, Replayer: replay.New(replay.Options{DB: e.Runner, Reader: reader, Recorder: e.Recorder, AdminBinding: authz.HasAdminBinding})}, nil
	}
}

type realJournalSource struct {
	reader *replay.Reader
	journal.Replayer
}

func (j realJournalSource) Reachable(ctx context.Context) error {
	_, _, err := j.reader.Entries(ctx, time.Now())
	return err
}

// cancelAfter interrupts a replay once a line has been printed: the pod is killed between two entries.
type cancelAfter struct {
	journal.Replayer
	reachable func(context.Context) error
	cancel    func()
	fired     bool
}

func (c *cancelAfter) Reachable(ctx context.Context) error { return c.reachable(ctx) }

func (c *cancelAfter) Replay(ctx context.Context, o journal.ReplayOptions) (*journal.Summary, error) {
	o.Out = lineHook{w: o.Out, on: func(line string) {
		if !c.fired && strings.Contains(line, "applied") {
			c.fired = true
			c.cancel()
			time.Sleep(50 * time.Millisecond)
		}
	}}
	return c.Replayer.Replay(ctx, o)
}

type lineHook struct {
	w  io.Writer
	on func(string)
}

func (l lineHook) Write(p []byte) (int, error) {
	n, err := l.w.Write(p)
	l.on(string(p))
	return n, err
}

// TestR_BAK_029_RevocationsSurviveTheRestore is scenario 10 of R-BAK-029 and the acceptance criteria of R-BAK-024: a token
// revoked and a user disabled after the snapshot stay revoked and disabled after the restore, with the journal the shipper wrote
// to the source's bucket and the replay the restore command runs. The restore is killed during the replay; the resumed one lists
// each entry once as applied and, on a second pass, as already in effect, and a restored replica answers 401 to both tokens.
func TestR_BAK_029_RevocationsSurviveTheRestore(t *testing.T) {
	e := newJEnv(t)
	ctx := context.Background()
	alice, bob := e.stack.User("alice"), e.stack.User("bob")
	rawAlice, rawBob := e.stack.Token(alice, scope.Default()), e.stack.Token(bob, scope.Default())

	sealStackCanary(t, e.stack)

	// The source installation of the backup is the journal's stack; its journal prefix is the prefix the manifest names.
	loaded, err := config.Load(config.Options{Environ: append(e.stack.Environ(), "TROVE_BACKUP_TARGET_BUCKET="+testenv.Get(t, "TROVE_TEST_S3_BUCKET_BACKUPS")), Command: config.CommandBackup})
	if err != nil {
		t.Fatal(err)
	}
	cfg := loaded.Config
	client, err := s3.NewFromFactory(ctx, cfg.Storage.S3, outbound.New(cfg.Proxy.Outbound))
	if err != nil {
		t.Fatal(err)
	}
	in := &install{t: t, dbName: e.stack.Database(), dbCfg: cfg.Database, s3cfg: cfg.Storage.S3, obj: client, client: client, prefix: e.prefix}
	in.s3cfg.Prefix = e.prefix
	in.dbCfg.Preflight.AllowSuperuser = true
	tc := targetConfig(t, testenv.Get(t, "TROVE_TEST_S3_BUCKET_BACKUPS"), "none")
	tgt := newTarget(t, tc)
	cleanup(t, tgt, tc.Target.Prefix+"/")
	r := &rig{cfg: in.backupConfig(t), tgt: tgt}
	r.cfg.Target = tc.Target

	for name, raw := range map[string]string{"alice": rawAlice, "bob": rawBob} {
		if _, err := e.tokens.Validate(ctx, raw); err != nil {
			t.Fatalf("%s's token must be valid before the snapshot: %v", name, err)
		}
	}
	res, err := in.run(ctx, r, backup.CreateOptions{NoPrune: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// After the snapshot: alice's token is revoked, bob is disabled.
	caller := principal.Caller{Actor: audit.Actor{Kind: "admin-cli", Name: "test"}}
	if _, err := e.tokens.Revoke(ctx, caller, e.tokenID(rawAlice), token.ReasonAdmin); err != nil {
		t.Fatal(err)
	}
	if err := e.manager.DisableUser(ctx, caller, "bob", principal.DisableOptions{Reason: "admin", RevokeTokens: true}); err != nil {
		t.Fatal(err)
	}
	if n, err := e.shipper.Ship(ctx); err != nil || n < 2 {
		t.Fatalf("Ship = %d, %v; want at least the revocation and the disable", n, err)
	}

	src := in.sourceOf(t, r, res.BackupID)
	if !src.m.Source.RevocationJournal || src.m.Source.Storage.Prefix != e.prefix {
		t.Fatalf("the manifest names the journal %v at %q, want it at %q", src.m.Source.RevocationJournal, src.m.Source.Storage.Prefix, e.prefix)
	}
	dst := src.destination()

	// kill during the replay: the first entry is applied, the pod ends
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var hold *cancelAfter
	r1, _ := dst.restorer(restorerOptions{journalFn: func(c context.Context, env restore.JournalEnv) (restore.Journal, error) {
		inner, err := realJournal(dst.cfg)(c, env)
		if err != nil {
			return nil, err
		}
		js := inner.(realJournalSource)
		hold = &cancelAfter{Replayer: js.Replayer, reachable: js.Reachable, cancel: cancel}
		return hold, nil
	}})
	if _, err := r1.Restore(rctx, backup.RestoreOptions{ID: src.id}); err == nil || backup.ExitCode(err) != backup.ExitTemporary {
		t.Fatalf("the interrupted restore = %v (exit %d), want exit 75", err, backup.ExitCode(err))
	}
	if hold == nil || !hold.fired {
		t.Fatal("the restore never replayed an entry")
	}

	// the Job's next pod resumes
	r2, out := dst.restorer(restorerOptions{journalFn: realJournal(dst.cfg)})
	if _, err := r2.Restore(ctx, backup.RestoreOptions{ID: src.id}); err != nil {
		t.Fatalf("resumed restore: %v", err)
	}
	t.Logf("resumed restore output:\n%s", out.String())

	s := dst.stack
	if n := s.Text(`SELECT count(*)::text FROM api_token WHERE revoked_at IS NOT NULL`); n == "0" {
		t.Fatal("no token is revoked in the restored database")
	}
	if got := s.Text(`SELECT coalesce(max(details->>'journal'), '') FROM audit_event WHERE action = 'system.restored'`); got != "replayed" {
		t.Errorf("system.restored records journal %q, want replayed", got)
	}
	if n := s.Text(`SELECT count(*)::text FROM audit_event WHERE action = 'system.restore.journal_replayed'`); n == "0" {
		t.Error("system.restore.journal_replayed was not written")
	}
	rep := s.Start(t, testenv.ReplicaOptions{Name: "restored-1"})
	for name, raw := range map[string]string{"alice": rawAlice, "bob": rawBob} {
		if a := s.Do(rep, "GET", "/api/v1/authz/me", raw, nil); a.Status != 401 {
			t.Errorf("the restored installation answers %d to %s's token, want 401: %s", a.Status, name, a.Body)
		}
	}
	if st := s.Text(`SELECT status FROM principal WHERE name = 'bob'`); st != "disabled" {
		t.Errorf("bob's status is %q in the restored database, want disabled", st)
	}
	// the resumed pass: the entry the killed pod applied is already in effect, the other is applied now
	if !strings.Contains(out.String(), "already in effect") || !strings.Contains(out.String(), "applied") {
		t.Errorf("the resumed restore lists neither an entry already in effect nor one applied:\n%s", out.String())
	}

	// a further pass of the same journal changes nothing: every entry is already in effect
	db2, err := postgres.Open(ctx, s.DatabaseConfig(), "scenario-10", meta.RoleWeb, quietLog)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	again, err := replay.New(replay.Options{DB: db2, Reader: replay.NewReader(e.store, e.prefix), AdminBinding: authz.HasAdminBinding}).
		Replay(ctx, journal.ReplayOptions{RestoredTo: src.m.SnapshotAt, Margin: 5 * time.Minute, Out: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if again.Counts[journal.Applied] != 0 || again.Counts[journal.AlreadyInEffect] < 2 {
		t.Errorf("a further pass = %v, want every entry already in effect", again.Counts)
	}
}
