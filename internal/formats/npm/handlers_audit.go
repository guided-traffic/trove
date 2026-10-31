package npm

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/npm/name"
	"github.com/guided-traffic/trove/internal/formats/npm/rows"
)

// The security audit (R-NPM-038). A remote repository forwards the bulk advisory request
// of npm 7 and later, pnpm 11 and later and `yarn npm audit` to its advisory provider
// through its upstream client, with only the names it has fetched from its upstream and
// that nothing marks internal; a virtual repository forwards through its first forwarding
// remote member (Q-NPM-62). The forwarded set is decided by one query before the provider
// is asked, so no connection is held while it answers (R-NPM-046).

// auditBulkPath is the bulk advisory endpoint below a registry URL, of trove and of the
// provider.
const auditBulkPath = "-/npm/v1/security/advisories/bulk"

// Bounds of an audit body beyond npm.audit.maxBodySize (Q-NPM-64).
const (
	// auditInflation is the ratio of R-NPM-038: a body may decompress to ten times
	// maxBodySize.
	auditInflation = 10
	// auditMaxNames bounds the names of one body, and so the parameter of the query.
	auditMaxNames = 100_000
)

// The reasons a name is withheld, in the order they are decided: a name is counted once,
// by the first that applies (Q-NPM-63).
const (
	withheldLocal      = "local"
	withheldClaimed    = "claimed"
	withheldPinned     = "pinned"
	withheldNotProxied = "not_proxied"
)

func withheldReasons() []string {
	return []string{withheldLocal, withheldClaimed, withheldPinned, withheldNotProxied}
}

// auditUnavailable is the 501 of R-NPM-038.
func auditUnavailable(key string) error {
	return errs.E(errs.NotImplemented, errs.CodeNPMAuditUnavailable,
		"security audit is not available on repository "+key+"; it is disabled or no upstream provides advisories")
}

// auditForwarder is the remote repository an audit is forwarded through.
type auditForwarder struct {
	// rq is the request addressed to the forwarding remote.
	rq  *formats.Request
	set auditSettings
	// virtualID is the requesting virtual repository whose pin rules apply; 0 for a
	// request addressed to the remote.
	virtualID int64
}

// forwarder finds the repository that forwards an audit of rq, or the 501 (Q-NPM-62). A
// request that may not reach an upstream never forwards: the cache view of a remote, an
// endpoint that does not fill caches, and an endpoint with exposure rules, where the split
// into forwarded and withheld names would tell which names the remote has fetched (R-NPM-048).
// A virtual repository forwards through the first enabled remote member in resolution order
// whose mode is forward, whoever asks, and the caller needs read on it (R-AUTHZ-016).
func (f *Format) forwarder(ctx context.Context, rq *formats.Request) (*auditForwarder, error) {
	if rq.CacheView || rq.Endpoint.Rules || !rq.Endpoint.CacheFill {
		return nil, auditUnavailable(rq.Repo.Key)
	}
	switch rq.Repo.Class {
	case formats.Remote:
		if s := settingsOf(rq.Repo.Config).Audit; s.Forward {
			return &auditForwarder{rq: rq, set: s}, nil
		}
	case formats.Virtual:
		members := slices.Clone(rq.Repo.Members())
		sort.SliceStable(members, func(i, j int) bool { return members[i].PriorityMember && !members[j].PriorityMember })
		for i := range members {
			m := &members[i]
			s := settingsOf(m.Config).Audit
			if m.Class != formats.Remote || m.Disabled || !s.Forward {
				continue
			}
			mrq := *rq
			mrq.Repo = m
			if err := rq.Host.Authorize(ctx, &mrq, formats.Target{Actions: []formats.Action{formats.ActionRead}}); err != nil {
				return nil, err
			}
			return &auditForwarder{rq: &mrq, set: s, virtualID: rq.RepoRef().ID}, nil
		}
	}
	return nil, auditUnavailable(rq.Repo.Key)
}

