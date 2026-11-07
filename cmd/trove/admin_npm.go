package main

import (
	"fmt"
	"strings"

	"github.com/guided-traffic/trove/internal/formatdata/npm"
	"github.com/guided-traffic/trove/internal/formats/npm/name"
)

const npmAdminUsage = "usage: trove admin npm reinstate <projectKey>/<repoKey> <name>@<version> [--config <file>]"

// npmAdminCmd is trove admin npm <command> (R-NPM-047).
func npmAdminCmd(args []string, s streams) error {
	if len(args) == 0 || args[0] != "reinstate" {
		return usageError{npmAdminUsage}
	}
	return npmReinstate(args[1:], s)
}

// npmReinstate reverses the withdrawal of a version that a journal replay withheld (R-NPM-047, Q-NPM-8): it removes a
// `journal` tombstone only while the version is stored with the tombstone's integrity, increments rev_seq and writes
// npm.reinstate (R-NPM-044). It runs in the transaction of the data port, as the administrator's command.
func npmReinstate(args []string, s streams) error {
	c := newFlags("admin npm reinstate", s)
	repo, rest := positional(args)
	nameVersion, rest := positional(rest)
	project, repoKey, ok := strings.Cut(repo, "/")
	at := strings.LastIndex(nameVersion, "@")
	if !ok || project == "" || repoKey == "" || strings.Contains(repoKey, "/") || at <= 0 || at == len(nameVersion)-1 {
		return usageError{npmAdminUsage}
	}
	pkg, err := name.Normalize(nameVersion[:at])
	if err != nil {
		return fmt.Errorf("admin npm reinstate: %w", err)
	}
	version := nameVersion[at+1:]
	ctx, done := adminContext()
	defer done()
	env, closeDB, err := openAdmin(ctx, c, s, rest)
	if err != nil {
		return err
	}
	defer closeDB()
	if c.fs.NArg() > 0 {
		return usageError{npmAdminUsage}
	}
	store, err := npm.New(npm.Options{DB: env.db, Recorder: env.recorder})
	if err != nil {
		return err
	}
	got, err := store.Reinstate(ctx, npm.ReinstateInput{Project: project, Repository: repoKey, Name: pkg, Version: version, Actor: env.actor})
	if err != nil {
		return fmt.Errorf("admin npm reinstate: %w", err)
	}
	_, err = fmt.Fprintf(s.stdout, "reinstated %s@%s in %s/%s (integrity %s)\n", pkg, version, project, repoKey, got.Integrity)
	return err
}
