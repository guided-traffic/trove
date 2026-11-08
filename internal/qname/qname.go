// Package qname is the identity of a repository and of the objects a project holds
// (Q-REPO-212, Q-AUTHZ-209), and the one parser of the paths that address a
// repository.
//
// A repository is named by its project key and its own key, `web/npm-remote`; the
// key is unique within the project. A project's service account, policy and CI
// mapping is named `<project key>/<name>`, an instance-wide one by its bare name.
// `/` is in no project key, repository key or object name, so the qualified form is
// unambiguous and needs no reservation or overlap rule.
//
// The package imports the standard library only, so that the authorization engine,
// the formats, the edge and the Bastion spell one address.
package qname

import (
	"errors"
	"fmt"
	"strings"
)

// Errors of the parsers; callers test them with errors.Is.
var (
	// ErrNotQualified: the text is not "<project>/<key>" (one segment, or more than two).
	ErrNotQualified = errors.New("qname: not a qualified name <project>/<name>")
	// ErrInvalidProject: the project part is no project key.
	ErrInvalidProject = errors.New("qname: invalid project key")
	// ErrInvalidKey: the key part is no repository key.
	ErrInvalidKey = errors.New("qname: invalid repository key")
)

// CacheSuffix is the suffix of a repository key that addresses the cache view of a
// remote repository (R-REPO-032). It belongs to the repository segment of a path only.
const CacheSuffix = "~cache"

// The rules of the keys in words, for the messages of a refusal. The parsers below are the one source of the grammar.
const (
	ProjectKeyRule = "2 to 32 characters: lower-case letters, digits and hyphens, starting with a letter, ending with a letter or digit"
	KeyRule        = "2 to 64 characters: lower-case letters and digits, separated by single dots or underscores, a double underscore or hyphens; starting and ending with a letter or digit"
)

// ValidProjectKey reports whether s is a project key: ^[a-z][a-z0-9]*(?:-+[a-z0-9]+)*$ with 2 to 32 characters (R-OCI-002,
// Q-OCI-3). A project key is a path component of every image reference, so it is an OCI name component without dots and
// underscores.
func ValidProjectKey(s string) bool {
	if len(s) < 2 || len(s) > 32 || !isLower(s[0]) || !isLower(s[len(s)-1]) && !isDigit(s[len(s)-1]) {
		return false
	}
	for i := 1; i < len(s); i++ {
		if c := s[i]; !isLower(c) && !isDigit(c) && c != '-' {
			return false
		}
	}
	return true
}

// ValidKey reports whether s is a repository key (R-REPO-002, R-OCI-002, Q-OCI-3): ^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*$ with
// 2 to 64 characters, the component grammar of the OCI distribution specification: runs of lower-case letters and digits,
// separated by one dot, one underscore, two underscores or any number of hyphens. A key does not start or end with a
// separator, which rules out ".." and a trailing ".". It does not look at reserved names.
func ValidKey(s string) bool {
	if len(s) < 2 || len(s) > 64 {
		return false
	}
	alnum := func(c byte) bool { return isLower(c) || isDigit(c) }
	if !alnum(s[0]) || !alnum(s[len(s)-1]) {
		return false
	}
	for i := 1; i < len(s); {
		if alnum(s[i]) {
			i++
			continue
		}
		// s[i] starts a separator; it is followed by an alphanumeric (s ends with one), within the separator rules.
		j := i
		switch s[i] {
		case '.':
			j = i + 1
		case '_':
			j = i + 1
			if j < len(s) && s[j] == '_' {
				j++
			}
		case '-':
			for j < len(s) && s[j] == '-' {
				j++
			}
		default:
			return false
		}
		if j >= len(s) || !alnum(s[j]) {
			return false
		}
		i = j + 1
	}
	return true
}

// ValidFormat reports whether s has the shape of a format ID: the shape of a project key and
// not ^v[0-9]+$, which is reserved for API versions.
func ValidFormat(s string) bool {
	if !ValidProjectKey(s) { // the same character rule and length
		return false
	}
	if s[0] == 'v' {
		digits := true
		for i := 1; i < len(s); i++ {
			digits = digits && isDigit(s[i])
		}
		return !digits
	}
	return true
}

