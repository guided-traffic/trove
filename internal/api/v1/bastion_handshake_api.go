package v1

import (
	"net/http"
	"time"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastionmain"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// The handshake operation of the Bastion interface (R-BAS-008, R-BAS-010). The Bastion token was checked by the
// recognition stage before the handler runs; the shared authorization step admits the route on that proof (the
// declaration {bastion: true}) and the handler never sees a subject of the decision point. Every answer carries
// iface.HeaderInterface; the bodies are the types of internal/bastion/iface.

// BastionHandshake implements GET /api/v1/bastion/handshake (R-BAS-010): the interface version, the identity
// and token expiry of the Bastion, the response base URL derived for this request, whether anonymous requests read
// anything, the download switch and the feed position. It records the last contact of the replica.
func (s *Server) BastionHandshake(w http.ResponseWriter, r *http.Request, _ gen.BastionHandshakeParams) {
	ctx := r.Context()
	b := s.o.Bastion
	rec, ok := bastionmain.RecognitionFrom(ctx)
	if !ok || b.Feed == nil || b.AnonymousReads == nil {
		httpserver.Problem(w, r, unavailable("The Bastion interface"))
		return
	}
	cursor, err := b.Feed.Position(ctx)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	info := httpserver.InfoFrom(ctx)
	if b.Contacts != nil {
		rep := bastionmain.Report{ClientAddress: info.Client.String(), UserAgent: r.UserAgent(), ResponseBaseURL: info.BaseURL}
		if !info.Client.IsValid() {
			rep.ClientAddress = ""
		}
		// The contact is a record about the call, not part of its answer: a failure to write it is logged
		// and the handshake still answers.
		if err := b.Contacts.Record(ctx, rec, rep); err != nil {
			bastionLog(r).Warn("the last contact of a Bastion replica could not be recorded", "bastion", rec.Bastion, "replica", rec.Replica, "error", err)
		}
	}
	writeInterface(w, r, http.StatusOK, iface.Handshake{
		InterfaceVersion: iface.InterfaceVersion,
		Bastion:          iface.HandshakeBastion{Name: rec.Bastion, TokenID: rec.TokenID, TokenExpiresAt: rec.TokenExpiresAt.UTC()},
		Installation:     iface.HandshakeInstallation{BaseURL: b.BaseURL, Release: b.Release},
		ResponseBaseURL:  info.BaseURL,
		AnonymousReads:   b.AnonymousReads(ctx),
		RecordDownloads:  b.Downloads != nil && b.Downloads.Records(),
		Feed:             iface.HandshakeFeed{Cursor: cursor},
		ServerTime:       time.Now().UTC(),
	})
}
