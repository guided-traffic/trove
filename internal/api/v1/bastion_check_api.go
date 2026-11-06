package v1

import (
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"github.com/guided-traffic/trove/internal/qname"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastionmain"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// The check operation of the Bastion interface (R-BAS-008, R-BAS-011, R-BAS-012). The Bastion token was checked
// by the recognition stage before the handler runs; the shared authorization step admits the route on that
// proof (the declaration {bastion: true}). The caller's credential is a different matter: it is validated by
// the authentication stage of the default endpoint, exactly as a direct request is, and the read shapes are
// the decision point's.

// bastionCheckPath is the path of the request the check hands to the authentication stage: a package-protocol path
// with a safe method, so that the stage applies the rules of a package read (an anonymous caller needs the
// switch of R-AUTH-037, the session cookie and the API-key header play no part) and no rule of the
// management API's bridge.
const bastionCheckPath = "/api/bastion-check"

// maxCredentialLen bounds the credential a check may carry; every token is far shorter.
const maxCredentialLen = 8192

// BastionCheck implements POST /api/v1/bastion/check (R-BAS-011).
func (s *Server) BastionCheck(w http.ResponseWriter, r *http.Request, _ gen.BastionCheckParams) {
	ctx := r.Context()
	b := s.o.Bastion
	begin, _ := s.o.Access.(Beginner)
	if b.Authenticate == nil || b.Shapes == nil || begin == nil {
		httpserver.Problem(w, r, unavailable("The Bastion check"))
		return
	}
	body, err := ReadBody(w, r, MediaJSON, s.bodyLimit())
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	var req iface.CheckRequest
	if err := StrictUnmarshal(body, &req); err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	addr, err := s.checkRequest(req)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}

	subject, p, err := s.checkSubject(r, req, addr)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	ans := iface.CheckAnswer{Subject: subject, Repositories: map[string]iface.RepositoryRead{}}
	if subject.Kind != iface.SubjectToken && subject.Kind != iface.SubjectAnonymous {
		// A subject that cannot be used carries no shapes; the Bastion answers its caller from the kind.
		writeInterface(w, r, http.StatusOK, ans)
		return
	}
	cctx := httpserver.WithPrincipal(ctx, p)
	names := make([]qname.Repo, len(req.Repositories))
	for i, text := range req.Repositories {
		names[i], _ = qname.ParseRepo(text) // checkRequest has validated the text
	}
	acc, err := begin.Begin(cctx, "", names...)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	shapes, err := b.Shapes.Shapes(cctx, acc.Index(), acc.Subject, names)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	for i, text := range req.Repositories {
		ans.Repositories[text] = repositoryRead(shapes[names[i]], subject.Kind == iface.SubjectAnonymous)
	}
	writeInterface(w, r, http.StatusOK, ans)
}

// checkRequest validates the semantics of a check body (R-BAS-011) and returns the client address.
func (s *Server) checkRequest(req iface.CheckRequest) (netip.Addr, error) {
	invalid := func(pointer, code, detail string, opts ...errs.Option) error {
		opts = append(opts, errs.WithFields(errs.FieldError{Pointer: pointer, Code: code, Detail: detail}))
		return errs.E(errs.Invalid, errs.CodeInvalid, "The check request is not valid.", opts...)
	}
	addr, err := netip.ParseAddr(req.ClientAddress)
	if err != nil {
		return netip.Addr{}, invalid("/clientAddress", "invalid_address", "must be an IPv4 or IPv6 address")
	}
	addr = addr.Unmap().WithZone("")
	if n := len(req.Repositories); n == 0 || n > s.o.Bastion.Config.Check.MaxRepositories {
		return netip.Addr{}, invalid("/repositories", "count", "must name between 1 and "+strconv.Itoa(s.o.Bastion.Config.Check.MaxRepositories)+" repositories")
	}
	for i, key := range req.Repositories {
		if _, perr := qname.ParseRepo(key); perr != nil || len(key) > 255 {
			return netip.Addr{}, invalid("/repositories/"+strconv.Itoa(i), "invalid_key", "must be a repository name <project>/<key> of at most 255 characters", errs.WithBadRequest())
		}
	}
	if req.Credential != nil && len(*req.Credential) > maxCredentialLen {
		return netip.Addr{}, invalid("/credential", "too_long", "is longer than any credential")
	}
	return addr, nil
}

// checkSubject runs the caller's credential through the authentication stage as a direct request from the
// caller's address would be run, and turns the verdict into the subject of the answer. The failed-credential
// throttle and the token.rejected event apply to that address (R-AUTH-038). Only a failure of the stage's own
// stores is an error of the operation.
func (s *Server) checkSubject(r *http.Request, req iface.CheckRequest, addr netip.Addr) (iface.Subject, httpserver.Principal, error) {
	cr := r.Clone(httpserver.WithClient(r.Context(), addr))
	cr.Method, cr.URL.Path, cr.URL.RawPath, cr.URL.RawQuery = http.MethodGet, bastionCheckPath, "", ""
	cr.Header, cr.Body = http.Header{}, http.NoBody
	if req.Credential != nil {
		cr.Header.Set("Authorization", "Bearer "+*req.Credential)
	}
	p, err := s.o.Bastion.Authenticate(cr)
	if err == nil {
		if p.Anonymous {
			return iface.Subject{Kind: iface.SubjectAnonymous}, p, nil
		}
		sub := iface.Subject{Kind: iface.SubjectToken, TokenID: p.Credential.TokenID, PrincipalID: p.ID}
		if e := p.Credential.ExpiresAt; e != nil {
			t := e.UTC()
			sub.ExpiresAt = &t
		}
		return sub, p, nil
	}
	e, ok := errs.As(err)
	if !ok {
		return iface.Subject{}, p, err
	}
	switch e.Code {
	case errs.CodeInvalidToken, errs.CodePrincipalDisabled:
		return iface.Subject{Kind: iface.SubjectInvalid}, p, nil
	case errs.CodeCredentialsRequired:
		return iface.Subject{Kind: iface.SubjectAnonymousRefused}, p, nil
	case errs.CodeRateLimited:
		secs := int((e.RetryAfter + time.Second - 1) / time.Second)
		return iface.Subject{Kind: iface.SubjectThrottled, RetryAfterSeconds: &secs}, p, nil
	}
	return iface.Subject{}, p, err
}

// repositoryRead converts a resolved shape to the wire form: a none always names the status the caller
// would receive, 401 for an anonymous caller and 404 otherwise when the resolver named none.
func repositoryRead(sh bastionmain.Shape, anonymous bool) iface.RepositoryRead {
	if sh.Read == "" || !sh.Read.Valid() {
		sh = bastionmain.Shape{Read: iface.ReadNone}
	}
	if sh.Read != iface.ReadNone {
		return iface.RepositoryRead{Read: sh.Read}
	}
	status := sh.Status
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
	default:
		status = http.StatusNotFound
		if anonymous {
			status = http.StatusUnauthorized
		}
	}
	return iface.RepositoryRead{Read: iface.ReadNone, Status: status}
}
