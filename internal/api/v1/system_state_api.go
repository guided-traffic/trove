package v1

import (
	"net/http"

	"github.com/guided-traffic/trove/internal/api/edge"
	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/sysstate"
)

// The banner and the read-only maintenance mode (R-OPS-025, R-OPS-024; the settings surface of R-API-037). Both live in system_state
// behind internal/sysstate, which stores them with their audit event and propagates them to every replica. The reads go to PostgreSQL
// so that an answer shows a write of another replica (R-API-052); the places that enforce the mode ask the replica's cached copy.

func (s *Server) systemState() (*sysstate.Store, error) {
	if s.o.SystemState == nil {
		return nil, unavailable("The system settings")
	}
	return s.o.SystemState, nil
}

func bannerBody(b sysstate.Banner) gen.Banner {
	out := gen.Banner{Enabled: b.Enabled}
	if b.Severity != "" {
		sev := gen.BannerSeverity(b.Severity)
		out.Severity = &sev
	}
	if b.Title != "" {
		out.Title = &b.Title
	}
	if b.Message != "" {
		out.Message = &b.Message
	}
	return out
}

func maintenanceBody(m sysstate.Maintenance) gen.MaintenanceMode {
	out := gen.MaintenanceMode{Mode: gen.MaintenanceModeMode(m.Mode), CacheFill: m.CacheFill}
	if m.Message != "" {
		out.Message = &m.Message
	}
	if m.ReadOnly() {
		retry := m.RetryAfterSeconds
		out.RetryAfterSeconds = &retry
		out.Since = optTime(m.Since)
		if m.By != "" {
			out.By = &m.By
		}
	}
	return out
}

// GetSystemBanner implements GET /api/v1/system/banner (R-OPS-025): public, because the sign-in page shows it.
func (s *Server) GetSystemBanner(w http.ResponseWriter, r *http.Request) {
	store, err := s.systemState()
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	st, err := store.Read(r.Context())
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "api.getSystemBanner"))
		return
	}
	writeJSON(w, r, http.StatusOK, bannerBody(st.Banner))
}

// bannerRequest is the body of PUT /api/v1/system/banner. Enabled is a pointer so that a body without it is told from one that
// switches the banner off.
type bannerRequest struct {
	Enabled  *bool   `json:"enabled"`
	Severity *string `json:"severity"`
	Title    *string `json:"title"`
	Message  *string `json:"message"`
}

// PutSystemBanner implements PUT /api/v1/system/banner (R-OPS-025); settings:write was decided by the shared step.
func (s *Server) PutSystemBanner(w http.ResponseWriter, r *http.Request) {
	store, err := s.systemState()
	var in bannerRequest
	if err == nil {
		err = DecodeJSON(w, r, &in, s.bodyLimit())
	}
	if err == nil && in.Enabled == nil {
		err = errs.E(errs.Invalid, errs.CodeValidation, "The request is invalid.",
			errs.WithFields(errs.FieldError{Pointer: "/enabled", Code: "required", Detail: "The banner is switched on or off: enabled is required."}))
	}
	var out sysstate.Banner
	if err == nil {
		out, err = store.SetBanner(r.Context(), s.sysCaller(r), sysstate.BannerInput{
			Enabled: *in.Enabled, Severity: strOf(in.Severity), Title: strOf(in.Title), Message: strOf(in.Message),
		})
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, bannerBody(out))
}

// GetSystemMaintenance implements GET /api/v1/system/maintenance (R-OPS-024): any authenticated caller.
func (s *Server) GetSystemMaintenance(w http.ResponseWriter, r *http.Request) {
	store, err := s.systemState()
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	st, err := store.Read(r.Context())
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "api.getSystemMaintenance"))
		return
	}
	writeJSON(w, r, http.StatusOK, maintenanceBody(st.Maintenance))
}

// PutSystemMaintenance implements PUT /api/v1/system/maintenance (R-OPS-024); settings:write was decided by the shared step, and the
// operation declares x-trove-maintenance: allowed, so an administrator can switch the mode off while it is on.
func (s *Server) PutSystemMaintenance(w http.ResponseWriter, r *http.Request) {
	store, err := s.systemState()
	var in gen.MaintenanceInput
	if err == nil {
		err = DecodeJSON(w, r, &in, s.bodyLimit())
	}
	var out sysstate.Maintenance
	if err == nil {
		retry := 0
		if in.RetryAfterSeconds != nil {
			retry = *in.RetryAfterSeconds
			if retry == 0 {
				retry = -1 // an explicit 0 is out of range, not the default
			}
		}
		out, err = store.SetMaintenance(r.Context(), s.sysCaller(r), sysstate.MaintenanceInput{
			Mode: string(in.Mode), CacheFill: in.CacheFill, Message: strOf(in.Message), RetryAfterSeconds: retry,
		})
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, maintenanceBody(out))
}

func (s *Server) sysCaller(r *http.Request) sysstate.Caller {
	p := httpserver.PrincipalFrom(r.Context())
	return sysstate.Caller{Actor: edge.ActorFrom(p), Source: auditSource(r)}
}

func strOf[T ~string](p *T) string {
	if p == nil {
		return ""
	}
	return string(*p)
}

// maintenanceGate is the refusal of R-OPS-024 in the shared step: an operation that writes and does not declare
// x-trove-maintenance: allowed answers 503 unavailable with Retry-After and the operator's message while the installation is in
// read-only mode. It runs after the declaration was decided, so a caller who may not do the action is told 401 or 403 and learns
// nothing from the mode. A replica without the settings (a shell, a unit test) never refuses.
func (s *Server) maintenanceGate(r *http.Request, op Operation) error {
	if s.o.SystemState == nil || op.MaintenanceAllowed || !refusedInMaintenance(r.Method) {
		return nil
	}
	return s.o.SystemState.RefuseWrite(r.Context())
}