// handleAudit answers POST -/npm/v1/security/advisories/bulk (R-NPM-038).
func (f *Format) handleAudit(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	start := time.Now()
	fw, err := f.forwarder(ctx, rq)
	if err != nil {
		return err
	}
	body, err := readAuditBody(rq.Request, fw.rq.Repo.Key, fw.set.MaxBodySize)
	if err != nil {
		return err
	}
	forward, withheld, err := f.auditSplit(ctx, rq, fw, body)
	if err != nil {
		return err
	}
	rep := f.replicaOf(rq.Host)
	rep.met.auditWithheld(fw.rq.Repo.Key, withheld)
	notice := ""
	if n := len(body) - len(forward); n > 0 {
		notice = fmt.Sprintf("trove audited %d of %d packages; %d internal or not yet proxied names were not sent to the advisory provider",
			len(forward), len(body), n)
	}
	if len(forward) == 0 {
		auditHeaders(w, notice)
		return writeJSON(w, http.StatusOK, struct{}{})
	}
	payload, err := json.Marshal(forward)
	if err != nil {
		return errs.E(errs.Internal, errs.CodeInternal, "", errs.WithCause(err))
	}
	fctx, cancel := context.WithDeadline(ctx, start.Add(rep.auditBound(fw.set.Timeout)))
	defer cancel()
	resp, err := rq.Host.Remote().Exchange(fctx, fw.rq, formats.Exchange{
		Method: http.MethodPost, Base: fw.set.Provider, Path: auditBulkPath, Body: payload,
		Header: http.Header{"Content-Type": {"application/json"}, "Accept": {"application/json"}, "Accept-Encoding": {"identity"}},
	})
	switch {
	case errors.Is(err, formats.ErrOffline):
		return auditUnavailable(rq.Repo.Key)
	case err != nil:
		return providerFailed(fw.rq.Repo.Key, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return providerStatus(fw.rq.Repo.Key, resp.StatusCode)
	}
	if resp.ContentLength > MaxDocumentBytes {
		return errs.E(errs.UpstreamFailed, errs.CodeUpstreamItemTooLarge, "The answer of the advisory provider of "+fw.rq.Repo.Key+" is too large.")
	}
	auditHeaders(w, notice)
	h := w.Header()
	h.Set("Content-Type", "application/json")
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		h.Set("Content-Type", ct)
	}
	if ce := resp.Header.Get("Content-Encoding"); ce != "" {
		h.Set("Content-Encoding", ce)
	}
	if resp.ContentLength >= 0 {
		h.Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
	}
	w.WriteHeader(http.StatusOK)
	n, err := io.Copy(w, io.LimitReader(resp.Body, MaxDocumentBytes+1))
	if err == nil && n > MaxDocumentBytes {
		err = errors.New("npm: the answer of the advisory provider exceeds its bound")
	}
	return err
}

// auditHeaders sets the headers of an answer: never stored, and the notice when names
// were withheld (R-NPM-005).
func auditHeaders(w http.ResponseWriter, notice string) {
	w.Header().Set("Cache-Control", "no-store")
	// A volatile route has no content wrapper; the provider's answer is not to be sniffed.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if notice != "" {
		w.Header().Set("npm-notice", notice)
	}
}

// auditSplit decides which names of the body are forwarded (R-NPM-038, Q-NPM-63): the
// names the forwarding remote holds a stored document of and whose patterns, and those of
// the requesting virtual repository, admit, minus every name a local npm repository holds,
// a claim of any scope covers or a pin rule of the virtual repository selects. withheld
// counts the others by reason.
func (f *Format) auditSplit(ctx context.Context, rq *formats.Request, fw *auditForwarder, body map[string][]string) (map[string][]string, map[string]int, error) {
	withheld := map[string]int{}
	var asked []string
	for n := range body {
		if _, err := name.Parse(n); err != nil {
			withheld[withheldNotProxied]++ // no npm name, so never fetched
			continue
		}
		asked = append(asked, n)
	}
	forward := map[string][]string{}
	if len(asked) == 0 {
		return forward, withheld, nil
	}
	sort.Strings(asked)
	st, err := storeOf(rq.Host.FormatData(ID))
	if err != nil {
		return nil, nil, err
	}
	facts, err := st.AuditNames(ctx, fw.rq.RepoRef().ID, fw.virtualID, asked)
	if err != nil {
		return nil, nil, err
	}
	for _, a := range facts {
		reason := withheldReason(a, admitted(rq, fw, a.Name))
		if reason == "" {
			forward[a.Name] = body[a.Name]
			continue
		}
		withheld[reason]++
	}
	return forward, withheld, nil
}

// withheldReason is the first reason that withholds a name, "" for a name that is forwarded.
func withheldReason(a rows.AuditName, admitted bool) string {
	switch {
	case a.Local:
		return withheldLocal
	case a.Claimed:
		return withheldClaimed
	case a.Pinned:
		return withheldPinned
	case !a.Proxied || !admitted:
		return withheldNotProxied
	}
	return ""
}

