package main

import (
	"encoding/hex"
	"fmt"
	"strings"

	debdata "github.com/guided-traffic/trove/internal/formatdata/deb"
	"github.com/guided-traffic/trove/internal/repository"
)

const debAdminUsage = "usage: trove admin deb reinstate <projectKey>/<repoKey> <path> [--config <file>]"

// debAdminCmd is trove admin deb <command> (R-DEB-032).
func debAdminCmd(args []string, s streams) error {
	if len(args) == 0 || args[0] != "reinstate" {
		return usageError{debAdminUsage}
	}
	return debReinstate(args[1:], s)
}

// debReinstate reverses the withdrawal of a package file that a journal replay withheld (R-DEB-032): it removes the mark only while the
// file is stored with the digest the mark holds, raises change_seq of the file's suites and writes deb.reinstate (not journaled). It runs
// in the transaction of the data port, as the administrator's command.
func debReinstate(args []string, s streams) error {
	c := newFlags("admin deb reinstate", s)
	repo, rest := positional(args)
	path, rest := positional(rest)
	project, repoKey, ok := strings.Cut(repo, "/")
	if !ok || project == "" || repoKey == "" || strings.Contains(repoKey, "/") || path == "" {
		return usageError{debAdminUsage}
	}
	if _, err := repository.CleanPath(path); err != nil {
		return usageError{debAdminUsage}
	}
	ctx, done := adminContext()
	defer done()
	env, closeDB, err := openAdmin(ctx, c, s, rest)
	if err != nil {
		return err
	}
	defer closeDB()
	if c.fs.NArg() > 0 {
		return usageError{debAdminUsage}
	}
	store, err := debdata.New(debdata.Options{DB: env.db, Recorder: env.recorder})
	if err != nil {
		return err
	}
	got, err := store.Reinstate(ctx, debdata.ReinstateInput{Project: project, Repository: repoKey, Path: path, Actor: env.actor})
	if err != nil {
		return fmt.Errorf("admin deb reinstate: %w", err)
	}
	_, err = fmt.Fprintf(s.stdout, "reinstated %s in %s/%s (sha256 %s)\n", path, project, repoKey, hex.EncodeToString(got.SHA256[:]))
	return err
}
