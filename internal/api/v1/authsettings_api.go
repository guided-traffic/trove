package v1

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/auth/oidc"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// The runtime authentication settings (R-AUTH-040). The row has no revision
// column; its ETag number is the microsecond time of its last change, which
// every update raises (Q-AUTH-40).

func seconds(d time.Duration) int { return int(d / time.Second) }

func lifetimes(def, maxTTL time.Duration) gen.AuthSettingsLifetimes {
	return gen.AuthSettingsLifetimes{DefaultTTLSeconds: seconds(def), MaxTTLSeconds: seconds(maxTTL)}
}

// authSettings builds the representation of R-AUTH-040: the effective runtime
// values, the YAML ceilings and the read-only status.
func (s *Server) authSettings(r *http.Request, row token.Settings) (gen.AuthSettings, error) {
	cfg := s.o.Auth.Config
	eff := s.o.Auth.Settings.Resolve(row)
	out := gen.AuthSettings{
		AnonymousAccess: eff.AnonymousAccess,
		Tokens: gen.AuthSettingsTokens{
			Pat: lifetimes(eff.PATDefaultTTL, eff.PATMaxTTL), Sat: lifetimes(eff.SATDefaultTTL, eff.SATMaxTTL), ExpiryNoticeSeconds: seconds(eff.ExpiryNotice),
		},
		Ceilings: gen.AuthSettingsCeilings{
			AnonymousAllowed: cfg.Anonymous.Allowed,
			Tokens: gen.AuthSettingsTokens{
				Pat: lifetimes(cfg.Tokens.PAT.DefaultTTL, cfg.Tokens.PAT.MaxTTL), Sat: lifetimes(cfg.Tokens.SAT.DefaultTTL, cfg.Tokens.SAT.MaxTTL),
				ExpiryNoticeSeconds: seconds(cfg.Tokens.ExpiryNotice),
			},
		},
	}
	st := &out.Status
	st.Providers = []gen.AuthSettingsProvider{}
	if s.o.Auth.Login != nil {
		for _, p := range s.o.Auth.Login.LoginOptions(eff.AnonymousAccess).Providers {
			st.Providers = append(st.Providers, gen.AuthSettingsProvider{Id: p.ID, DisplayName: p.DisplayName, Available: p.Available})
		}
	}
	st.BreakGlass.Enabled = cfg.BreakGlass.Enabled
	st.BreakGlass.PasswordStatus = gen.AuthSettingsStatusBreakGlassPasswordStatus(breakGlassPassword(cfg.BreakGlass, s.o.Auth.Login))
	if cfg.BreakGlass.Enabled && s.o.Auth.Manager != nil {
		u, err := s.o.Auth.Manager.GetUser(r.Context(), cfg.BreakGlass.Username)
		switch {
		case err == nil:
			st.BreakGlass.LockedUntil = optTime(u.LockedUntil)
		case !isNotFound(err):
			return gen.AuthSettings{}, err
		}
	}
	st.Sessions.IdleTimeoutSeconds = seconds(cfg.Sessions.IdleTimeout)
	st.Sessions.AbsoluteTimeoutSeconds = seconds(cfg.Sessions.AbsoluteTimeout)
	st.Sessions.MaxPerUser = cfg.Sessions.MaxPerUser
	st.Sessions.RecentAuthWindowSeconds = seconds(cfg.Sessions.RecentAuthWindow)
	st.Clients.BasicEnabled = cfg.Clients.Basic.Enabled
	return out, nil
}

// breakGlassPassword is the state of the break-glass password: the login
// service's, which knows whether the replica holds a usable one, or, on a
// replica without a login service, what the configuration alone shows.
func breakGlassPassword(cfg config.AuthBreakGlass, login LoginService) oidc.PasswordStatus {
	switch {
	case login != nil:
		return login.BreakGlassPassword()
	case !cfg.Enabled:
		return oidc.PasswordNotApplicable
	case cfg.PasswordFile == "":
		return oidc.PasswordNotConfigured
	}
	// A file is configured but this replica has no login service to say whether it
	// holds a usable password; "ok" would be a claim nobody checked.
	return oidc.PasswordUnusable
}

