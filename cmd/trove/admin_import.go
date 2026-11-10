package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/importer"
	"github.com/guided-traffic/trove/internal/outbound"
)

const importUsage = `usage: trove admin import npm|nuget|ansible --from <source address> --to <trove repository address>
         [--package <name>]... [--packages-file <file>] [--search <text>]
         [--source-auth none|basic|bearer] [--source-username <name>] [--source-extra-host <pattern>]...
         [--state <file>] [--report <file>] [--concurrency 2] [--rate 5] [--max-package-bytes 536870912]
         [--spool-dir <dir>] [--dry-run] [--config <file>]

The trove token for the target is read from the file given by --token-file or from TROVE_IMPORT_TOKEN; the password or
token of the source from --source-password-file or TROVE_IMPORT_SOURCE_PASSWORD. Neither is ever taken as an argument.`

// The names of the environment variables that carry the two secrets, and the size limit of a secret file.
const (
	envToken          = "TROVE_IMPORT_TOKEN"           //nolint:gosec // G101: a variable name, not a credential
	envSourcePassword = "TROVE_IMPORT_SOURCE_PASSWORD" //nolint:gosec // G101: a variable name, not a credential
	maxSecretFile     = 8 << 10
)

// stringList is a flag that can be given more than once.
type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ",") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

// importCmd is trove admin import <format> (R-IMP-001): it copies the packages of an existing registry into a local repository
// of trove through the repository's publish protocol.
func importCmd(args []string, s streams) error {
	format, rest := positional(args)
	if format == "" {
		return usageError{importUsage}
	}
	c := newFlags("admin import", s)
	from := c.fs.String("from", "", "the source: the registry address (npm), the service index (NuGet) or the API root (Ansible Galaxy)")
	to := c.fs.String("to", "", "the target: the address of a local repository of trove, as a package manager is given it")
	var names, extra stringList
	c.fs.Var(&names, "package", "a package to copy; may be repeated")
	c.fs.Var(&extra, "source-extra-host", "a host pattern, besides the source's origin, that may serve files; may be repeated")
	pkgFile := c.fs.String("packages-file", "", "a file with one package name per line")
	search := c.fs.String("search", "", "also copy the packages the source's search finds for this text")
	auth := c.fs.String("source-auth", "none", "how the source is asked: none, basic or bearer")
	user := c.fs.String("source-username", "", "the account for --source-auth basic")
	srcPassFile := c.fs.String("source-password-file", "", "a file holding the source's password or token")
	tokenFile := c.fs.String("token-file", "", "a file holding the trove token of the target")
	state := c.fs.String("state", "", "the state file: a run that is started again with it skips the packages that were complete")
	report := c.fs.String("report", "", "write the report as JSON to this file")
	conc := c.fs.Int("concurrency", 2, "packages in flight")
	rate := c.fs.Float64("rate", 5, "requests per second towards the source; 0 for no limit")
	maxBytes := c.fs.Int64("max-package-bytes", importer.DefaultMaxBytes, "the largest file taken from the source")
	spool := c.fs.String("spool-dir", "", "where a file waits between download and publish (default: the temporary directory)")
	dry := c.fs.Bool("dry-run", false, "read both sides and report what would be copied; download and write nothing")
	flags, fnames, err := c.parse(rest, map[string]string{"log-level": "log.level"})
	if err != nil {
		return err
	}
	if c.fs.NArg() > 0 || *from == "" || *to == "" {
		return usageError{importUsage}
	}
	searchGiven := false
	c.fs.Visit(func(f *flag.Flag) { searchGiven = searchGiven || f.Name == "search" })

	token, err := secret(s, envToken, *tokenFile)
	if err != nil {
		return fmt.Errorf("the target token: %w", err)
	}
	srcPass, err := secret(s, envSourcePassword, *srcPassFile)
	if err != nil && (*auth == importer.AuthBasic || *auth == importer.AuthBearer) {
		return fmt.Errorf("the source password: %w", err)
	}
	if *pkgFile != "" {
		more, err := readNames(*pkgFile)
		if err != nil {
			return err
		}
		names = append(names, more...)
	}
	loaded, err := c.loadConfig(s, config.CommandAdmin, flags, fnames)
	if err != nil {
		return err
	}
	factory := outbound.New(loaded.Config.Proxy.Outbound)
	if err := factory.Err(); err != nil {
		return err
	}
	f, src, dst, err := importer.Build(factory, importer.Params{
		Format: format, From: *from, To: *to, SourceAuth: *auth, SourceUsername: *user, SourcePassword: srcPass,
		ExtraHosts: extra, Token: token, Rate: *rate,
	})
	if err != nil {
		return usageError{err.Error() + "\n" + importUsage}
	}
	opts := importer.Options{
		Format: f, Source: src, Target: dst, Names: names, Concurrency: *conc, Dir: *spool, MaxBytes: *maxBytes, DryRun: *dry,
		Progress: s.stderr,
	}
	if searchGiven {
		opts.Search = search
	}
	if *state != "" && !*dry {
		j, err := importer.OpenJournal(*state, f.ID(), src.Origin.String(), dst.Base.String())
		if err != nil {
			return err
		}
		defer j.Close()
		opts.Journal = j
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	rep, runErr := importer.Run(ctx, opts)
	if rep != nil {
		if err := writeReport(s, rep, *report); err != nil {
			return err
		}
	}
	if runErr != nil {
		return fmt.Errorf("the import was stopped: %w", runErr)
	}
	if !rep.OK() {
		return errors.New("the import finished with problems; see the report")
	}
	return nil
}

// secret reads a secret from a file or, without a file, from the environment. The file's trailing line break is removed, and a
// file over 8 KiB is refused (R-REPO-033).
func secret(s streams, env, file string) (string, error) {
	if file != "" {
		b, err := os.ReadFile(file) //nolint:gosec // G304: the operator's flag
		if err != nil {
			return "", err
		}
		if len(b) > maxSecretFile {
			return "", fmt.Errorf("%s is larger than 8 KiB", file)
		}
		v := strings.TrimRight(string(b), "\r\n")
		if v == "" {
			return "", fmt.Errorf("%s is empty", file)
		}
		return v, nil
	}
	for _, kv := range s.environ {
		if k, v, ok := strings.Cut(kv, "="); ok && k == env && v != "" {
			return v, nil
		}
	}
	return "", fmt.Errorf("give a file or set %s", env)
}

func readNames(path string) ([]string, error) {
	f, err := os.Open(path) //nolint:gosec // G304: the operator's flag
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			out = append(out, line)
		}
	}
	return out, sc.Err()
}

