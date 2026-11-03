// Package bastionmark is the Mark stage of the Bastion chain on the main installation (R-BAS-013, R-BAS-014,
// Q-BAS-52, Q-BAS-70): for a request that carries a bastionmain.Recognition it installs the
// bastionmain.RouteObserver, refuses the writes a Bastion token must never make, and adds the marking headers
// at the status line; for every other request it strips every iface.HeaderPrefix header from the response, so
// that an ordinary caller never learns its own shape. No format code is involved: the stage knows the route
// from the observer calls of the dispatcher and of the raw file handler, the shape from the
// bastionmain.ShapeResolver, and what the lookups found from the bastionmain.Absence record.
//
// It is an edge package because it reads the caller the authentication stage found (httpserver.Principal).
package bastionmark

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastionmain"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// ClassProbe is the class label of trove_bastion_forwarded_requests_total for a write probe (R-BAS-014). It is
// no iface.Class: a probe answer is `pass` on the wire.
const ClassProbe = "probe"

// Options configure [New].
type Options struct {
	// Shapes computes the caller's read shape on the addressed repository (R-BAS-012). Required for a shape in
	// the marking; without it no response is `shared` or `private`.
	Shapes bastionmain.ShapeResolver
	// MainURL is the main installation's own base URL (server.base_url), which a refused write names so that
	// the client knows where to publish (R-BAS-014). Empty leaves the URLs out of the detail.
	MainURL string
	// Metrics counts the forwarded requests by registration and class; nil counts nothing.
	Metrics *bastionmain.Metrics
	// Log receives what the stage cannot say on the wire; nil discards it.
	Log *slog.Logger
}

// Marker is the Mark stage.
type Marker struct{ o Options }

// New returns the stage over its options.
func New(o Options) *Marker {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	return &Marker{o: o}
}

// Stage returns the middleware for bastionmain.Stages.Mark.
func (m *Marker) Stage() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec, ok := bastionmain.RecognitionFrom(r.Context())
			if !ok {
				next.ServeHTTP(&stripWriter{ResponseWriter: w}, r)
				return
			}
			st := &state{m: m, rec: rec, subject: iface.Subject{Kind: iface.SubjectInvalid}}
			ctx, absence := bastionmain.WithAbsence(r.Context())
			st.absence = absence
			ctx = httpserver.WithPrincipalNote(ctx, st.noteCaller)
			ctx = bastionmain.WithObserver(ctx, &observer{st: st})
			next.ServeHTTP(&markWriter{ResponseWriter: w, st: st}, r.WithContext(ctx))
		})
	}
}

// state is what one recognized request tells the marking: the caller, the route, the shape and the lookups.
type state struct {
	m       *Marker
	rec     bastionmain.Recognition
	absence *bastionmain.Absence

	mu      sync.Mutex
	subject iface.Subject // invalid until the authentication stage accepted a credential
	match   *bastionmain.RouteMatch
	kind    string
	read    iface.Read
	probe   bool // the request was answered as a probe
}

// noteCaller is called by the authentication stage with the caller it accepted. The subject of the marking is
// the token with its principal and expiry, the anonymous caller, or invalid for a credential the Bastion cannot
// use as one (a session cookie).
func (s *state) noteCaller(p httpserver.Principal) {
	sub := iface.Subject{Kind: iface.SubjectInvalid}
	switch {
	case p.Anonymous:
		sub = iface.Subject{Kind: iface.SubjectAnonymous}
	case p.Credential.TokenID != "" && p.ID != "":
		sub = iface.Subject{Kind: iface.SubjectToken, TokenID: p.Credential.TokenID, PrincipalID: p.ID, ExpiresAt: p.Credential.ExpiresAt}
	}
	s.mu.Lock()
	s.subject = sub
	s.mu.Unlock()
}

func (s *state) setRoute(mt bastionmain.RouteMatch) {
	s.mu.Lock()
	s.match, s.kind = &mt, mt.Kind
	s.mu.Unlock()
}

// observer is the bastionmain.RouteObserver of one recognized request.
type observer struct{ st *state }

// Matched refuses the writes of a Bastion and answers the probe of a read route that takes a body (R-BAS-014).
// A write is refused whether it came as a probe or, by a defect of the Bastion, as the real request: the
// Bastion token makes every write bastion_read_only.
func (o *observer) Matched(w http.ResponseWriter, _ *http.Request, mt bastionmain.RouteMatch) (bool, error) {
	st := o.st
	st.setRoute(mt)
	if mt.Method == http.MethodGet || mt.Method == http.MethodHead {
		return false, nil
	}
	if !readRoute(mt) {
		st.mu.Lock()
		st.probe = st.rec.Probe
		st.mu.Unlock()
		return false, st.m.readOnly(mt)
	}
	if !st.rec.Probe {
		// The real request of a read route that takes a body, forwarded after a probe answered 204.
		return false, nil
	}
	st.mu.Lock()
	st.probe = true
	st.mu.Unlock()
	w.Header().Set(iface.HeaderRoute, iface.RouteReadWithBody)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
	return true, nil
}

// readRoute reports whether the route reads: a kind that serves content or documents and the coarse action
// read. A kind this build does not know is no read route.
func readRoute(mt bastionmain.RouteMatch) bool {
	switch mt.Kind {
	case bastionmain.KindDownload, bastionmain.KindMetadata, bastionmain.KindComputed, bastionmain.KindVolatile:
		return mt.Action == authz.Read
	}
	return false
}

