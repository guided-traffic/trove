package main

import (
	"fmt"

	"github.com/guided-traffic/trove/internal/config"
)

const adminUsage = `usage: trove admin <command> [flags]

commands:
  config validate [--format table|json]        validate the configuration and print it, redacted
  break-glass status|unlock|end-sessions       the break-glass lockout, rotation hold and sessions (R-AUTH-015)
  sessions end --user <username> | --all       end UI sessions (R-AUTH-015)
  service-account create <name> [--description <text>] [--admin]   create a service account (R-AUTH-015)
  token create --service-account <name|project/name> [--ttl 15m] [--scope-file <file>]   print a token once (R-AUTH-015)
  token revoke --id <uuid> | --value-stdin     revoke a token (R-AUTH-015)
  user disable <username> [--keep-tokens]      disable a user (R-AUTH-015)
  post-restore --restored-to <time> [--credentials-file <path>]   clean up after a point-in-time restore (R-HA-025)
  storage verify [--since <time>] [--json]     check that the object of every stored blob is in the object store (R-HA-024, R-HA-025)
  storage restore-versions --before <time> [--credentials-file <path>] [--dry-run]   make the objects deleted after a point-in-time restore point current again (R-HA-025)
  maintenance pause-deletions --reason <text> | resume-deletions   set or clear the deletion pause (R-HA-025)
  backup check [--json]                        verify the declared platform protection: archive_mode, bucket versioning (R-HA-023)
  npm reinstate <projectKey>/<repoKey> <name>@<version>   put back a version a journal replay withheld (R-NPM-047)
  oci reinstate <projectKey>/<repoKey> <name>@<digest>   put back a manifest a journal replay withheld (R-OCI-033)
  deb reinstate <projectKey>/<repoKey> <path>   put back a package file a journal replay withheld (R-DEB-032)
  import npm|nuget --from <source> --to <repository> [--package <name>]... [--state <file>]   copy the packages of an existing registry into a local repository (R-IMP-001)
  bastion create <name> [--description <text>]   register a Bastion (R-BAS-004)
  bastion token create <name> [--ttl 8760h]     print a Bastion token once (R-BAS-004)
  bastion token revoke <name> <token-id>        revoke a Bastion token (R-BAS-004)

every command takes --config <file> and --log-level <level>`

// adminCmd dispatches the bootstrap and recovery commands (R-ARCH-001,
// R-AUTH-015).
func adminCmd(args []string, s streams) error {
	if len(args) == 0 {
		return usageError{adminUsage}
	}
	switch args[0] {
	case "config":
		if len(args) >= 2 && args[1] == "validate" {
			return configValidate(args[2:], s)
		}
	case "break-glass":
		return breakGlassCmd(args[1:], s)
	case "sessions":
		return sessionsCmd(args[1:], s)
	case "service-account":
		return serviceAccountCmd(args[1:], s)
	case "token":
		return tokenCmd(args[1:], s)
	case "user":
		return userCmd(args[1:], s)
	case "bastion":
		return bastionCmd(args[1:], s)
	case "backup":
		return adminBackupCmd(args[1:], s)
	case "storage":
		return storageCmd(args[1:], s)
	case "maintenance":
		return maintenanceCmd(args[1:], s)
	case "post-restore":
		return postRestoreCmd(args[1:], s)
	case "npm":
		return npmAdminCmd(args[1:], s)
	case "oci":
		return ociAdminCmd(args[1:], s)
	case "deb":
		return debAdminCmd(args[1:], s)
	case "import":
		return importCmd(args[1:], s)
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
