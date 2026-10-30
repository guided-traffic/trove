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

func sortStrings(s []string) { sort.Strings(s) }
