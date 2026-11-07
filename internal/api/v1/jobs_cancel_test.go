package v1

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// TestR_API_015_TheStarterCancelsThroughASessionOrAnInheritingToken: the job records the principal that started it, not the
// credential, so the starter's way in admits a session and a token that inherits the principal's rights; a token whose
// scope does not inherit them cancels only as a holder of settings:write (Q-API-215).
func TestR_API_015_TheStarterCancelsThroughASessionOrAnInheritingToken(t *testing.T) {
	const starter = "0192f0c4-7a7e-7b51-9b1e-2f0d6c9e1a10"
	settingsWrite := func(held bool) Access {
		return accessFunc(func(_ context.Context, permission string) error {
			if held && permission == "settings:write" {
				return nil
			}
			return errs.E(errs.Forbidden, errs.CodeForbidden, "no")
		})
	}
	cancel := func(p httpserver.Principal, holds bool, createdBy string) error {
		r := httptest.NewRequest("POST", "/api/v1/system/jobs/x/cancel", nil)
		ctx := httpserver.WithPrincipal(r.Context(), p)
		return New(Options{Access: settingsWrite(holds)}).mayCancel(ctx, createdBy)
	}
	for name, p := range owners() {
		if p.Anonymous || p.Kind == httpserver.KindBreakGlass {
			continue
		}
		t.Run("R-API-015 "+name, func(t *testing.T) {
			err := cancel(p, false, starter)
			if refusedByTheGate[name] {
				if e, ok := errs.As(err); !ok || e.Status() != 403 {
					t.Fatalf("the starter's narrowed token: %v, want 403", err)
				}
			} else if err != nil {
				t.Fatalf("the starter: %v", err)
			}
			if err := cancel(p, true, starter); err != nil {
				t.Errorf("with settings:write: %v", err)
			}
			if err := cancel(p, false, "0192f0c4-7a7e-7b51-9b1e-2f0d6c9e1a99"); err == nil {
				t.Error("another principal's job was admitted without settings:write")
			}
		})
	}
}
