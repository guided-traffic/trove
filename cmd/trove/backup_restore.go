package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/guided-traffic/trove/internal/app"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/backup/restore"
	"github.com/guided-traffic/trove/internal/blob/s3"
	"github.com/guided-traffic/trove/internal/buildinfo"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/journal/replay"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/outbound"
	"github.com/guided-traffic/trove/internal/secrets"
)

// backupRestoreCmd is trove backup restore <id> (R-BAK-020, R-BAK-021). Owned by the restore work package: it replaces
// newRestorer.
func backupRestoreCmd(args []string, s streams) error {
	c, asJSON := newBackupFlags("restore", s)
	check := c.fs.Bool("check", false, "run the preconditions of R-BAK-020 and change nothing")
	skip := c.fs.Bool("skip-revocation-journal", false, "restore without replaying the revocation journal (R-BAK-024)")
	journalCreds := c.fs.String("journal-credentials-file", "", "credentials that read the source's revocation journal when the blob-store credentials cannot")
	identity := c.fs.String("identity-file", "", "age identity of a client-side encrypted backup (R-BAK-027)")
	pos, flags, names, err := parseBackup(c, args, 1)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageError{backupUsage}
	}
	id, err := format.ParseID(pos[0])
	if err != nil {
		return usageError{err.Error()}
	}
	env, err := openBackupEnv(c, s, flags, names)
	if err != nil {
		return err
	}
	env.json = *asJSON
	restorer, err := newRestorer(env)
	if err != nil {
		return err
	}
	ctx, stop := env.context()
	defer stop()
	rep, err := restorer.Restore(ctx, backup.RestoreOptions{
		ID: id, Check: *check, SkipRevocationJournal: *skip, JournalCredentialsFile: *journalCreds, IdentityFile: *identity,
	})
	if err != nil {
		return err
	}
	if env.json {
		return env.printJSON(rep)
	}
	if rep.AlreadyRestored {
		_, err = fmt.Fprintf(s.stdout, "backup %s is already restored into this database; nothing to do\n", rep.BackupID)
		return err
	}
	if rep.Checked {
		_, err = fmt.Fprintf(s.stdout, "restore of %s: every precondition holds\n", rep.BackupID)
		return err
	}
	_, err = fmt.Fprintf(s.stdout, "restored %s (snapshot %s), journal %s\n", rep.BackupID, rep.SnapshotAt.UTC().Format("2006-01-02T15:04:05Z"), rep.Journal)
	return err
}

// newRestorer builds the Restorer of R-BAK-021: the database session (it takes the migration lock when the restore starts and
// holds it until the command ends), the backup target with its own credential, the new installation's blob store with the
// runtime credential, and the key ring.
func newRestorer(env *backupEnv) (backup.Restorer, error) {
	cfg := env.cfg
	ctx := context.Background()
	out := outbound.New(cfg.Proxy.Outbound)
	if err := out.Err(); err != nil {
		return nil, backup.Fail(backup.ExitUsage, backup.CodeConfig, fmt.Errorf("outbound configuration: %w", err))
	}
	tgt, err := s3.NewTarget(ctx, cfg.Backup, out)
	if err != nil {
		return nil, backup.Fail(backup.ExitUsage, backup.CodeConfig, err)
	}
	client, err := s3.NewFromFactory(ctx, cfg.Storage.S3, out)
	if err != nil {
		return nil, backup.Fail(backup.ExitUsage, backup.CodeConfig, fmt.Errorf("storage.s3: %w", err))
	}
	ring, err := secrets.Load(cfg.Secrets)
	if err != nil {
		return nil, backup.Fail(backup.ExitUsage, backup.CodeConfig, err)
	}
	session, err := postgres.OpenRestoreSession(ctx, cfg.Database, postgres.MigrateOptions{
		Instance: env.instance, Migrate: cfg.Migrate, Version: buildinfo.Get().Version, Log: env.log,
	})
	if err != nil {
		if postgres.IsUnavailable(err) {
			return nil, backup.Fail(backup.ExitTemporary, backup.CodeTemporary, err)
		}
		return nil, err
	}
	return restore.New(restore.Deps{
		Session: session, Target: tgt, Layout: format.Layout{Prefix: cfg.Backup.Target.Prefix},
		TargetEndpoint: tgt.Endpoint(), TargetBucket: tgt.Bucket(),
		Blobs: s3.NewBlobSink(client, cfg.Storage.S3, int64(cfg.Backup.Copy.PartSize)), Keys: ring,
		Items: app.RestoreItems(cfg, env.log), Journal: restoreJournal(cfg, out, env.log), Config: cfg.Backup, Log: env.log, Out: env.stdout, Pod: env.instance,
	}), nil
}

// restoreJournal builds the revocation journal of a source for the restore (R-BAK-020 check 8, R-BAK-024): the reader of the
// source's bucket and prefix, with the blob-store credentials or those of --journal-credentials-file, and the replayer over the
// restore's session.
func restoreJournal(cfg *config.Config, out *outbound.Factory, log *slog.Logger) func(context.Context, restore.JournalEnv) (restore.Journal, error) {
	return func(ctx context.Context, e restore.JournalEnv) (restore.Journal, error) {
		s3cfg := cfg.Storage.S3
		s3cfg.Bucket, s3cfg.Prefix = e.SourceBucket, e.SourcePrefix
		if e.SourceEndpoint != "" {
			s3cfg.Endpoint = e.SourceEndpoint
		}
		reader, err := replay.OpenJournal(ctx, s3cfg, out, e.CredentialsFile)
		if err != nil {
			return nil, err
		}
		return journalSource{reader: reader, Replayer: replay.New(replay.Options{DB: e.Runner, Reader: reader, Recorder: e.Recorder, AdminBinding: authz.HasAdminBinding, Log: log})}, nil
	}
}

// journalSource is the journal of the source: the check lists it, the replay applies it.
type journalSource struct {
	reader *replay.Reader
	journal.Replayer
}

// Reachable lists the journal prefix for the entries of today and later, which needs the same permission as a replay.
func (j journalSource) Reachable(ctx context.Context) error {
	_, _, err := j.reader.Entries(ctx, time.Now())
	return err
}
