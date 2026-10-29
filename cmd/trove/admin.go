package main

import (
	"fmt"

	"github.com/guided-traffic/trove/internal/config"
)

const adminUsage = "usage: trove admin config validate [--config <file>] [--format table|json]"

// adminCmd dispatches the bootstrap and recovery commands (R-ARCH-001). Only
// `config validate` exists yet.
func adminCmd(args []string, s streams) error {
	if len(args) >= 2 && args[0] == "config" && args[1] == "validate" {
		return configValidate(args[2:], s)
	}
	return usageError{adminUsage}
}

// configValidate validates the file plus the environment and prints the
// effective configuration, redacted, with each value's source (R-OPS-028). It
// applies every check of `serve` except that the role may be unset.
func configValidate(args []string, s streams) error {
	c := newFlags("admin config validate", s)
	format := c.fs.String("format", "table", "output format: table or json")
	flags, names, err := c.parse(args, map[string]string{"log-level": "log.level"})
	if err != nil {
		return err
	}
	if *format != "table" && *format != "json" {
		return usageError{adminUsage}
	}
	loaded, err := c.loadConfig(s, config.CommandValidate, flags, names)
	if err != nil {
		return err
	}
	if *format == "json" {
		raw, err := loaded.EffectiveJSON()
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(s.stdout, string(raw))
		return err
	}
	if err := loaded.WriteTable(s.stdout); err != nil {
		return err
	}
	src := loaded.File
	if src == "" {
		src = "no file (defaults and environment)"
	}
	_, err = fmt.Fprintf(s.stderr, "configuration is valid (%s)\n", src)
	return err
}
