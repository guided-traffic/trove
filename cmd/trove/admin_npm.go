package main

import (
	"fmt"
	"strings"
)

const npmAdminUsage = "usage: trove admin npm reinstate <projectKey>/<repoKey> <name>@<version> [--config <file>]"

// npmAdminCmd is trove admin npm <command> (R-NPM-047). Owned by the npm/NuGet work package: it replaces the body of
// npmReinstate.
func npmAdminCmd(args []string, s streams) error {
	if len(args) == 0 || args[0] != "reinstate" {
		return usageError{npmAdminUsage}
	}
	return npmReinstate(args[1:], s)
}

// npmReinstate reverses the withdrawal of a version that a journal replay withheld (R-NPM-047, Q-NPM-8): it removes a
// `journal` tombstone only while the version is stored with the tombstone's integrity, increments rev_seq and writes
// npm.reinstate (R-NPM-044).
func npmReinstate(args []string, s streams) error {
	c := newFlags("admin npm reinstate", s)
	repo, rest := positional(args)
	nameVersion, rest := positional(rest)
	if _, _, err := c.parse(rest, map[string]string{"log-level": "log.level"}); err != nil {
		return err
	}
	at := strings.LastIndex(nameVersion, "@")
	if repo == "" || !strings.Contains(repo, "/") || at <= 0 || at == len(nameVersion)-1 || c.fs.NArg() > 0 {
		return usageError{npmAdminUsage}
	}
	return fmt.Errorf("admin npm reinstate: %w", errNotImplemented)
}
