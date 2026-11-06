package nuget

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/observability"
)

// headerSessionID is the request header a NuGet client numbers the requests of one command
// with. It is logged so the requests of one restore can be correlated (R-NUGET-002) and
// never used for a decision; X-NuGet-Protocol-Version and X-NuGet-Client-Version are
// ignored.
const headerSessionID = "X-NuGet-Session-Id"

// recordClientFacts adds the access-log fields of R-NUGET-002 to the request record. The
// value comes from the client; the record clips and scrubs it.
func recordClientFacts(r *http.Request) {
	info := observability.RequestInfoFrom(r.Context())
	if info == nil {
		return
	}
	if v := cleanField(r.Header.Get(headerSessionID)); v != "" {
		info.SetField("nuget-session-id", v)
	}
}

// cleanField drops the control characters of a header value.
func cleanField(v string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, v)
}

// challenge is the WWW-Authenticate value of every 401 on a NuGet endpoint, exactly
// (R-NUGET-012): NuGet clients answer Basic, and offering NTLM or Negotiate besides it would
// make a client send Basic only after it restricted its valid authentication types.
const challenge = `Basic realm="trove"`

// maxRetryAfter bounds the Retry-After of a NuGet protocol endpoint whatever value the owning
// page computes (R-NUGET-002): a client counts the wait against the one timeout of its
// command, and a longer value would spend a push's budget on waiting.
const maxRetryAfter = 30 * time.Second

// WriteError renders an error in the native shape of NuGet (R-NUGET-002): the status of the
// error, Content-Type text/plain and one line, "<code>: <message>". The NuGet clients print
// status and reason phrase and no body; the line serves people with curl, and the logs. The
// chain's refusals on this format's paths are rendered here too (Q-ARCH-214), which is why it
// must not call back into the chain's problem writer.
func (f *Format) WriteError(w http.ResponseWriter, r *http.Request, err error) {
	recordClientFacts(r)
	p, e := errs.ProblemOf(err, "", "")
	text := p.Detail
	if text == "" {
		text = p.Title
	}
	switch {
	case p.Code == errs.CodeNuGetV2Unsupported:
		text = v2Message(r)
	case e.Kind == errs.MethodNotAllowed && e.Extensions["repositoryClass"] == "remote" && r.Method != http.MethodGet && r.Method != http.MethodHead:
		text = "This is a remote repository: it caches an upstream feed and takes no pushes. Push to a local repository, or to a virtual repository that has one as its deployment target."
	}
	h := w.Header()
	for k, v := range e.Headers {
		h.Set(k, v)
	}
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("X-Content-Type-Options", "nosniff")
	switch e.Kind {
	case errs.Unauthenticated:
		// The stage's own challenge (errs.Error.Challenge: Bearer for a browser-shaped request) is
		// not used: a NuGet endpoint offers Basic alone, whoever refused (R-NUGET-012).
		h.Set("WWW-Authenticate", challenge)
	case errs.MethodNotAllowed:
		if len(e.Allow) > 0 {
			h.Set("Allow", strings.Join(e.Allow, ", "))
		}
	case errs.RateLimited, errs.Unavailable:
		h.Set("Retry-After", retryAfterSeconds(e.RetryAfter))
	default:
		if e.RetryAfter > 0 {
			h.Set("Retry-After", retryAfterSeconds(e.RetryAfter))
		}
	}
	body := oneLine(p.Code + ": " + text)
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(p.Status)
	if r.Method != http.MethodHead {
		_, _ = w.Write([]byte(body))
	}
	// A refusal of an operation the format does not serve is the client's, however it is numbered.
	if p.Status >= 500 && e.Kind != errs.NotImplemented {
		attrs := []any{"status", p.Status, "code", p.Code, "kind", e.Kind.String()}
		if e.Op != "" {
			attrs = append(attrs, "op", e.Op)
		}
		if e.Err != nil {
			attrs = append(attrs, "cause", e.Err.Error())
		}
		slog.Error("nuget request failed", attrs...)
	}
}

// v2Message is the text of the 404 of a path that is no v3 resource: it names the absolute
// URL of the service index of the repository the client addressed, the one a client uses as
// its source (R-NUGET-001). The URL comes from the refusal the dispatcher attached
// (Q-ARCH-263); a refusal that happened before the repository was known cannot link.
func v2Message(r *http.Request) string {
	const text = "NuGet v2 is not supported; this repository serves the NuGet v3 protocol"
	ref, ok := formats.RefusalFrom(r)
	if !ok {
		return text + " at the service index of the repository (<base>/api/nuget/<projectKey>/<repoKey>/v3/index.json)."
	}
	return text + ". Use its service index as the package source: " + ref.Registry() + FeedSegment + "/" + indexFile
}

// oneLine keeps a body to one line: a detail that carries a line break would end the
// "<code>: <message>" form.
func oneLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' {
			return ' '
		}
		return r
	}, s)
}

func retryAfterSeconds(d time.Duration) string {
	d = min(d, maxRetryAfter)
	s := int64((d + time.Second - 1) / time.Second)
	if s < 1 {
		s = 1
	}
	return strconv.FormatInt(s, 10)
}

// notImplemented is the answer of a v3 operation this build does not serve yet: 501, so a
// client reads a failure of the server and not a missing package.
func notImplemented(what string) error {
	return errs.E(errs.NotImplemented, errs.CodeNuGetNotImplemented, "The NuGet operation "+what+" is not served by this build of trove yet.")
}
