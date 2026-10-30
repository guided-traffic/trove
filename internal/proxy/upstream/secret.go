package upstream

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/observability"
	"github.com/guided-traffic/trove/internal/repository"
)

// RefreshInterval is how long a resolved secretRef is used before the file or the
// variable is read again, so that a changed file takes effect within it (R-REPO-033
// names 60 s as the bound; Q-REPO-89). A reference that failed is tried again
// after RetryInterval.
const (
	RefreshInterval = 10 * time.Second
	RetryInterval   = time.Second

	// maxSecretBytes bounds a file: the value limit of the document, 8 KiB, and a
	// line break.
	maxSecretBytes = 8<<10 + 2
)

// errNotConfigured is why a file reference cannot be resolved without a directory.
var errNotConfigured = errors.New("config_as_code.secrets_dir is not set")

// resolver reads what a reference points at.
type resolver struct {
	dir      string
	lookup   func(string) (string, bool)
	readFile func(string) ([]byte, error)
}

// resolve reads a reference. The rules of R-OPS-029 are checked again here, so a
// row that was written around the API cannot route a trove file or variable to an
// upstream.
func (r resolver) resolve(ref repository.SecretRef) (observability.Secret, error) {
	if why := ref.Valid(); why != "" {
		return "", errors.New("the reference is invalid: " + why)
	}
	var val string
	switch {
	case ref.Env != "":
		v, ok := r.lookup(ref.Env)
		if !ok {
			return "", fmt.Errorf("the variable %s is not set", ref.Env)
		}
		val = v
	default:
		if r.dir == "" {
			return "", errNotConfigured
		}
		b, err := r.readFile(filepath.Join(r.dir, ref.File))
		if err != nil {
			return "", fmt.Errorf("the file %s cannot be read: %w", ref.File, err)
		}
		if len(b) >= maxSecretBytes {
			return "", fmt.Errorf("the file %s is larger than 8 KiB", ref.File)
		}
		val = string(b)
	}
	val = strings.TrimRight(val, "\r\n")
	switch {
	case val == "":
		return "", errors.New("the value is empty")
	case strings.ContainsAny(val, "\r\n\x00"):
		return "", errors.New("the value holds a line break or NUL")
	}
	return observability.Secret(val), nil
}

// readLimited reads a file up to maxSecretBytes.
func readLimited(name string) ([]byte, error) {
	f, err := os.Open(name) //nolint:gosec // G304: name is a validated reference joined to config_as_code.secrets_dir
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, maxSecretBytes))
}

// secretValue is one secret of a client: fixed, for a sealed value or an inline
// one, or a reference that is read again after RefreshInterval.
type secretValue struct {
	ref *repository.SecretRef
	res resolver
	now func() time.Time

	mu  sync.Mutex
	val observability.Secret
	err error
	at  time.Time
}

func newFixedSecret(v observability.Secret) *secretValue { return &secretValue{val: v} }

func newRefSecret(ref repository.SecretRef, res resolver, now func() time.Time) *secretValue {
	s := &secretValue{ref: &ref, res: res, now: now}
	s.val, s.err = res.resolve(ref)
	s.at = now()
	return s
}

// get returns the current value, resolving a reference again when it is due.
func (s *secretValue) get() (observability.Secret, error) {
	if s.ref == nil {
		return s.val, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	age := s.now().Sub(s.at)
	if (s.err == nil && age >= RefreshInterval) || (s.err != nil && age >= RetryInterval) {
		s.val, s.err = s.res.resolve(*s.ref)
		s.at = s.now()
	}
	return s.val, s.err
}

// credential is the authentication of a client: how the password goes into the
// Authorization header (R-REPO-033). It is the only place that builds the header;
// the hop policy decides who receives it.
type credential struct {
	mode     string
	username string
	secret   *secretValue // nil when no password is configured
}

// check reports why the credential cannot be used now: the failure of
// upstream_secret_unavailable, without the cause in the client-facing text.
func (c *credential) check(repo string) error {
	if c == nil || c.secret == nil {
		return nil
	}
	if _, err := c.secret.get(); err != nil {
		return secretUnavailable(repo, err)
	}
	return nil
}

// headers returns the Authorization header of the current secret; none for
// authMode none, or while the secret cannot be resolved (check has failed the
// request before it gets here).
func (c *credential) headers() http.Header {
	if c == nil || c.mode == repository.AuthNone {
		return nil
	}
	var pw string
	if c.secret != nil {
		v, err := c.secret.get()
		if err != nil {
			return nil
		}
		pw = v.Reveal()
	}
	h := http.Header{}
	switch c.mode {
	case repository.AuthBasic:
		h.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(c.username+":"+pw)))
	case repository.AuthBearer:
		h.Set("Authorization", "Bearer "+pw)
	}
	return h
}

// secretUnavailable is the 502 of a secret that cannot be unsealed or resolved
// (R-REPO-033). The cause is for the log; the client-facing text names the
// repository and nothing about the secret.
func secretUnavailable(repo string, cause error) *errs.Error {
	return errs.E(errs.UpstreamFailed, errs.CodeUpstreamSecretUnavailable,
		"The upstream credential of the repository "+repo+" cannot be unsealed or resolved on this replica.",
		errs.WithCause(cause))
}
