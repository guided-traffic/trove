package formattest

import (
	"bytes"
	"context"
	"net/http"
	"strings"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/observability"
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

// callerHeaders are the request headers of each caller of check 12, in the order of sharedSubjects: what tells
// callers apart without saying what they ask for - the credential, the client, its session and the hops in
// front of it. Their values differ between the callers. A document that varies by one of them must name it in
// Vary, where a Bastion's shared copy is keyed by it (Q-BAS-86); a header the first answer names in Vary is sent
// alike to every caller.
func callerHeaders() []http.Header {
	return []http.Header{
		{
			"Authorization": {"Basic YWxpY2U6dHJ2X3BhdF9hbGljZQ=="}, "User-Agent": {"npm/11.20.0 node/v24.0.0 linux x64"},
			"Cookie": {"session=alice"}, "Npm-Session": {"0123456789abcdef"}, "Npm-Command": {"install"},
			"X-Nuget-Session-Id": {"11111111-1111-1111-1111-111111111111"}, "X-Forwarded-For": {"203.0.113.1"}, "Referer": {"https://a.example/"},
		},
		{
			"Authorization": {"Bearer trv_sat_ci-bot"}, "User-Agent": {"NuGet Command Line/6.14.0 (Linux)"},
			"Npm-Session": {"fedcba9876543210"}, "Npm-Command": {"ci"},
			"X-Nuget-Session-Id": {"22222222-2222-2222-2222-222222222222"}, "X-Forwarded-For": {"198.51.100.2"},
		},
		{"User-Agent": {"curl/8.11.0"}},
	}
}

// varyNames returns the header names a response names in Vary, canonical, and whether it names "*".
func varyNames(h http.Header) (map[string]bool, bool) {
	out := map[string]bool{}
	for _, line := range h.Values("Vary") {
		for _, n := range strings.Split(line, ",") {
			n = strings.TrimSpace(n)
			if n == "*" {
				return out, true
			}
			if n != "" {
				out[http.CanonicalHeaderKey(n)] = true
			}
		}
	}
	return out, false
}

// headersFor returns the request headers of caller i: its own, except those the first answer names in Vary,
// which every caller sends as the first did (all of them when Vary is "*").
func headersFor(i int, first *Response) http.Header {
	all := callerHeaders()
	if first == nil {
		return all[i].Clone()
	}
	named, star := varyNames(first.Result().Header)
	out := http.Header{}
	for name, v := range all[i] {
		if !star && !named[name] {
			out[name] = append([]string(nil), v...)
		}
	}
	for name, v := range all[0] {
		if star || named[name] {
			out[name] = append([]string(nil), v...)
		}
	}
	return out
}

// callerContext is the request context of a caller: a request-log holder that names the caller, as the chain's
// authentication fills it in production (R-OPS-010), so each caller arrives with a context of its own and not
// only with a subject of its own. A format reads none of it (R-ARCH-008 lets it import neither the HTTP shell nor
// its principal); a Host or contract code that did would be compared too.
func callerContext(sub authz.Subject) context.Context {
	ctx, ri := observability.WithRequestInfo(context.Background())
	if !sub.Anonymous {
		ri.SetPrincipal(sub.Name, string(sub.Kind), sub.ID)
		ri.SetToken("token-of-"+sub.Name, "bearer")
	}
	return ctx
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
// and the validators must be equal. The callers differ in the subject, in the request context and in every
// request header of callerHeaders that the first answer does not name in Vary. The seed is made once, by the
// fixture's own subject, so only the rendering is compared and not what a publisher left in the rows.
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
			rq, err := h.NewRequest(callerContext(sub), method, c.Path, bytes.NewReader(nil))
			if err != nil {
				r.failf("content case %q: %v", c.Name, err)
				break
			}
			rt = rq.Route
			if rt.Kind != formats.Metadata && rt.Kind != formats.Computed {
				break
			}
			rq.Subject = sub
			for name, v := range headersFor(i, got[0]) {
				rq.Header[name] = v
			}
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
