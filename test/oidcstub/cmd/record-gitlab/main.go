// Command record-gitlab re-records the GitLab fixtures of the issuer double (test/oidcstub/testdata)
// against a real GitLab instance (Q-CI-25; R-CI-003, R-CI-006, R-CI-012):
//
//	TROVE_GITLAB_URL=https://gitlab.example.com TROVE_GITLAB_TOKEN=<token with scope api> \
//	  go run ./test/oidcstub/cmd/record-gitlab [-ca-file ca.pem] [-out test/oidcstub/testdata]
//
// or `make oidc-fixtures`. The token's user must be an administrator (with Admin Mode on, the
// token also needs the scope admin_mode). The command creates, or reuses on a later run, the group
// trove-oidc-validation with the subgroups billing and partner, the project billing/invoice-api,
// its fork partner/invoice-api, a protected tag rule v*, the environments production (protected;
// the claim says so only on a licensed instance) and staging, an inactive pipeline schedule and
// the user trove-oidcv-partner (Reporter of the parent, Maintainer of partner), whose merge
// request from the fork GitLab cannot move into the parent. It commits .gitlab-ci.yml, starts one
// pipeline per case, reads the job logs, in which each job prints the base64url-decoded header and
// payload of its ID tokens and the shape of CI_JOB_TOKEN (never a raw token, never a signature),
// closes its merge requests and writes one anonymized fixture per case. It deletes nothing but the
// artifact of the signature check (see signature.go) and revokes nothing but the impersonation
// token it created for the partner user.
//
// The fixtures carry the instance's GitLab version. Anonymization replaces the instance's host by
// gitlab.example.com, the token user's name, login and e-mail addresses by neutral ones and the
// key IDs by placeholders of the same shape; it keeps every claim, its JSON type and the time
// relations. A run refuses to write a fixture in which the host, its domain or the user's
// e-mail address is still visible.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The environment variables of the instance and the token.
const (
	urlEnv   = "TROVE_GITLAB_URL"
	tokenEnv = "TROVE_GITLAB_TOKEN" //nolint:gosec // the name of a variable, not a credential
)

type config struct {
	base     string // the instance's base URL, no trailing slash
	token    string
	caFile   string
	out      string
	group    string
	jobImage string
	timeout  time.Duration
}

func main() {
	var cfg config
	flag.StringVar(&cfg.caFile, "ca-file", "", "PEM bundle trusted in addition to the system roots (an instance behind an internal CA)")
	flag.StringVar(&cfg.out, "out", "test/oidcstub/testdata", "directory the fixtures are written to")
	flag.StringVar(&cfg.group, "group", "trove-oidc-validation", "top-level group the recorder creates or reuses")
	flag.StringVar(&cfg.jobImage, "job-image", "", "image of the jobs on docker and kubernetes runners (POSIX sh, base64, tr; make oidc-fixtures passes INTEGRATION_TOOLS_IMAGE of tools/versions.env); a shell runner ignores it")
	flag.DurationVar(&cfg.timeout, "timeout", 30*time.Minute, "give up after this long")
	flag.Parse()
	cfg.base = strings.TrimSuffix(os.Getenv(urlEnv), "/")
	cfg.token = os.Getenv(tokenEnv)
	if err := cfg.check(); err != nil {
		fmt.Fprintln(os.Stderr, "record-gitlab:", err)
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.timeout)
	err := run(ctx, cfg)
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, "record-gitlab:", err)
		os.Exit(1)
	}
}

func (c config) check() error {
	if c.base == "" || c.token == "" {
		return fmt.Errorf("set %s (the instance's base URL) and %s (a token with scope api)", urlEnv, tokenEnv)
	}
	u, err := url.Parse(c.base)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("%s must be an https URL, got %q", urlEnv, c.base)
	}
	if c.group == "" || strings.Contains(c.group, "/") {
		return errors.New("-group must name a top-level group")
	}
	if c.jobImage == "" || strings.ContainsAny(c.jobImage, " \n'\"") {
		return errors.New("-job-image must name an image (make oidc-fixtures passes INTEGRATION_TOOLS_IMAGE)")
	}
	return nil
}

func httpClient(caFile string) (*http.Client, error) {
	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}
	if caFile != "" {
		pem, err := os.ReadFile(filepath.Clean(caFile))
		if err != nil {
			return nil, err
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("%s holds no PEM certificate", caFile)
		}
	}
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	return &http.Client{
		Transport: tr,
		Timeout:   60 * time.Second,
		// The API never redirects; a redirect would carry the token header to wherever it points.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, nil
}

func logf(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }

func run(ctx context.Context, cfg config) error {
	hc, err := httpClient(cfg.caFile)
	if err != nil {
		return err
	}
	c := &client{base: cfg.base, token: cfg.token, hc: hc}

	var ver struct {
		Version string `json:"version"`
	}
	if err := c.call(ctx, http.MethodGet, "/version", nil, &ver); err != nil {
		return err
	}
	var me user
	if err := c.call(ctx, http.MethodGet, "/user", nil, &me); err != nil {
		return err
	}
	logf("GitLab %s, recording as user %d", ver.Version, me.ID)

	iss, err := recordIssuer(ctx, c)
	if err != nil {
		return fmt.Errorf("discovery and key set: %w", err)
	}
	env, err := ensureSetup(ctx, c, cfg.group)
	if err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	runID := time.Now().UTC().Format("20060102t150405")
	pipes, err := startPipelines(ctx, c, env, runID, ciConfig(runID, cfg.jobImage))
	if err != nil {
		return err
	}
	if err := waitPipelines(ctx, c, pipes); err != nil {
		return err
	}
	if err := closeMergeRequests(ctx, c, env, runID); err != nil {
		return err
	}
	logs, err := collect(ctx, c, pipes)
	if err != nil {
		return err
	}
	sig, err := checkSignature(ctx, c, pipes, logs, iss)
	if err != nil {
		return fmt.Errorf("signature check: %w", err)
	}
	iss.Signature = sig

	an := newAnonymizer(cfg.base, iss.kids, person{me, "jdoe", "Jane Doe"}, person{env.partnerUser, "jroe", "John Roe"})
	w := writer{dir: cfg.out, version: ver.Version, day: time.Now().UTC().Format(time.DateOnly), an: an}
	return w.writeAll(iss, logs, env)
}