// readOnly is the refusal of a write in the shape of the format: 403 bastion_read_only, whose detail names
// the main installation's URL of the addressed repository (R-BAS-014).
func (m *Marker) readOnly(mt bastionmain.RouteMatch) error {
	detail := "This is a read-only Bastion; publish to the main installation."
	if base := strings.TrimRight(m.o.MainURL, "/"); base != "" && mt.RepoKey != "" {
		target := base + "/api/" + mt.Format + "/" + mt.RepoKey + "/"
		if mt.Format == bastionmain.FormatFiles {
			target = base + "/files/" + mt.RepoKey + "/"
		}
		detail = "This is a read-only Bastion of " + base + "; publish to " + target
	}
	return errs.E(errs.Forbidden, errs.CodeBastionReadOnly, detail)
}

// Decided computes the caller's read shape on the addressed repository (R-BAS-012). The subject and the
// index are the ones the decision just used, so the shape agrees with the decision of this request.
func (o *observer) Decided(r *http.Request, d bastionmain.RouteDecision) {
	st := o.st
	st.setRoute(d.Match)
	if st.m.o.Shapes == nil || d.Index == nil {
		return
	}
	key := d.Match.RepoKey
	shapes, err := st.m.o.Shapes.Shapes(r.Context(), d.Index, d.Subject, []string{key})
	if err != nil {
		// The marking stays without a shape: the response is `pass`, never shared.
		st.m.o.Log.Warn("bastion: the read shape could not be computed", "repository", key, "error", err)
		return
	}
	st.mu.Lock()
	st.read = shapes[key].Read
	st.mu.Unlock()
}

// marking derives the headers of the response from the state at the status line.
func (s *state) marking(status int) iface.Marking {
	s.mu.Lock()
	defer s.mu.Unlock()
	mk := iface.Marking{Class: iface.ClassPass, Interface: iface.InterfaceVersion, Subject: s.subject}
	if status == http.StatusUnauthorized {
		// A rejected credential, or a caller the main installation asks to sign in: either way the credential
		// is of no use to the Bastion (R-BAS-013).
		mk.Subject = iface.Subject{Kind: iface.SubjectInvalid}
	}
	if s.match == nil {
		return mk
	}
	mk.Repository = s.match.RepoKey
	mk.Read = s.read
	if s.probe {
		return mk
	}
	switch status {
	case http.StatusOK, http.StatusNotModified:
		switch s.kind {
		case bastionmain.KindDownload:
			mk.Class = iface.ClassArtifact
		case bastionmain.KindMetadata, bastionmain.KindComputed:
			switch mk.Read {
			case iface.ReadFull:
				mk.Class = iface.ClassShared
			case iface.ReadPartial, iface.ReadNone:
				mk.Class = iface.ClassPrivate
			}
		}
	case http.StatusNotFound:
		switch s.kind {
		case bastionmain.KindDownload, bastionmain.KindMetadata, bastionmain.KindComputed:
			if mk.Read == iface.ReadFull && s.absence.Absent() {
				mk.Class = iface.ClassAbsent
			}
		}
	}
	return mk
}

// markWriter adds the marking at the status line.
type markWriter struct {
	http.ResponseWriter
	st    *state
	wrote bool
}

func (w *markWriter) mark(code int) {
	if w.wrote {
		return
	}
	// 1xx informational responses do not start the response.
	if code >= 100 && code < 200 && code != http.StatusSwitchingProtocols {
		return
	}
	w.wrote = true
	h := w.Header()
	// Nothing below this stage states a marking but the observer's probe answer.
	probeRoute := h.Get(iface.HeaderRoute)
	iface.StripMarking(h)
	if w.st.probe && probeRoute != "" {
		h.Set(iface.HeaderRoute, probeRoute)
	}
	if w.st.rec.Route {
		// An interface route has no addressed repository and no caller but the Bastion.
		h.Set(iface.HeaderClass, string(iface.ClassPass))
		h.Set(iface.HeaderInterface, strconv.Itoa(iface.InterfaceVersion))
		return
	}
	mk := w.st.marking(code)
	mk.Set(h)
	if mt := w.st.m.o.Metrics; mt != nil && mt.Forwarded != nil {
		class := string(mk.Class)
		if w.st.probe {
			class = ClassProbe
		}
		mt.Forwarded.WithLabelValues(w.st.rec.Bastion, class).Inc()
	}
}

// WriteHeader implements http.ResponseWriter.
func (w *markWriter) WriteHeader(code int) {
	w.mark(code)
	w.ResponseWriter.WriteHeader(code)
}

// Write implements http.ResponseWriter.
func (w *markWriter) Write(p []byte) (int, error) {
	w.mark(http.StatusOK)
	return w.ResponseWriter.Write(p)
}

// Flush implements http.Flusher.
func (w *markWriter) Flush() {
	w.mark(http.StatusOK)
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

// Unwrap returns the wrapped writer for http.ResponseController.
func (w *markWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// stripWriter removes every iface.HeaderPrefix header from the response of a request that is no recognized
// Bastion request (R-BAS-013).
type stripWriter struct {
	http.ResponseWriter
	wrote bool
}

func (w *stripWriter) strip(code int) {
	if w.wrote || (code >= 100 && code < 200 && code != http.StatusSwitchingProtocols) {
		return
	}
	w.wrote = true
	iface.StripMarking(w.Header())
}

// WriteHeader implements http.ResponseWriter.
func (w *stripWriter) WriteHeader(code int) {
	w.strip(code)
	w.ResponseWriter.WriteHeader(code)
}

// Write implements http.ResponseWriter.
func (w *stripWriter) Write(p []byte) (int, error) {
	w.strip(http.StatusOK)
	return w.ResponseWriter.Write(p)
}

// Flush implements http.Flusher.
func (w *stripWriter) Flush() {
	w.strip(http.StatusOK)
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

// Unwrap returns the wrapped writer for http.ResponseController.
func (w *stripWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
