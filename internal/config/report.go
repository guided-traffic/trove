package config

import (
	"fmt"
	"strings"
)

// Command names what is about to run, because the keys a command needs differ:
// `serve` needs a role, a Valkey and a key ring; `migrate` needs the database.
type Command int

// The commands that load a configuration.
const (
	// CommandServe is `trove serve`.
	CommandServe Command = iota
	// CommandMigrate is `trove migrate`.
	CommandMigrate
	// CommandAdmin is `trove admin …`.
	CommandAdmin
	// CommandValidate is `trove admin config validate`: every check of
	// CommandServe except that the role may be unset.
	CommandValidate
)

// runsReplica reports whether the command needs what a serving replica needs:
// Valkey, the object store and the key ring.
func (c Command) runsReplica() bool { return c == CommandServe || c == CommandValidate }

// Issue is one problem found in a configuration.
type Issue struct {
	Message string
}

// Error collects every issue of one load, so a fix needs one round trip
// (R-OPS-028: every error is reported in one pass).
type Error struct {
	Issues []Issue
}

// Error implements error.
func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("invalid configuration:")
	for _, i := range e.Issues {
		b.WriteString("\n  ")
		b.WriteString(i.Message)
	}
	return b.String()
}

// Messages returns the issue texts.
func (e *Error) Messages() []string {
	out := make([]string, len(e.Issues))
	for i, is := range e.Issues {
		out[i] = is.Message
	}
	return out
}

// Report receives the problems a section finds in itself. Messages read
// "<section>.<key> <problem>".
type Report struct {
	prefix string
	issues *[]Issue
	role   string
}

// Role is the replica's role (process.role) as loaded, empty when it is unset or
// the root is not a Config. A section uses it for rules that bind some roles
// only, for example the spool a role that accepts uploads needs.
func (r *Report) Role() string { return r.role }

// Add records a problem with the key (relative to the section).
func (r *Report) Add(key, format string, args ...any) {
	*r.issues = append(*r.issues, Issue{Message: r.prefix + key + " " + fmt.Sprintf(format, args...)})
}

// Raw records a message as written, without a key prefix.
func (r *Report) Raw(msg string) {
	*r.issues = append(*r.issues, Issue{Message: msg})
}

// Validator is implemented (on the pointer) by a section that checks rules
// across its own fields. It runs on every load, whatever the command.
type Validator interface {
	Validate(r *Report)
}

// CommandValidator is implemented by a section that requires keys only for some
// commands, for example the role and the Valkey address of `serve`.
type CommandValidator interface {
	ValidateFor(cmd Command, r *Report)
}
