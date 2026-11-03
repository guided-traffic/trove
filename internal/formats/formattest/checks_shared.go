package formattest

import (
	"bytes"
	"context"
	"net/http"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/formats"
)

// sharedSubjects are callers the Host allows to read everything: a user, a service account and the anonymous
// caller, derived from the subject the fixture's Host acts as (the kit builds no subject of its own). Whatever the
// format renders for one of them it must render, byte for byte, for the others (check 12).
func sharedSubjects(base authz.Subject) []authz.Subject {
	user, bot, anon := base, base, base
	user.Anonymous, user.Kind, user.ID, user.Name, user.Groups = false, "user", "11111111-1111-1111-1111-11111111aaaa", "alice", nil
	bot.Anonymous, bot.Kind, bot.ID, bot.Name, bot.Groups = false, "service_account", "22222222-2222-2222-2222-22222222bbbb", "ci-bot", nil
	anon.Anonymous, anon.Kind, anon.ID, anon.Name, anon.Groups = true, "anonymous", "", "", nil
	return []authz.Subject{user, bot, anon}
}

func subjectName(s authz.Subject) string {
	if s.Anonymous {
		return "the anonymous caller"
	}
	return s.Name
}

// sharedHeaders are the response headers that say what a shared copy is and how it may be reused; they must agree
// between two full readers as the body does.
func sharedHeaders() []string {
	return []string{"ETag", "Content-Type", "Content-Encoding", "Vary", "Cache-Control"}
}

// shared is check 12 (R-BAS-013, Q-BAS-72): the document of a Metadata or Computed route is the same for every
// subject that may read everything. A main installation marks such a response `shared` for a Bastion, which then
// serves one copy to every full reader, so a document that varies by the caller (a user name, a group, whether
// there is a credential) must be rendered by a Volatile route. Every content case that a Metadata or Computed
// route serves is sent once to a Host seeded as the fixture says, as each of three callers; the status, the body
// and the validators must be equal. The seed is made once, by the fixture's own subject, so only the rendering
// is compared and not what a publisher left in the rows.
func (r *run) shared() {
	routes := r.f.Routes()
	if len(routes) == 0 {
		r.failf("%s: the format has no routes and the check is not skipped (Skip[%d] needs a reason)", CheckShared, CheckShared)
		return
	}
	var base authz.Subject
	if r.fx.Host != nil {
		base = r.fx.Host().Subject
	}
	subjects := sharedSubjects(base)
	compared := map[string]bool{}
	for _, c := range r.fx.Content {
		method := c.Method
		if method == "" {
			method = http.MethodGet
		}
		if method != http.MethodGet && method != http.MethodHead {
			continue
		}
		want := c.Status
		if want == 0 {
			want = http.StatusOK
		}
		if want >= http.StatusBadRequest {
			continue // an error has no document to share
		}
		h := r.host("")
		if c.Seed != nil {
			if err := c.Seed(h); err != nil {
				r.failf("content case %q: the seed fails: %v", c.Name, err)
				continue
			}
		}
		got := make([]*Response, len(subjects))
		var rt *formats.Route
		for i, sub := range subjects {
			rq, err := h.NewRequest(context.Background(), method, c.Path, bytes.NewReader(nil))
			if err != nil {
				r.failf("content case %q: %v", c.Name, err)
				break
			}
			rt = rq.Route
			if rt.Kind != formats.Metadata && rt.Kind != formats.Computed {
				break
			}
			rq.Subject = sub
			got[i] = h.ServeRequest(rq, false)
		}
		if rt == nil || (rt.Kind != formats.Metadata && rt.Kind != formats.Computed) || got[len(got)-1] == nil {
			continue
		}
		compared[rt.Name] = true
		first := got[0]
		for i := 1; i < len(got); i++ {
			other := got[i]
			who := subjectName(subjects[0]) + " and " + subjectName(subjects[i])
			switch {
			case first.Code != other.Code:
				r.failf("content case %q (route %q, %s): status %d and %d for %s; a %s route answers every caller that may read everything alike, render it Volatile if it cannot",
					c.Name, rt.Name, rt.Kind, first.Code, other.Code, who, rt.Kind)
			case !bytes.Equal(first.Body.Bytes(), other.Body.Bytes()):
				r.failf("content case %q (route %q, %s): the document differs between %s (%d and %d bytes); "+
					"a %s route must render the same bytes for every caller that may read everything, so make the route Volatile or take the caller out of the document",
					c.Name, rt.Name, rt.Kind, who, first.Body.Len(), other.Body.Len(), rt.Kind)
			default:
				for _, hd := range sharedHeaders() {
					if x, y := first.Result().Header.Values(hd), other.Result().Header.Values(hd); !equalStrings(x, y) {
						r.failf("content case %q (route %q, %s): header %s is %q and %q for %s", c.Name, rt.Name, rt.Kind, hd, x, y, who)
					}
				}
			}
		}
	}
	for _, rt := range routes {
		if rt.Kind != formats.Metadata && rt.Kind != formats.Computed {
			continue
		}
		if _, listed := r.fx.NoContent[rt.Name]; listed {
			continue
		}
		if !compared[rt.Name] {
			r.failf("route %q (%s) has no content case that serves a document, so it was never compared between callers: give it one, or list it in NoContent with the reason", rt.Name, rt.Kind)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
