package main

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/guided-traffic/trove/internal/formatdata/oci"
	ociformat "github.com/guided-traffic/trove/internal/formats/oci"
)

const ociAdminUsage = "usage: trove admin oci reinstate <projectKey>/<repoKey> <name>@<digest> [--config <file>]"

// ociAdminCmd is trove admin oci <command> (R-OCI-033).
func ociAdminCmd(args []string, s streams) error {
	if len(args) == 0 || args[0] != "reinstate" {
		return usageError{ociAdminUsage}
	}
	return ociReinstate(args[1:], s)
}

// ociReinstate reverses the withdrawal of a manifest that a journal replay withheld (R-OCI-033, Q-OCI-33): it removes the mark only
// while the manifest row and its item are stored with that digest, increments rev_seq and writes oci.reinstate (not journaled).
// It runs in the transaction of the data port, as the administrator's command.
func ociReinstate(args []string, s streams) error {
	c := newFlags("admin oci reinstate", s)
	repo, rest := positional(args)
	nameDigest, rest := positional(rest)
	project, repoKey, ok := strings.Cut(repo, "/")
	at := strings.LastIndex(nameDigest, "@")
	if !ok || project == "" || repoKey == "" || strings.Contains(repoKey, "/") || at <= 0 || at == len(nameDigest)-1 {
		return usageError{ociAdminUsage}
	}
	name, digest := nameDigest[:at], nameDigest[at+1:]
	if !ociformat.ValidName(name) || !ociformat.ValidDigest(digest) {
		return usageError{ociAdminUsage}
	}
	var d [32]byte
	if _, err := hex.Decode(d[:], []byte(strings.TrimPrefix(digest, "sha256:"))); err != nil {
		return usageError{ociAdminUsage}
	}
	ctx, done := adminContext()
	defer done()
	env, closeDB, err := openAdmin(ctx, c, s, rest)
	if err != nil {
		return err
	}
	defer closeDB()
	if c.fs.NArg() > 0 {
		return usageError{ociAdminUsage}
	}
	store, err := oci.New(oci.Options{DB: env.db, Recorder: env.recorder})
	if err != nil {
		return err
	}
	got, err := store.Reinstate(ctx, oci.ReinstateInput{Project: project, Repository: repoKey, Name: name, Digest: d, Actor: env.actor})
	if err != nil {
		return fmt.Errorf("admin oci reinstate: %w", err)
	}
	_, err = fmt.Fprintf(s.stdout, "reinstated %s@%s in %s/%s (revision %d)\n", name, digest, project, repoKey, got.Revision)
	return err
}
