//go:build integration

package registry

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/registrytoken"
	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/qname"
)

// Q-OCI-220: the insufficient_scope refusal says what the grant lacks and why an administrator's plain token is refused, and it says
// it the same for a repository that exists, one the caller cannot read and one that does not exist. The ID of the requirement
// (R-OCI-005) stays out of the name because its other criteria are proven elsewhere.
func TestTheRefusalOfATokenWithoutRightsSaysWhyAndNothingAboutTheRepository(t *testing.T) {
	e := newEnv(t, nil)
	plainAdmin := e.st.Token(e.rita, scope.Default())
	message := func(a answer) string {
		t.Helper()
		var b struct {
			Errors []struct {
				Code, Message string
			} `json:"errors"`
		}
		if err := json.Unmarshal(a.Body.Bytes(), &b); err != nil || len(b.Errors) != 1 {
			t.Fatalf("no OCI error body: %v %q", err, a.Body.String())
		}
		return b.Errors[0].Message
	}
	pushWithout := func(troveToken, repo string) answer {
		t.Helper()
		// The grant the token service gives for a push the caller does not hold is empty; the registry token is valid all the same.
		rgt := e.mint("default", "", troveToken, registrytoken.Grant{Entries: []registrytoken.Entry{{Repo: qname.Repo{Project: "web", Key: repo}, Name: "app", Actions: registrytoken.Push}}}, false)
		a := e.do("default", "", "PUT", "/v2/web/"+repo+"/app/manifests/1", rgt)
		if a.Code != http.StatusUnauthorized || a.code(t) != "UNAUTHORIZED" {
			t.Fatalf("%s: %d %s", repo, a.Code, a.Body.String())
		}
		return a
	}
	t.Run("an administrator whose token carries no administrator rights is told so", func(t *testing.T) {
		got := message(pushWithout(plainAdmin, "docker-local"))
		for _, want := range []string{"does not grant repository:web/docker-local/app:push", "no administrator rights", "scope Administrator"} {
			if !strings.Contains(got, want) {
				t.Errorf("the message lacks %q: %s", want, got)
			}
		}
	})
	t.Run("a token with administrator rights gets no administrator advice", func(t *testing.T) {
		rgt := e.mint("default", "", e.admin, registrytoken.Grant{Entries: []registrytoken.Entry{{Repo: local, Name: "app", Actions: registrytoken.Pull}}}, false)
		a := e.do("default", "", "PUT", "/v2/web/docker-local/app/manifests/1", rgt)
		if a.Code != http.StatusUnauthorized || strings.Contains(message(a), "administrator") {
			t.Fatalf("%d %s", a.Code, a.Body.String())
		}
	})
	t.Run("a caller who cannot read the repository gets the answer it gets for one that does not exist", func(t *testing.T) {
		hidden := pushWithout(e.nobody, "team-local")
		missing := pushWithout(e.nobody, "ghost-local")
		if got, want := strings.ReplaceAll(hidden.Body.String(), "team-local", "X"), strings.ReplaceAll(missing.Body.String(), "ghost-local", "X"); got != want {
			t.Fatalf("hidden: %s\nmissing: %s", hidden.Body.String(), missing.Body.String())
		}
		if got, want := strings.ReplaceAll(hidden.Header().Get("WWW-Authenticate"), "team-local", "X"), strings.ReplaceAll(missing.Header().Get("WWW-Authenticate"), "ghost-local", "X"); got != want {
			t.Fatalf("the challenges differ: %q %q", got, want)
		}
	})
}