// writeReport prints the summary and the packages that need a look, and writes the JSON report when asked.
func writeReport(s streams, rep *importer.Report, path string) error {
	if path != "" {
		b, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
			return err
		}
	}
	totals, problems := rep.Totals()
	kinds := make([]string, 0, len(totals))
	for k := range totals {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, p := range rep.Packages {
		for _, v := range p.Versions {
			if v.Outcome == importer.OutcomeConflict || v.Outcome == importer.OutcomeFailed {
				fmt.Fprintf(s.stdout, "%s %s@%s: %s %s\n", v.Outcome, p.Name, v.Version, v.Code, v.Detail)
			}
		}
		if p.Error != "" {
			fmt.Fprintf(s.stdout, "failed %s: %s %s\n", p.Name, p.Code, p.Error)
		}
		for _, n := range p.NotKept {
			fmt.Fprintf(s.stdout, "not kept %s: %s\n", p.Name, n)
		}
		for _, n := range p.Notes {
			fmt.Fprintf(s.stdout, "note %s: %s\n", p.Name, n)
		}
	}
	line := fmt.Sprintf("%d packages", len(rep.Packages))
	for _, k := range kinds {
		line += fmt.Sprintf(", %d %s", totals[k], k)
	}
	fmt.Fprintf(s.stdout, "%s; %d packages need a look\n", line, problems)
	if rep.Aborted != "" {
		fmt.Fprintf(s.stdout, "stopped: %s\n", rep.Aborted)
	}
	return nil
}
