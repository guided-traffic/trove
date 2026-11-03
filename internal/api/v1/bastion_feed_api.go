package v1

import (
	"net/http"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// The feed operation of the Bastion interface (R-BAS-008, R-BAS-015). The Bastion token was checked by the
// recognition stage before the handler runs; the shared authorization step admits the route on that proof (the
// declaration {bastion: true}). Every answer carries iface.HeaderInterface.

// BastionFeed implements GET /api/v1/bastion/feed (R-BAS-015): the entries after the cursor and the entries
// within the lookback behind it, the cursor to continue from, whether the page was cut, and whether the cursor
// is no longer valid.
func (s *Server) BastionFeed(w http.ResponseWriter, r *http.Request, params gen.BastionFeedParams) {
	feed := s.o.Bastion.Feed
	if feed == nil {
		httpserver.Problem(w, r, unavailable("The Bastion feed"))
		return
	}
	limit := 0
	if params.Limit != nil {
		if *params.Limit < 1 {
			httpserver.Problem(w, r, errs.E(errs.Invalid, errs.CodeInvalid, "The limit must be at least 1.",
				errs.WithFields(errs.FieldError{Pointer: "limit", Code: "range", Detail: "must be at least 1"})))
			return
		}
		limit = *params.Limit
	}
	ans, err := feed.Read(r.Context(), params.After, limit)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	writeInterface(w, r, http.StatusOK, ans)
}