// admitted says whether the include and exclude patterns of the forwarding remote and of
// the requesting virtual repository admit a name (R-REPO-005): a name they refuse is one
// the remote does not proxy, whatever it fetched before the pattern was set.
func admitted(rq *formats.Request, fw *auditForwarder, n string) bool {
	return fw.rq.Repo.Admits(n, false) && (fw.virtualID == 0 || rq.Repo.Admits(n, false))
}

// readAuditBody reads an audit body within its bounds (Q-NPM-64): maxBody bytes as sent,
// ten times that decompressed, at most auditMaxNames names, each a list of versions.
func readAuditBody(r *http.Request, key string, maxBody int64) (map[string][]string, error) {
	tooLarge := errs.E(errs.TooLarge, errs.CodeNPMTooLarge, fmt.Sprintf(
		"The audit body exceeds what %s accepts: %d bytes as sent, ten times that decompressed, %d package names.", key, maxBody, auditMaxNames))
	if r.ContentLength > maxBody {
		return nil, tooLarge
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		return nil, errs.E(errs.Invalid, errs.CodeInvalid, "The audit body could not be read.", errs.WithCause(err))
	}
	if int64(len(raw)) > maxBody {
		return nil, tooLarge
	}
	var rd io.Reader = bytes.NewReader(raw)
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding"))) {
	case "", "identity":
	case "gzip", "x-gzip":
		zr, err := gzip.NewReader(rd)
		if err != nil {
			return nil, errs.E(errs.Invalid, errs.CodeInvalid, "The audit body is no gzip stream.")
		}
		rd = zr
	default:
		return nil, errs.E(errs.Unsupported, errs.CodeUnsupported, "An audit body is sent as gzip or without a content coding.")
	}
	limit := maxBody * auditInflation
	plain, err := io.ReadAll(io.LimitReader(rd, limit+1))
	if err != nil {
		return nil, errs.E(errs.Invalid, errs.CodeInvalid, "The audit body is no gzip stream.")
	}
	if int64(len(plain)) > limit {
		return nil, tooLarge
	}
	invalid := errs.E(errs.Invalid, errs.CodeInvalid, "The audit body is not a JSON object of package names and lists of versions.")
	var body map[string][]string
	dec := json.NewDecoder(bytes.NewReader(plain))
	if err := dec.Decode(&body); err != nil || body == nil {
		return nil, invalid
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, invalid
	}
	if len(body) > auditMaxNames {
		return nil, tooLarge
	}
	for n, vs := range body {
		if vs == nil {
			body[n] = []string{}
		}
	}
	return body, nil
}

// providerFailed is the 502 of a forward that got no answer (R-NPM-038): the code of the
// engine's classification, the status of an upstream failure.
func providerFailed(key string, err error) error {
	e, ok := errs.As(err)
	if !ok {
		return errs.E(errs.UpstreamFailed, errs.CodeUpstreamUnreachable, "The advisory provider of "+key+" cannot be reached.", errs.WithCause(err))
	}
	if e.Kind == errs.UpstreamFailed {
		return e
	}
	return errs.E(errs.UpstreamFailed, e.Code, e.Detail, errs.WithCause(err))
}

// providerStatus is the 502 of a provider that did not answer 200.
func providerStatus(key string, status int) error {
	code := errs.CodeUpstreamUnexpectedStatus
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		code = errs.CodeUpstreamAuthFailed
	case status >= 500:
		code = errs.CodeUpstreamError
	}
	return errs.E(errs.UpstreamFailed, code, "The advisory provider of "+key+" answered "+strconv.Itoa(status)+".")
}

// handleAuditLegacy answers npm 6's audit endpoints, -/npm/v1/security/audits and
// …/audits/quick, with 501 (R-NPM-038, Q-NPM-65). npm 7 to 10 fall back to the quick
// endpoint when the bulk request fails, and then print this answer, so a repository without
// a forwarder answers the text of the bulk endpoint.
func (f *Format) handleAuditLegacy(ctx context.Context, rq *formats.Request, _ http.ResponseWriter) error {
	if _, err := f.forwarder(ctx, rq); err != nil {
		return err
	}
	return errs.E(errs.NotImplemented, errs.CodeNPMAuditUnavailable,
		"trove answers security audits on the bulk advisory endpoint only, which npm 7 and later, pnpm 11 and later and yarn npm audit use; "+
			"npm asks this endpoint after its bulk request failed, and npm audit --loglevel=verbose shows why")
}