// Repo names a repository: the key of its project and its own key, unique within the
// project. The zero value names nothing. It is comparable and a map key; the int64
// repository ID stays below the name.
type Repo struct {
	Project string
	Key     string
}

// IsZero reports whether r names nothing.
func (r Repo) IsZero() bool { return r == Repo{} }

// Valid reports whether both parts have the shape of their key.
func (r Repo) Valid() bool { return ValidProjectKey(r.Project) && ValidKey(r.Key) }

// String is "<project>/<key>", "web/npm-remote"; the zero value prints "".
// Persisted names (job targets, audit events, cache keys) store this form.
func (r Repo) String() string {
	if r.IsZero() {
		return ""
	}
	return r.Project + "/" + r.Key
}

// MarshalText implements encoding.TextMarshaler, so that a Repo is a JSON string and a JSON map key.
func (r Repo) MarshalText() ([]byte, error) { return []byte(r.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler; the text is parsed by [ParseRepo].
func (r *Repo) UnmarshalText(b []byte) error {
	v, err := ParseRepo(string(b))
	if err != nil {
		return err
	}
	*r = v
	return nil
}

// ParseRepo parses "<project>/<key>": exactly one slash, a project key before it and a
// repository key after it. A key with the cache suffix is not a repository.
func ParseRepo(s string) (Repo, error) {
	project, key, ok := strings.Cut(s, "/")
	if !ok || strings.Contains(key, "/") {
		return Repo{}, fmt.Errorf("%w: %q", ErrNotQualified, s)
	}
	if !ValidProjectKey(project) {
		return Repo{}, fmt.Errorf("%w: %q", ErrInvalidProject, project)
	}
	if !ValidKey(key) {
		return Repo{}, fmt.Errorf("%w: %q", ErrInvalidKey, key)
	}
	return Repo{Project: project, Key: key}, nil
}

// Object names a service account, a permission policy or a CI mapping: Project is the
// key of its project, empty for an instance-wide object. The name rules differ per
// kind and stay with the kind; this type holds only the structure.
type Object struct {
	Project string
	Name    string
}

// String is "<project>/<name>" for a project's object, the bare name otherwise; it is
// the name as stored.
func (o Object) String() string {
	if o.Project == "" {
		return o.Name
	}
	return o.Project + "/" + o.Name
}

// MarshalText implements encoding.TextMarshaler.
func (o Object) MarshalText() ([]byte, error) { return []byte(o.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler; the text is parsed by [ParseObject].
func (o *Object) UnmarshalText(b []byte) error {
	v, err := ParseObject(string(b))
	if err != nil {
		return err
	}
	*o = v
	return nil
}

// ParseObject splits a stored or addressed name: "<project>/<name>" or a bare "<name>". At most
// one slash, a project key before it, a non-empty name without a slash after it. Whether the
// name is valid for its kind is the kind's rule.
func ParseObject(s string) (Object, error) {
	if s == "" {
		return Object{}, fmt.Errorf("%w: empty", ErrNotQualified)
	}
	project, name, ok := strings.Cut(s, "/")
	if !ok {
		return Object{Name: s}, nil
	}
	if name == "" || strings.Contains(name, "/") {
		return Object{}, fmt.Errorf("%w: %q", ErrNotQualified, s)
	}
	if !ValidProjectKey(project) {
		return Object{}, fmt.Errorf("%w: %q", ErrInvalidProject, project)
	}
	return Object{Project: project, Name: name}, nil
}

// Context is where a name is read: a project (its policies, CI mappings, virtual
// repositories and deployment targets name repositories by bare key) or the instance
// (global policies, token scopes, exposure rules, configuration: always qualified).
type Context struct {
	// Project is the project key; empty is the instance context.
	Project string
}

// Instance reports whether c is the instance context.
func (c Context) Instance() bool { return c.Project == "" }

func isLower(c byte) bool { return c >= 'a' && c <= 'z' }
func isDigit(c byte) bool { return c >= '0' && c <= '9' }
