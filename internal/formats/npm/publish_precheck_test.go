package npm

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
)

// readCounter counts the bytes a publish read of its body.
type readCounter struct {
	r io.Reader
	n int
}

func (c *readCounter) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

// S-4, R-AUTHZ-018: the ownership rule is asked, read-only, before the body is read, so that a
// caller who could not commit costs no spool and no validation.
func TestR_AUTHZ_018_PublishAsksOwnershipBeforeTheBodyIsRead(t *testing.T) {
	t.Run("a refusal answers 403 and reads no byte of the body", func(t *testing.T) {
		rf := newRepo(t, func(o *formattest.HostOptions) {
			o.Precheck = func(formats.Target, formats.OwnershipWrite) error {
				return errs.E(errs.Forbidden, errs.CodePackageOwnedByOtherOwner, "owned by someone else")
			}
		})
		body := &readCounter{r: strings.NewReader(string(pubBody(t, "p", "1.0.0", "latest", nil)))}
		rq, err := rf.h.NewRequest(context.Background(), http.MethodPut, "p", body)
		if err != nil {
			t.Fatal(err)
		}
		rq.Subject = rf.opts.Subject
		r := rf.h.ServeRequest(rq, false)
		if r.Code != http.StatusForbidden || errorBodyOf(t, r.ResponseRecorder).Code != errs.CodePackageOwnedByOtherOwner {
			t.Fatalf("%d %s", r.Code, r.Body.String())
		}
		if body.n != 0 {
			t.Fatalf("the body was read: %d bytes", body.n)
		}
		if len(rf.h.Claims()) != 0 {
			t.Fatalf("the commit was reached: %+v", rf.h.Claims())
		}
	})

	t.Run("the question names the package and whether the name has a version", func(t *testing.T) {
		rf := newRepo(t)
		rf.publish("p", "1.0.0")
		rf.publish("p", "1.1.0")
		checks := rf.h.OwnershipChecks()
		if len(checks) != 2 {
			t.Fatalf("checks = %+v", checks)
		}
		for i, wantFirst := range []bool{true, false} {
			c := checks[i]
			if c.Target.Package != "p" || len(c.Target.Actions) != 1 || c.Target.Actions[0] != formats.ActionDeploy || c.Write.FirstVersion != wantFirst {
				t.Errorf("check %d = %+v", i, c)
			}
			// The early answer is never a claim: only the commit knows whether a claim has an owner.
			if c.Write.Publish {
				t.Errorf("check %d asks as a claim", i)
			}
		}
	})
}
