package main

import (
	"fmt"
	"io"
	"time"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/blob/s3"
	"github.com/guided-traffic/trove/internal/blob/verify"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/outbound"
)

const storageUsage = `usage: trove admin storage verify [--since <RFC 3339 time>] [--concurrency N] [--max-items N] [--json] [--config <file>]
       trove admin storage restore-versions --before <RFC 3339 time> [--credentials-file <path>] [--dry-run] [--concurrency N] [--json] [--config <file>]

verify            check that the object of every live blob is in the object store, and list the items that reference each blob without one (R-HA-024, R-HA-025)
restore-versions  remove the delete markers younger than t from the objects of the blobs that have none, so that a point-in-time restore of the database finds its objects again (R-HA-025 step 5)

exit codes: 0 every object is there, 1 failed, 2 usage or configuration, 5 a blob has no object or another size, 75 temporary (the database or the store could not be reached or answered)`

// storageCmd dispatches trove admin storage <command>.
func storageCmd(args []string, s streams) error {
	if len(args) >= 1 {
		switch args[0] {
		case "verify":
			return storageVerifyCmd(args[1:], s)
		case "restore-versions":
			return storageRestoreVersionsCmd(args[1:], s)
		}
	}
	return usageError{storageUsage}
}

// storageVerifyCmd is trove admin storage verify (R-HA-024, R-HA-025 step 4): it asks the object store for the object of
// every live blob row and prints one line per item that references a blob without its object. It runs with the runtime
// database role and the runtime object-store credential and changes nothing: it needs no write permission, and it leaves the
// marks of the scrub (R-STOR-027) alone. It exits 5 when a blob has no object or an object of another size, 75 when a check
// could not be answered and no blob is damaged.
func storageVerifyCmd(args []string, s streams) error {
	c, asJSON := newBackupFlags("admin storage verify", s)
	since := c.fs.String("since", "", "only the blob rows created or changed at or after this time (RFC 3339); every blob without it")
	conc := c.fs.Int("concurrency", verify.DefaultConcurrency, "simultaneous existence checks")
	maxItems := c.fs.Int("max-items", verify.DefaultMaxItems, "items listed per damaged blob")
	_, flags, names, err := parseBackup(c, args, 0)
	if err != nil {
		return backupExit(err)
	}
	var cut time.Time
	if *since != "" {
		cut, err = time.Parse(time.RFC3339, *since)
		if err != nil {
			return usageError{storageUsage}
		}
	}
	if *conc < 1 || *maxItems < 1 {
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
	client, err := s3.NewFromFactory(ctx, cfg.Storage.S3, outbound.New(cfg.Proxy.Outbound))
	if err != nil {
		return backupExit(err)
	}
	rep, err := verify.Run(ctx, verify.Options{
		DB: verify.PGSource{DB: db}, Objects: client, Prefix: cfg.Storage.S3.Prefix, Since: cut,
		Concurrency: *conc, MaxItems: *maxItems, Log: env.log,
	})
	if rep != nil {
		if env.json {
			if jerr := env.printJSON(rep); jerr != nil {
				return jerr
			}
		} else if werr := writeVerifyReport(s.stdout, rep); werr != nil {
			return werr
		}
	}
	if err != nil {
		if blob.IsConfig(err) {
			return backup.Fail(backup.ExitUsage, backup.CodeConfig, err)
		}
		return backupExit(err)
	}
	switch {
	case len(rep.Findings) > 0:
		return backup.Fail(backup.ExitFindings, backup.CodeFindings,
			fmt.Errorf("%d blobs have no object and %d another size; %d items are affected", rep.Missing, rep.SizeMismatch, rep.ItemsAffected))
	case len(rep.Unchecked) > 0:
		return backup.Fail(backup.ExitTemporary, backup.CodeTemporary, fmt.Errorf("%d blobs could not be checked: the object store did not answer", len(rep.Unchecked)))
	}
	return nil
}

// writeVerifyReport prints the report as text: one line per item of a damaged blob, a line per damaged blob that no item
// references, one per blob that could not be checked, and the totals last.
func writeVerifyReport(w io.Writer, rep *verify.Report) error {
	for _, f := range rep.Findings {
		detail := "object missing"
		if f.Kind == verify.KindSizeMismatch {
			detail = fmt.Sprintf("object has %d bytes, the row says %d", f.ObjectSize, f.Size)
		}
		if len(f.Items) == 0 {
			if _, err := fmt.Fprintf(w, "blob %s: %s (no item references it)\n", f.SHA256, detail); err != nil {
				return err
			}
			continue
		}
		for _, it := range f.Items {
			trashed := ""
			if it.Trashed {
				trashed = " (trashed)"
			}
			if _, err := fmt.Fprintf(w, "item %s/%s:%s%s: %s, blob %s\n", it.Project, it.Repository, it.Path, trashed, detail, f.SHA256); err != nil {
				return err
			}
		}
		if more := f.ItemCount - int64(len(f.Items)); more > 0 {
			if _, err := fmt.Fprintf(w, "blob %s: and %d more items\n", f.SHA256, more); err != nil {
				return err
			}
		}
	}
	for _, u := range rep.Unchecked {
		if _, err := fmt.Fprintf(w, "blob %s: not checked: %s\n", u.SHA256, u.Error); err != nil {
			return err
		}
	}
	since := ""
	if !rep.Since.IsZero() {
		since = " since " + rep.Since.UTC().Format(time.RFC3339)
	}
	_, err := fmt.Fprintf(w, "storage verify%s: %d blobs checked, %d without object, %d of another size, %d not checked, %d items affected\n",
		since, rep.Checked, rep.Missing, rep.SizeMismatch, len(rep.Unchecked), rep.ItemsAffected)
	return err
}
