package token

import (
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
)

func i64(v int64) *int64 { return &v }

func TestR_AUTH_026_ResolveLifetime(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	def, maxTTL := 2160*time.Hour, 8760*time.Hour
	at := func(d time.Duration) *time.Time { v := now.Add(d); return &v }
	code := func(err error) string {
		if e, ok := errs.As(err); ok {
			return e.Code
		}
		return ""
	}
	cases := []struct {
		name     string
		typ      Type
		req      TTLRequest
		allowNE  bool
		admin    bool
		derived  bool
		parent   *time.Time
		wantCode string
		wantTTL  time.Duration
		wantNE   bool
	}{
		{name: "neither applies the default", typ: TypePAT, wantTTL: def},
		{name: "ttlSeconds", typ: TypePAT, req: TTLRequest{Seconds: i64(3600)}, wantTTL: time.Hour},
		{name: "expiresAt", typ: TypePAT, req: TTLRequest{ExpiresAt: at(2 * time.Hour)}, wantTTL: 2 * time.Hour},
		{name: "both are invalid_ttl", typ: TypePAT, req: TTLRequest{Seconds: i64(3600), ExpiresAt: at(time.Hour)}, wantCode: "invalid_ttl"},
		{name: "negative is invalid_ttl", typ: TypePAT, req: TTLRequest{Seconds: i64(-5)}, wantCode: "invalid_ttl"},
		{name: "30 is invalid_ttl", typ: TypePAT, req: TTLRequest{Seconds: i64(30)}, wantCode: "invalid_ttl"},
		{name: "60 is the shortest", typ: TypePAT, req: TTLRequest{Seconds: i64(60)}, wantTTL: time.Minute},
		{name: "expiresAt 30 s ahead is invalid_ttl", typ: TypePAT, req: TTLRequest{ExpiresAt: at(30 * time.Second)}, wantCode: "invalid_ttl"},
		{name: "above the maximum", typ: TypePAT, req: TTLRequest{Seconds: i64(40000000)}, wantCode: "ttl_exceeds_maximum"},
		{name: "an expiresAt above the maximum", typ: TypePAT, req: TTLRequest{ExpiresAt: at(maxTTL + time.Hour)}, wantCode: "ttl_exceeds_maximum"},
		{name: "the maximum itself", typ: TypePAT, req: TTLRequest{Seconds: i64(int64(maxTTL / time.Second))}, wantTTL: maxTTL},
		{name: "a PAT is never non-expiring", typ: TypePAT, req: TTLRequest{Seconds: i64(0)}, allowNE: true, admin: true, wantCode: "non_expiring_not_allowed"},
		{name: "a SAT needs the configuration", typ: TypeSAT, req: TTLRequest{Seconds: i64(0)}, admin: true, wantCode: "non_expiring_not_allowed"},
		{name: "a SAT needs an administrator", typ: TypeSAT, req: TTLRequest{Seconds: i64(0)}, allowNE: true, wantCode: "non_expiring_restricted"},
		{name: "a non-expiring SAT", typ: TypeSAT, req: TTLRequest{Seconds: i64(0)}, allowNE: true, admin: true, wantNE: true},
		{name: "never under the derivation rule", typ: TypeSAT, req: TTLRequest{Seconds: i64(0)}, allowNE: true, admin: true, derived: true, wantCode: "non_expiring_not_allowed"},
		{name: "the default is capped at the presenting token's expiry", typ: TypePAT, derived: true, parent: at(24 * time.Hour), wantTTL: 24 * time.Hour},
		{name: "a creation path with its own default", typ: TypePAT, req: TTLRequest{Default: 15 * time.Minute}, wantTTL: 15 * time.Minute},
	}
	for _, c := range cases {
		t.Run("R-AUTH-026 "+c.name, func(t *testing.T) {
			got, err := resolveLifetime(c.typ, c.req, now, def, maxTTL, c.allowNE, c.admin, c.derived, c.parent)
			if c.wantCode != "" {
				if code(err) != c.wantCode {
					t.Fatalf("err = %v, want %s", err, c.wantCode)
				}
				return
			}
			if err != nil || got.nonExpiring != c.wantNE || (!c.wantNE && got.ttl != c.wantTTL) {
				t.Fatalf("got %+v, %v; want ttl %s non-expiring %v", got, err, c.wantTTL, c.wantNE)
			}
		})
	}
}
