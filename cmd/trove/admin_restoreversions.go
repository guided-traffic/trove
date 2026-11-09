package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/blob/restoreversions"
	"github.com/guided-traffic/trove/internal/blob/s3"
	"github.com/guided-traffic/trove/internal/blob/verify"
	"github.com/guided-traffic/trove/internal/journal/replay"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/outbound"
)

// storageRestoreVersionsCmd is trove admin storage restore-versions (R-HA-025 step 5): after a point-in-time restore of the
// database it finds the blobs whose object is missing and removes the delete markers younger than t from their keys, so that the
// version before the marker is current again. It needs a credential that may list and delete object versions
// (s3:ListBucketVersions, s3:DeleteObjectVersion), which the runtime credential deliberately lacks (R-HA-023): the operator
// passes it with --credentials-file, to this command only. It exits 5 when a blob still has no object afterwards, 75 when the
// database or the store could not be reached or a check was not answered.
func storageRestoreVersionsCmd(args []string, s streams) error {
	c, asJSON := newBackupFlags("admin storage restore-versions", s)
	before := c.fs.String("before", "", "t, the time the platform restore went to (RFC 3339): delete markers created after it are removed")
	creds := c.fs.String("credentials-file", "", "a credential that may list and delete object versions; empty = the blob store's credential")
	dry := c.fs.Bool("dry-run", false, "list the blobs that would be restored and remove nothing")
	conc := c.fs.Int("concurrency", verify.DefaultConcurrency, "simultaneous existence checks")
	_, flags, names, err := parseBackup(c, args, 0)
	if err != nil {
		return backupExit(err)
	}
	t, perr := time.Parse(time.RFC3339, *before)
	if *before == "" || perr != nil || *conc < 1 {
		return usageError{storageUsage}
	}
	env, err := openPostRestoreEnv(c, s, flags, names)
	if err != nil {
		return backupExit(err)
	}
	env.json = *asJSON
	ctx, stop := env.context()
	defer stop()
	cfg := env.cfg
	db, err := postgres.Open(ctx, cfg.Database, env.instance, meta.RoleWeb, env.log)
	if err != nil {
		if postgres.IsUnavailable(err) {
			return backup.Fail(backup.ExitTemporary, backup.CodeTemporary, err)
		}
		return backupExit(err)
	}
	defer db.Close()
	s3cfg := cfg.Storage.S3
	if *creds != "" {
		if s3cfg.Credentials, err = replay.LoadCredentials(*creds); err != nil {
			return backup.Fail(backup.ExitUsage, backup.CodeConfig, err)
		}
	}
	client, err := s3.NewFromFactory(ctx, s3cfg, outbound.New(cfg.Proxy.Outbound))
	if err != nil {
		return backupExit(err)
	}
	rep, err := restoreversions.Run(ctx, restoreversions.Options{
		DB: verify.PGSource{DB: db}, Store: client, Prefix: s3cfg.Prefix, Before: t, DryRun: *dry, Concurrency: *conc, Log: env.log,
	})
	if rep != nil {
		if env.json {
			if jerr := env.printJSON(rep); jerr != nil {
				return jerr
			}
		} else if werr := writeRestoreVersionsReport(s.stdout, rep); werr != nil {
			return werr
		}
		if rep.MarkersRemoved > 0 {
			if aerr := recordVersionsRestored(ctx, env, db, rep); aerr != nil {
				return backupExit(aerr)
			}
		}
	}
	if err != nil {
		if blob.IsConfig(err) {
			return backup.Fail(backup.ExitUsage, backup.CodeConfig, err)
		}
		return backupExit(err)
	}
	switch {
	case rep.Remaining > 0 || rep.SizeMismatch > 0:
		return backup.Fail(backup.ExitFindings, backup.CodeFindings,
			fmt.Errorf("%d blobs still have no object and %d another size; %d items are affected", rep.Remaining, rep.SizeMismatch, rep.ItemsAffected))
	case rep.Unchecked > 0:
		return backup.Fail(backup.ExitTemporary, backup.CodeTemporary, fmt.Errorf("%d blobs could not be checked: the object store did not answer", rep.Unchecked))
	}
	return nil
}

// recordVersionsRestored writes system.storage.versions_restored for a run that removed delete markers.
func recordVersionsRestored(ctx context.Context, env *backupEnv, db *postgres.DB, rep *restoreversions.Report) error {
	rec, err := audit.New(audit.Options{DB: db, Replica: env.instance, Config: env.cfg.Audit, Log: env.log, AppLog: env.log})
	if err != nil {
		return err
	}
	details, err := json.Marshal(map[string]any{
		"before": rep.Before.UTC().Format(time.RFC3339), "markersRemoved": rep.MarkersRemoved, "restored": rep.Restored, "remaining": rep.Remaining,
	})
	if err != nil {
		return err
	}
	return db.InTx(ctx, func(tx meta.Tx) error {
		return rec.RecordTx(ctx, tx, audit.Event{
			Type: audit.EventStorageVersionsRestored, Outcome: audit.Success, Actor: audit.Actor{Kind: "system", Name: "admin"}, Details: details,
		})
	})
}

// writeRestoreVersionsReport prints one line per blob that had no object, and the totals last.
func writeRestoreVersionsReport(w io.Writer, rep *restoreversions.Report) error {
	for _, e := range rep.Entries {
		var detail string
		switch e.Outcome {
		case restoreversions.OutcomeRestored:
			detail = fmt.Sprintf("restored, %d delete markers removed", e.MarkersRemoved)
		case restoreversions.OutcomeWouldRestore:
			detail = fmt.Sprintf("would be restored, %d delete markers would be removed", e.MarkersRemoved)
		case restoreversions.OutcomeNoDeleteMarker:
			detail = "no delete marker younger than t: the object is not missing because of a deletion after t"
		case restoreversions.OutcomeNoVersion:
			detail = "no object version below its delete markers"
		default:
			detail = "still without object after removing its delete markers"
		}
		if _, err := fmt.Fprintf(w, "blob %s: %s (%d items)\n", e.SHA256, detail, e.ItemCount); err != nil {
			return err
		}
	}
	mode := ""
	if rep.DryRun {
		mode = " (dry run)"
	}
	_, err := fmt.Fprintf(w, "storage restore-versions before %s%s: %d blobs checked, %d without object, %d restored, %d delete markers removed, %d still without object, %d of another size, %d not checked\n",
		rep.Before.UTC().Format(time.RFC3339), mode, rep.Checked, rep.Missing, rep.Restored, rep.MarkersRemoved, rep.Remaining, rep.SizeMismatch, rep.Unchecked)
	return err
}
