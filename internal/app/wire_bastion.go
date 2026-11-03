package app

import (
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"slices"
	"time"

	"github.com/guided-traffic/trove/internal/api/bastionmark"
	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/auth"
	"github.com/guided-traffic/trove/internal/bastionmain"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// The wiring of the main installation's side of the trove Bastion (docs/developer/bastion.md, R-BAS-004 to
// R-BAS-017). The contract work wires nothing but the seams' places: the chain stage of the default endpoint,
// which is empty until the work packages fill it.
//
// Each work package edits its own marked block, and only that one, and adds the fields it needs to App in
// its own block of app.go:
//
//   - package 1 (registrations, the bst token type): builds the bastionmain.TokenAuthenticator;
//   - package 2 (interface handlers, recognition): the Recognize stage and the interface services;
//   - package 3 (marking, write probe): builds the bastionmain.ShapeResolver (package 2's check uses it) and
//     the Mark stage.

// bastionStage returns the Bastion stage of an endpoint's chain (httpserver.Chain.Bastion): only the default
// endpoint serves the Bastion interface and recognizes Bastion requests (R-BAS-005), so every other endpoint
// gets none.
func (a *App) bastionStage(rt *endpointRuntime) (func(http.Handler) http.Handler, error) {
	if !rt.cfg.Default {
		return nil, nil
	}
	st, err := a.bastionStages(rt)
	if err != nil {
		return nil, err
	}
	return st.Chain(), nil
}

// bastionStages assembles the two stages. A stage whose package is not built is nil, and a nil stage is
// skipped, so a main installation without the Bastion work in this build behaves as before.
func (a *App) bastionStages(rt *endpointRuntime) (bastionmain.Stages, error) {
	var st bastionmain.Stages
	// >>> package 2
	forwarded, err := a.bastionForwarded(rt)
	if err != nil {
		return st, err
	}
	st.Recognize = a.bastionRecognize(forwarded)
	// <<< package 2

	// >>> package 3
	if shapes := a.bastionShapes(); shapes != nil {
		st.Mark = bastionmark.New(bastionmark.Options{
			Shapes: shapes, MainURL: a.cfg.Server.BaseURL, Metrics: a.bastionMetrics(), Log: a.log.With("component", "bastion"),
		}).Stage()
	}
	// <<< package 3
	return st, nil
}

// >>> package 1

// bastionRegistrations builds the registration service behind /api/v1/bastions (R-BAS-004). It is nil on a
// replica that has no identity area (a worker), whose API operations then answer 503.
func (a *App) bastionRegistrations() *bastionmain.Registrations {
	in, id := part[infra](a.parts), part[identityArea](a.parts)
	if in.db == nil || id.tokens == nil {
		return nil
	}
	return bastionmain.NewRegistrations(bastionmain.RegistrationOptions{
		DB: in.db, Notifier: in.changes, Recorder: in.rec, Tokens: id.tokens, Max: a.cfg.Bastions.MaxRegistrations,
	})
}

// bastionAuthenticator builds the bastionmain.TokenAuthenticator over the token service (R-BAS-005): the
// recognition stage of package 2 validates a Bastion token with it. It is nil where the token service is not
// built.
func (a *App) bastionAuthenticator() bastionmain.TokenAuthenticator {
	id := part[identityArea](a.parts)
	if id.tokens == nil {
		return nil
	}
	return bastionmain.NewAuthenticator(id.tokens)
}

// <<< package 1
// >>> package 3

// bastionShapes returns the ShapeResolver over the decision point (R-BAS-012): the Mark stage asks it for the
// caller's shape on the addressed repository and the check operation for a credential it has validated. A
// replica without the authorization area has none.
func (a *App) bastionShapes() bastionmain.ShapeResolver {
	eng := part[authzArea](a.parts).engine
	if eng == nil {
		return nil
	}
	return bastionmain.NewShapeResolver(eng)
}

// bastionMetrics returns the metrics of the main installation's side, registered once: the Mark stage counts
// the forwarded requests on the value the interface handlers count the interface requests on.
func (a *App) bastionMetrics() *bastionmain.Metrics {
	a.bastionMxOnce.Do(func() {
		m, err := bastionmain.NewMetrics(a.tele.metrics.Registry())
		if err != nil {
			a.log.Error("the Bastion metrics could not be registered", "error", err)
			return
		}
		a.bastionMx = m
	})
	return a.bastionMx
}

// <<< package 3

// >>> package 2

// bastionRecognize is the Recognize stage (R-BAS-008, R-BAS-009): its refusals are problem details of the
// management API on every path, written with the request's ID and logger. The source of a Bastion token is the
// request as stage 5 resolved it, before the Bastion's forwarded headers count (R-BAS-017).
func (a *App) bastionRecognize(forwarded func(*http.Request) context.Context) func(http.Handler) http.Handler {
	return bastionmain.Recognize(bastionmain.RecognizeOptions{
		Tokens: a.bastionAuthenticator(), Metrics: a.bastionMetrics(), Throttle: a.bastionThrottle(),
		Source: func(r *http.Request) (string, string) {
			src := httpserver.SourceFrom(r.Context())
			return src.ClientAddr, src.UserAgent
		},
		Forwarded: forwarded,
		Problem: func(w http.ResponseWriter, r *http.Request, err error) {
			errs.Write(w, r, err, httpserver.RequestID(r.Context()), httpserver.Logger(r.Context()))
		},
	})
}

// bastionForwarded is the re-derivation of the forwarded headers of a recognized Bastion request (R-BAS-017,
// Q-BAS-116): when the address that reached this installation - stage 5's client address, after the main
// installation's own ingress in server.trusted_proxies - is one of bastions.trusted_proxies, the request is
// resolved again with those addresses trusted as well, so the caller's address the Bastion forwards and the
// Bastion's host count for this request and no other. Nil without bastions.trusted_proxies. An entry that
// server.trusted_proxies covers as well is trusted for every request, Bastion token or not; that is logged.
func (a *App) bastionForwarded(rt *endpointRuntime) (func(*http.Request) context.Context, error) {
	egress := a.cfg.Bastions.TrustedProxyPrefixes()
	if len(egress) == 0 {
		return nil, nil
	}
	trusted := append(slices.Clone(rt.cfg.TrustedProxies), a.cfg.Bastions.TrustedProxies...)
	res, err := httpserver.NewResolver(trusted, rt.cfg.BaseURL, a.cfg.Server.AdditionalBaseURLs,
		httpserver.WithForwardedHeader(httpserver.ForwardedHeader(a.cfg.Server.ForwardedHeader)), httpserver.ForEndpoint(rt.ep))
	if err != nil {
		return nil, fmt.Errorf("bastions.trusted_proxies: %w", err)
	}
	server := (&config.Bastions{TrustedProxies: rt.cfg.TrustedProxies}).TrustedProxyPrefixes()
	for _, e := range egress {
		for _, p := range server {
			if e.Overlaps(p) {
				a.log.Warn("an address of bastions.trusted_proxies is also in server.trusted_proxies: its forwarded headers count for every request, not only for a recognized Bastion",
					"bastion_egress", e.String(), "trusted_proxy", p.String())
			}
		}
	}
	return func(r *http.Request) context.Context {
		ctx := r.Context()
		if !inPrefixes(httpserver.InfoFrom(ctx).Client, egress) {
			return ctx // not a listed Bastion egress: what the request forwards does not count
		}
		info := res.Resolve(r)
		annotate(r, info)
		return httpserver.WithResolved(ctx, info)
	}, nil
}

// inPrefixes reports whether a lies in one of the prefixes.
func inPrefixes(a netip.Addr, prefixes []netip.Prefix) bool {
	if !a.IsValid() {
		return false
	}
	a = a.Unmap()
	for _, p := range prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// LimiterBastionDownloadEvents is the limiter of R-ARCH-034 the download events of a Bastion registration are
// charged to, in units of config.DownloadEventsPerUnit events, the registration as the bucket (R-BAS-016,
// Q-BAS-117).
const LimiterBastionDownloadEvents = "bastion.download_events"

// bastionEventBudget charges a batch of download events to its registration, its events rounded up to whole
// units; nil on a replica without the limiter.
func (a *App) bastionEventBudget() bastionmain.EventBudget {
	lim := part[limitsArea](a.parts).bastionEvents
	if lim == nil {
		return nil
	}
	dl := a.cfg.Bastions.Downloads
	return func(ctx context.Context, bastionID string, events int) (bool, time.Duration) {
		v := lim.TakeNAt(ctx, "bastion:"+bastionID, dl.Units(events), httpserver.InfoFrom(ctx).Received)
		return v.Allowed, v.RetryAfter
	}
}

// bastionThrottle builds the failed-credential throttle of the request's client address for the recognition of
// a Bastion token: the limiter and the exempt addresses of the authentication stage (R-AUTH-038). A replica
// without the limiter (the shell profile) has none.
func (a *App) bastionThrottle() func(*http.Request) bastionmain.Throttle {
	lim := part[limitsArea](a.parts).failures
	if lim == nil {
		return nil
	}
	exempt := a.cfg.Auth.RateLimit.ExemptPrefixes()
	return func(r *http.Request) bastionmain.Throttle {
		// A nil token.Throttle converts to a nil bastionmain.Throttle: no typed nil.
		return auth.NewAddressThrottle(lim, clientAddr(r), exempt, bastionmain.ThrottleScope)
	}
}

// bastionServices are the services of the four interface operations of the default endpoint (R-BAS-010,
// R-BAS-011, R-BAS-015, R-BAS-016); a replica without a database, and every other endpoint, has none.
func (a *App) bastionServices(rt *endpointRuntime) v1.BastionServices {
	in, id := part[infra](a.parts), part[identityArea](a.parts)
	if !rt.cfg.Default || in.db == nil || id.settings == nil {
		return v1.BastionServices{}
	}
	cfg := a.cfg
	rec := a.auditRecorder()
	return v1.BastionServices{
		Config: cfg.Bastions, Release: buildVersion(), BaseURL: cfg.Server.BaseURL,
		Anonymous: func(ctx context.Context) (bool, error) {
			if !rt.cfg.Anonymous {
				return false, nil
			}
			eff, err := id.settings.Effective(ctx)
			return eff.AnonymousAccess, err
		},
		Authenticate: a.credentialStage(rt),
		Shapes:       a.bastionShapes(),
		Feed: bastionmain.NewFeed(bastionmain.FeedOptions{
			DB: in.db, Lookback: cfg.Bastions.Feed.Lookback, MaxPage: cfg.Bastions.Feed.MaxPage, Endpoint: rt.cfg.Name, Log: a.log,
		}),
		Contacts: bastionmain.NewContacts(bastionmain.ContactOptions{DB: in.db, Audit: rec}),
		Downloads: bastionmain.NewIntake(bastionmain.IntakeOptions{
			DB: in.db, Audit: rec, MaxBatch: cfg.Bastions.Downloads.MaxBatch, MaxAge: cfg.Bastions.Downloads.MaxAge, Record: cfg.Audit.RecordDownloads,
			Events: a.bastionEventBudget(),
		}),
	}
}

// <<< package 2
