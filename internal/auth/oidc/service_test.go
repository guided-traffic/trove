package oidc

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/session"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
)

// The domain calls the browser routes of internal/api/login make: plain inputs,
// plain outputs, errors that are problems.

type eventSpy struct{ events []audit.Event }

func (s *eventSpy) RecordTx(_ context.Context, _ meta.Tx, ev audit.Event) error {
	s.events = append(s.events, ev)
	return nil
}
func (s *eventSpy) Record(ctx context.Context, ev audit.Event) { _ = s.RecordTx(ctx, nil, ev) }

type refuseAll struct{ retry time.Duration }

func (r refuseAll) Allow(context.Context, string) (bool, time.Duration) { return false, r.retry }

func TestR_AUTH_013_Logout(t *testing.T) {
	spy := &eventSpy{}
	p := testProvider(t, nil)
	p.doc.EndSessionEndpoint = "https://idp.test/logout?tenant=a"
	s := &Service{o: Options{Audit: spy, BaseURL: "https://trove.test/"}, byID: map[string]*Provider{"corp": p}}
	src := audit.Source{ClientAddr: "192.0.2.7", UserAgent: "test"}

	t.Run("R-AUTH-013 a cookie of an ended or unknown session leads to the logged-out page and records nothing", func(t *testing.T) {
		out := s.Logout(context.Background(), LogoutInput{Source: src})
		if out.RedirectTo != LoggedOutPath || len(spy.events) != 0 {
			t.Fatalf("redirect %q, %d events", out.RedirectTo, len(spy.events))
		}
	})

	t.Run("R-AUTH-013 a session that was ended leads to the logged-out page and records auth.logout", func(t *testing.T) {
		spy.events = nil
		out := s.Logout(context.Background(), LogoutInput{Session: session.LoggedOut{Ended: true, ID: "s-1"}, Source: src})
		if out.RedirectTo != LoggedOutPath || len(spy.events) != 1 {
			t.Fatalf("redirect %q, %d events", out.RedirectTo, len(spy.events))
		}
		ev := spy.events[0]
		var d map[string]any
		_ = json.Unmarshal(ev.Details, &d)
		if ev.Type != audit.EventAuthLogout || ev.Source != src || d["session_id"] != "s-1" || d["rp_initiated"] != false {
			t.Fatalf("event %+v details %v", ev, d)
		}
	})

	t.Run("R-AUTH-013 a session of a provider that advertises an end-session endpoint leads to it with the hint, the client and the return address", func(t *testing.T) {
		spy.events = nil
		out := s.Logout(context.Background(), LogoutInput{Session: session.LoggedOut{Ended: true, ID: "s-2", ProviderID: "corp", IDToken: "tok"}, Source: src})
		u, err := url.Parse(out.RedirectTo)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		if u.Host != "idp.test" || q.Get("tenant") != "a" || q.Get("id_token_hint") != "tok" || q.Get("client_id") != "trove" ||
			q.Get("post_logout_redirect_uri") != "https://trove.test"+LoggedOutPath {
			t.Fatalf("redirect %s", out.RedirectTo)
		}
		var d map[string]any
		_ = json.Unmarshal(spy.events[0].Details, &d)
		if d["rp_initiated"] != true {
			t.Fatalf("details %v", d)
		}
	})
}

func TestR_AUTH_014_AdmitLocalLogin(t *testing.T) {
	t.Run("R-AUTH-014 without a break-glass account the local login is disabled", func(t *testing.T) {
		err := (&Service{}).AdmitLocalLogin(context.Background(), "192.0.2.1")
		if e, ok := errs.As(err); !ok || e.Code != errs.CodeLocalLoginDisabled || e.Kind != errs.Forbidden {
			t.Fatalf("%v", err)
		}
	})
	t.Run("R-AUTH-038 R-AUTH-014 the flow-start limit comes before the account is looked at", func(t *testing.T) {
		s := &Service{o: Options{Limiter: refuseAll{retry: 7 * time.Second}}}
		err := s.AdmitLocalLogin(context.Background(), "192.0.2.1")
		e, ok := errs.As(err)
		if !ok || e.Code != errs.CodeRateLimited || e.Kind != errs.RateLimited || e.RetryAfter != 7*time.Second {
			t.Fatalf("%v", err)
		}
	})
}

func TestR_AUTH_005_StartFlow(t *testing.T) {
	t.Run("R-AUTH-005 an unknown provider is a 404 and the limiter is not asked", func(t *testing.T) {
		s := &Service{o: Options{Limiter: refuseAll{}}}
		_, err := s.StartFlow(context.Background(), StartInput{ProviderID: "nowhere"})
		if e, ok := errs.As(err); !ok || e.Kind != errs.NotFound {
			t.Fatalf("%v", err)
		}
	})
	t.Run("R-AUTH-038 R-AUTH-005 a start over the flow-start limit is a 429 with the retry time", func(t *testing.T) {
		p := testProvider(t, nil)
		s := &Service{o: Options{Limiter: refuseAll{retry: 3 * time.Second}}, byID: map[string]*Provider{"corp": p}}
		_, err := s.StartFlow(context.Background(), StartInput{ProviderID: "corp", ClientAddr: "192.0.2.1"})
		if e, ok := errs.As(err); !ok || e.Kind != errs.RateLimited || e.RetryAfter != 3*time.Second {
			t.Fatalf("%v", err)
		}
	})
	t.Run("R-AUTH-005 a provider whose discovery has not succeeded is a 503 with Retry-After", func(t *testing.T) {
		p := testProvider(t, nil)
		p.doc = nil
		s := &Service{byID: map[string]*Provider{"corp": p}}
		_, err := s.StartFlow(context.Background(), StartInput{ProviderID: "corp"})
		if e, ok := errs.As(err); !ok || e.Code != errs.CodeProviderUnavailable || e.RetryAfter != 30*time.Second {
			t.Fatalf("%v", err)
		}
	})
}

func TestR_AUTH_006_Callback(t *testing.T) {
	t.Run("R-AUTH-006 an unknown provider is a 404 problem, not a login failure", func(t *testing.T) {
		_, err := (&Service{}).Callback(context.Background(), CallbackInput{ProviderID: "nowhere"})
		if e, ok := errs.As(err); !ok || e.Kind != errs.NotFound {
			t.Fatalf("%v", err)
		}
		if _, isFailure := err.(*LoginFailure); isFailure { //nolint:errorlint // the concrete type is the point
			t.Fatal("an unknown provider is a login failure")
		}
	})
}
