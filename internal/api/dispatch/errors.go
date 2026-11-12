package dispatch

import (
	"net/http"
	"sort"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// registerShape makes a format's WriteError the error shape of its prefix
// /api/<id>/ (Q-ARCH-214). The chain renders its own refusals - 401 with its
// challenge, 421, 429, 503 busy - through httpserver.Problem, which consults the
// router's table by path prefix, so a package client gets the body and the
// challenge its protocol defines on every error response of its endpoints
// (R-NPM-005) and not a problem+json it cannot parse. The format's WriteError must
// not call httpserver.Problem itself: it is what Problem calls.
func registerShape(r *httpserver.Router, id string, f formats.Format) {
	r.HandleErrorShape("/api/"+id+"/", func(w http.ResponseWriter, req *http.Request, err error) {
		f.WriteError(w, req, err)
	})
}

// challenged is a format whose errors carry the challenge of the route's family (R-AUTH-002, Q-AUTH-235): the decision point
// answers a caller without a credential with the generic Bearer challenge, and a format whose WriteError passes the error's
// challenge on (npm, Debian, a format that keeps formats.Base's) would hand it to a package client, which sends its configured
// credentials only after a Basic challenge. The dispatcher keeps every format it serves in this form, so its own refusals, the
// handlers' errors (formats.ServeRoute) and the chain's refusals through the format's shape are rendered alike. A format with a
// fixed challenge (NuGet, Ansible) is unchanged.
type challenged struct{ formats.Format }

// WriteError implements formats.Format.
func (c challenged) WriteError(w http.ResponseWriter, r *http.Request, err error) {
	c.Format.WriteError(w, r, httpserver.PackageChallenge(r, err))
}

func sortStrings(s []string) { sort.Strings(s) }