// settingsETag is the entity tag of the settings document: the revision of the
// settings row and a digest of the read-only status, which changes without the row
// (a lockout, a provider's discovery, the state of the break-glass password), so a
// client that holds the tag is not told 304 about a status that moved. A write
// takes only the revision out of it ([settingsRevision]).
func settingsETag(revision int64, status gen.AuthSettingsStatus) string {
	raw, _ := json.Marshal(status)
	sum := sha256.Sum256(raw)
	return ETag(revision, hex.EncodeToString(sum[:6]))
}

// settingsRevision reads the revision out of an If-Match that carries the tag of
// [settingsETag]; the status digest is not a precondition of a write.
func settingsRevision(p Precondition) int64 {
	if !p.Present || p.Any {
		return errs.AnyRevision
	}
	num, _, _ := strings.Cut(p.Tag[1:len(p.Tag)-1], "-")
	if n, err := strconv.ParseInt(num, 10, 64); err == nil && n >= 1 {
		return n
	}
	return 0 // a tag the server never issued matches nothing
}

// GetAuthSettings implements GET /api/v1/settings/auth. It reads PostgreSQL, not
// the replica's cache, so the answer shows a change another replica committed
// (R-API-052).
func (s *Server) GetAuthSettings(w http.ResponseWriter, r *http.Request, _ gen.GetAuthSettingsParams) {
	if s.o.Auth.Settings == nil {
		httpserver.Problem(w, r, unavailable("The authentication settings"))
		return
	}
	row, err := s.o.Auth.Settings.Read(r.Context())
	if err != nil {
		failIdentity(w, r, err)
		return
	}
	out, err := s.authSettings(r, row)
	if err != nil {
		failIdentity(w, r, err)
		return
	}
	tag := settingsETag(row.Revision, out.Status)
	if NotModified(w, r, tag) {
		return
	}
	w.Header().Set("ETag", tag)
	writeJSON(w, r, http.StatusOK, out)
}

// settingsPatchBody is a merge patch of the runtime settings; ceilings and status
// are the read-only groups of the representation and are ignored.
type settingsPatchBody struct {
	AnonymousAccess *bool `json:"anonymousAccess"`
	Tokens          *struct {
		PAT                 *lifetimePatch `json:"pat"`
		SAT                 *lifetimePatch `json:"sat"`
		ExpiryNoticeSeconds *int           `json:"expiryNoticeSeconds"`
	} `json:"tokens"`
	Ceilings json.RawMessage `json:"ceilings"`
	Status   json.RawMessage `json:"status"`
}

type lifetimePatch struct {
	DefaultTTLSeconds *int `json:"defaultTTLSeconds"`
	MaxTTLSeconds     *int `json:"maxTTLSeconds"`
}

func duration(n *int) *time.Duration {
	if n == nil {
		return nil
	}
	d := time.Duration(*n) * time.Second
	return &d
}

// UpdateAuthSettings implements PATCH /api/v1/settings/auth (R-AUTH-040).
func (s *Server) UpdateAuthSettings(w http.ResponseWriter, r *http.Request, _ gen.UpdateAuthSettingsParams) {
	if s.o.Auth.Settings == nil {
		httpserver.Problem(w, r, unavailable("The authentication settings"))
		return
	}
	pre, err := ParseIfMatch(r)
	var raw []byte
	if err == nil {
		raw, err = ReadMergePatch(w, r, s.bodyLimit())
	}
	var in settingsPatchBody
	if err == nil {
		err = StrictUnmarshal(raw, &in)
	}
	if err != nil {
		failIdentity(w, r, err)
		return
	}
	p := token.SettingsPatch{AnonymousAccess: in.AnonymousAccess}
	if t := in.Tokens; t != nil {
		p.ExpiryNotice = duration(t.ExpiryNoticeSeconds)
		if t.PAT != nil {
			p.PATDefaultTTL, p.PATMaxTTL = duration(t.PAT.DefaultTTLSeconds), duration(t.PAT.MaxTTLSeconds)
		}
		if t.SAT != nil {
			p.SATDefaultTTL, p.SATMaxTTL = duration(t.SAT.DefaultTTLSeconds), duration(t.SAT.MaxTTLSeconds)
		}
	}
	p.Expected = settingsRevision(pre)
	row, err := s.o.Auth.Settings.Update(r.Context(), s.caller(r), p)
	if err != nil {
		failIdentity(w, r, err)
		return
	}
	out, err := s.authSettings(r, row)
	if err != nil {
		failIdentity(w, r, err)
		return
	}
	w.Header().Set("ETag", settingsETag(row.Revision, out.Status))
	writeJSON(w, r, http.StatusOK, out)
}
