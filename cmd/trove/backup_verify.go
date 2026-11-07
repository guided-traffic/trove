package main

import (
	"fmt"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/format"
)

// backupVerifyCmd is trove backup verify (R-BAK-019). Owned by the target work package: it replaces newVerifier.
func backupVerifyCmd(args []string, s streams) error {
	c, asJSON := newBackupFlags("verify", s)
	latest := c.fs.Bool("latest", false, "verify the newest complete backup")
	all := c.fs.Bool("all", false, "verify every complete backup")
	deep := c.fs.Bool("deep", false, "download every pool object and recompute its SHA-256")
	bps := c.fs.Int64("bytes-per-second", 0, "throttle the downloads of --deep; 0 is unlimited")
	pos, flags, names, err := parseBackup(c, args, 1)
	if err != nil {
		return err
	}
	selected := 0
	for _, b := range []bool{*latest, *all, len(pos) == 1} {
		if b {
			selected++
		}
	}
	if selected != 1 || *bps < 0 {
		return usageError{backupUsage}
	}
	opts := backup.VerifyOptions{Latest: *latest, All: *all, Deep: *deep, BytesPerSecond: *bps}
	if len(pos) == 1 {
		id, err := format.ParseID(pos[0])
		if err != nil {
			return usageError{err.Error()}
		}
		opts.ID = id
	}
	env, err := openBackupEnv(c, s, flags, names)
	if err != nil {
		return err
	}
	env.json = *asJSON
	verifier, err := newVerifier(env)
	if err != nil {
		return err
	}
	ctx, stop := env.context()
	defer stop()
	report, err := verifier.Verify(ctx, opts)
	if report != nil {
		if env.json {
			if jerr := env.printJSON(report); jerr != nil {
				return jerr
			}
		} else {
			for _, f := range report.Findings {
				if _, perr := fmt.Fprintf(s.stdout, "%s %s\n", f.Kind, f.Object); perr != nil {
					return perr
				}
			}
		}
	}
	return err
}

// newVerifier builds the Verifier. The target work package replaces the body.
func newVerifier(*backupEnv) (backup.Verifier, error) {
	return backup.NotBuilt{Operation: "verify"}, nil
}
