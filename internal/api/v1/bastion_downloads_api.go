package v1

import (
	"net/http"
	"strconv"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastionmain"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// The downloads operation of the Bastion interface (R-BAS-008, R-BAS-016). The Bastion token was checked by the
// recognition stage before the handler runs; the shared authorization step admits the route on that proof (the
// declaration {bastion: true}). Every answer carries iface.HeaderInterface.

// downloadEventBytes bounds the body of the intake per event: a path and a user agent are clipped to 4 KiB
// each by the audit table, and the other members are short.
const downloadEventBytes = 10 << 10

// BastionDownloads implements POST /api/v1/bastion/downloads (R-BAS-016): the downloads the Bastion served from
// its cache become asynchronous artifact.downloaded events. With audit.record_downloads off the answer is 204 and
// nothing is recorded.
func (s *Server) BastionDownloads(w http.ResponseWriter, r *http.Request, _ gen.BastionDownloadsParams) {
	in := s.o.Bastion.Downloads
	rec, ok := bastionmain.RecognitionFrom(r.Context())
	if !ok || in == nil {
		httpserver.Problem(w, r, unavailable("The Bastion download intake"))
		return
	}
	body, err := ReadBody(w, r, MediaJSON, int64(s.o.Bastion.Config.Downloads.MaxBatch)*downloadEventBytes+(64<<10))
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	var req iface.DownloadsRequest
	if err := StrictUnmarshal(body, &req); err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	if _, err := in.Accept(r.Context(), rec, req); err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	w.Header().Set(iface.HeaderInterface, strconv.Itoa(iface.InterfaceVersion))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}
