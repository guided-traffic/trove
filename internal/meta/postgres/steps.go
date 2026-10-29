package postgres

import (
	"crypto/sha256"
	"embed"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var embedded embed.FS

// Step phases (R-HA-018).
const (
	phaseExpand   = "expand"
	phaseContract = "contract"
)

// Step is one migration file. Its SQL is never edited once applied: the runner
// compares the checksum with the one recorded in schema_migration.
type Step struct {
	Version     int
	Name        string // file name without ".sql", e.g. 00004_coordination
	Phase       string // expand or contract
	NoTx        bool   // -- trove:no-transaction
	RequiresApp string // contract steps: the oldest release allowed to serve afterwards
	SQL         string
	Checksum    [sha256.Size]byte
}

var fileName = regexp.MustCompile(`^(\d{5})_([a-z0-9][a-z0-9_]*)\.sql$`)

// EmbeddedFS is the directory of the migrations compiled into the binary. Tests
// build a modified copy to inject a failing or an additional step.
func EmbeddedFS() fs.FS {
	sub, err := fs.Sub(embedded, "migrations")
	if err != nil {
		panic(err) // the directory is part of the embed pattern
	}
	return sub
}

// EmbeddedSteps parses the migrations compiled into the binary.
func EmbeddedSteps() ([]Step, error) { return ParseSteps(EmbeddedFS()) }

// RequiredSchema is the number of the newest expand step this binary embeds
// (R-HA-018): a replica serves only once schema_state.applied reaches it. It
// panics if the embedded migrations are invalid, which the unit tests prevent.
func RequiredSchema() int {
	steps, err := EmbeddedSteps()
	if err != nil {
		panic("postgres: invalid embedded migrations: " + err.Error())
	}
	return requiredSchema(steps)
}

func requiredSchema(steps []Step) int {
	n := 0
	for _, s := range steps {
		if s.Phase == phaseExpand {
			n = max(n, s.Version)
		}
	}
	return n
}

// ParseSteps reads every NNNNN_<name>.sql file in the root of fsys, checks the
// headers of R-ARCH-026 and returns the steps in order. The numbers must run
// 1..N without a gap, so a file that went missing is an error, not a silently
// skipped step.
func ParseSteps(fsys fs.FS) ([]Step, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	var steps []Step
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := fileName.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("migration file %q: the name must be NNNNN_<name>.sql with lower-case letters, digits and underscores", e.Name())
		}
		raw, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, err
		}
		v, _ := strconv.Atoi(m[1])
		step, err := parseStep(strings.TrimSuffix(e.Name(), ".sql"), v, raw)
		if err != nil {
			return nil, fmt.Errorf("migration file %s: %w", e.Name(), err)
		}
		steps = append(steps, step)
	}
	sort.Slice(steps, func(i, j int) bool { return steps[i].Version < steps[j].Version })
	for i, s := range steps {
		if s.Version != i+1 {
			return nil, fmt.Errorf("migration numbers must run 1..N without a gap: expected %05d, found %s", i+1, s.Name)
		}
	}
	return steps, nil
}

// contractStatements are what an expand step must not contain (R-HA-018).
var contractStatements = regexp.MustCompile(`(?i)\bdrop\s+(table|column|index|constraint|function|type|trigger|view|schema|sequence|extension)\b|\brename\b|\bset\s+not\s+null\b`)

func parseStep(name string, version int, raw []byte) (Step, error) {
	s := Step{Version: version, Name: name, SQL: string(raw), Checksum: sha256.Sum256(raw)}
	for _, line := range strings.Split(s.SQL, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if !strings.HasPrefix(t, "--") {
			break // the header is the leading comment block
		}
		body := strings.TrimSpace(strings.TrimPrefix(t, "--"))
		rest, ok := strings.CutPrefix(body, "trove:")
		if !ok {
			continue
		}
		key, arg, _ := strings.Cut(rest, " ")
		arg = strings.TrimSpace(arg)
		switch key {
		case "phase":
			if s.Phase != "" {
				return s, fmt.Errorf("more than one trove:phase header")
			}
			if arg != phaseExpand && arg != phaseContract {
				return s, fmt.Errorf("trove:phase must be expand or contract, got %q", arg)
			}
			s.Phase = arg
		case "no-transaction":
			s.NoTx = true
		case "requires-app":
			ver, ok := strings.CutPrefix(arg, ">=")
			if !ok {
				return s, fmt.Errorf("trove:requires-app must read \">= <version>\", got %q", arg)
			}
			ver = strings.TrimSpace(ver)
			if _, err := parseVersion(ver); err != nil || ver == "dev" {
				return s, fmt.Errorf("trove:requires-app: %q is not a release version", ver)
			}
			s.RequiresApp = ver
		default:
			return s, fmt.Errorf("unknown header trove:%s", key)
		}
	}
	if s.Phase == "" {
		return s, fmt.Errorf("missing -- trove:phase expand|contract header")
	}
	if s.Phase == phaseContract && s.RequiresApp == "" {
		return s, fmt.Errorf("a contract step needs -- trove:requires-app >= <version>")
	}
	if s.Phase == phaseExpand && s.RequiresApp != "" {
		return s, fmt.Errorf("trove:requires-app belongs to contract steps only")
	}
	body := stripSQLNoise(s.SQL)
	if strings.TrimSpace(body) == "" {
		return s, fmt.Errorf("the file holds no statement")
	}
	if s.Phase == phaseExpand {
		if m := contractStatements.FindString(body); m != "" {
			return s, fmt.Errorf("an expand step must not contain %q: that is a contract-class statement (R-HA-018)", strings.TrimSpace(m))
		}
	}
	if s.NoTx && strings.Count(strings.TrimRight(strings.TrimSpace(body), ";"), ";") > 0 {
		return s, fmt.Errorf("a trove:no-transaction step holds exactly one statement")
	}
	return s, nil
}

// stripSQLNoise removes comments, quoted strings and dollar-quoted bodies, so
// the statement checks look at SQL keywords only. It is a lint helper, not a
// parser: PostgreSQL parses the file when the runner executes it.
func stripSQLNoise(sql string) string {
	var b strings.Builder
	for i := 0; i < len(sql); {
		switch {
		case strings.HasPrefix(sql[i:], "--"):
			for i < len(sql) && sql[i] != '\n' {
				i++
			}
		case strings.HasPrefix(sql[i:], "/*"):
			end := strings.Index(sql[i+2:], "*/")
			if end < 0 {
				return b.String()
			}
			i += end + 4
		case sql[i] == '\'':
			i++
			for i < len(sql) {
				if sql[i] == '\'' {
					if i+1 < len(sql) && sql[i+1] == '\'' {
						i += 2
						continue
					}
					break
				}
				i++
			}
			i++
			b.WriteString("''")
		case sql[i] == '$':
			tag := dollarTag(sql[i:])
			if tag == "" {
				b.WriteByte(sql[i])
				i++
				continue
			}
			end := strings.Index(sql[i+len(tag):], tag)
			if end < 0 {
				return b.String()
			}
			i += len(tag) + end + len(tag)
			b.WriteString("''")
		default:
			b.WriteByte(sql[i])
			i++
		}
	}
	return b.String()
}

// dollarTag returns "$tag$" if s starts with a dollar-quote opener.
func dollarTag(s string) string {
	for j := 1; j < len(s); j++ {
		c := s[j]
		switch {
		case c == '$':
			return s[:j+1]
		case c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || j > 1 && c >= '0' && c <= '9':
		default:
			return ""
		}
	}
	return ""
}
