// Command trove is the one binary: the replica roles, the migration runner and
// the administration commands (R-ARCH-001). This file only dispatches
// subcommands; each body lives in its own file.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/guided-traffic/trove/internal/buildinfo"
	"github.com/guided-traffic/trove/internal/config"
)

// streams are the process's standard streams and environment, injected so the
// tests can run the commands in-process.
type streams struct {
	stdout, stderr io.Writer
	environ        []string
}

// errNotImplemented marks a subcommand whose body a later work package fills.
var errNotImplemented = errors.New("not implemented yet")

// usageError makes run exit with status 2 instead of 1.
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

const usageText = `usage: trove <command> [flags]

commands:
  serve --role web|worker|all   run one replica of a role
  migrate [up|status]           apply or list the embedded migrations
  admin config validate         validate the configuration and print it, redacted
  version, --version            print version, commit and build date

flags common to serve, migrate and admin:
  --config <file>               configuration file (default /etc/trove/trove.yaml, or TROVE_CONFIG_FILE)
  --log-level <level>           overrides log.level
`

func main() {
	os.Exit(run(os.Args[1:], streams{stdout: os.Stdout, stderr: os.Stderr, environ: os.Environ()}))
}

func run(args []string, s streams) int {
	if len(args) == 0 {
		fmt.Fprint(s.stderr, usageText)
		return 2
	}
	var err error
	switch args[0] {
	case "serve":
		err = serveCmd(args[1:], s)
	case "migrate":
		err = migrateCmd(args[1:], s)
	case "admin":
		err = adminCmd(args[1:], s)
	case "version", "--version", "-version":
		fmt.Fprintln(s.stdout, buildinfo.Get())
	case "help", "-h", "--help":
		fmt.Fprint(s.stdout, usageText)
	case "backup":
		err = fmt.Errorf("backup: %w", errNotImplemented)
	default:
		err = usageError{fmt.Sprintf("unknown command %q (see trove help)", args[0])}
	}
	if err == nil {
		return 0
	}
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	fmt.Fprintf(s.stderr, "trove: %v\n", err)
	var ec interface{ ExitCode() int }
	if errors.As(err, &ec) {
		return ec.ExitCode()
	}
	var ue usageError
	if errors.As(err, &ue) {
		return 2
	}
	return 1
}

// commonFlags are the flags every config-reading subcommand accepts.
type commonFlags struct {
	fs       *flag.FlagSet
	file     string
	logLevel string
}

func newFlags(name string, s streams) *commonFlags {
	c := &commonFlags{fs: flag.NewFlagSet("trove "+name, flag.ContinueOnError)}
	c.fs.SetOutput(s.stderr)
	c.fs.StringVar(&c.file, "config", "", "configuration file")
	c.fs.StringVar(&c.logLevel, "log-level", "", "log level; overrides log.level")
	return c
}

// parse parses args and returns the flags that were actually given, keyed by
// configuration path, with their flag names.
func (c *commonFlags) parse(args []string, paths map[string]string) (values, names map[string]string, err error) {
	if err := c.fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, nil, err
		}
		return nil, nil, usageError{err.Error()}
	}
	values, names = map[string]string{}, map[string]string{}
	c.fs.Visit(func(f *flag.Flag) {
		if path, ok := paths[f.Name]; ok {
			values[path] = f.Value.String()
			names[path] = "--" + f.Name
		}
	})
	return values, names, nil
}

// loadConfig loads the configuration for a command.
func (c *commonFlags) loadConfig(s streams, cmd config.Command, flags, names map[string]string) (*config.Loaded, error) {
	return config.Load(config.Options{
		File:      c.file,
		Environ:   s.environ,
		Flags:     flags,
		FlagNames: names,
		Command:   cmd,
	})
}
