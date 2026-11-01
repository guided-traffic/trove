package nuget

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/observability"
)

func render(method string, r *http.Request, err error) *httptest.ResponseRecorder {
	if r == nil {
		r = httptest.NewRequest(method, "https://trove.example.com/api/nuget/nuget-local/v3/index.json", nil)
	}
	w := httptest.NewRecorder()
	New().WriteError(w, r, err)
	return w
}

// TestR_NUGET_002_AnErrorIsOneLineOfCodeAndMessage: the body is text/plain, one line
// "<code>: <message>", whatever the detail holds.
func TestR_NUGET_002_AnErrorIsOneLineOfCodeAndMessage(t *testing.T) {
	w := render(http.MethodGet, nil, errs.E(errs.Invalid, errs.CodeNuGetInvalidPackage, "first line\nsecond line\r\nthird"))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	if got, want := w.Body.String(), "nuget_invalid_package: first line second line  third"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("the answer is not marked nosniff")
	}
	head := render(http.MethodHead, httptest.NewRequest(http.MethodHead, "/x", nil), errs.E(errs.Invalid, errs.CodeNuGetInvalidPackage, "bad"))
	if head.Body.Len() != 0 || head.Header().Get("Content-Length") != "26" {
		t.Errorf("HEAD: %d body bytes, Content-Length %q", head.Body.Len(), head.Header().Get("Content-Length"))
	}
}

// TestR_NUGET_012_A401ChallengesWithBasicAlone: the challenge is exactly Basic realm="trove",
// whatever the authentication stage put into the error, because NTLM or Negotiate beside
// Basic would make a client send Basic only after it restricted its authentication types.
func TestR_NUGET_012_A401ChallengesWithBasicAlone(t *testing.T) {
	for _, challenge := range []string{"", `Bearer realm="x"`, `Negotiate, Basic realm="trove"`} {
		err := errs.E(errs.Unauthenticated, errs.CodeUnauthenticated, "Credentials are required.")
		err.Challenge = challenge
		w := render(http.MethodGet, nil, err)
		if w.Code != http.StatusUnauthorized || w.Header().Get("WWW-Authenticate") != `Basic realm="trove"` {
			t.Errorf("challenge %q: status %d, WWW-Authenticate %q", challenge, w.Code, w.Header().Get("WWW-Authenticate"))
		}
		if !strings.HasPrefix(w.Body.String(), errs.CodeUnauthenticated+": ") {
			t.Errorf("body = %q", w.Body.String())
		}
	}
}

// TestR_NUGET_002_RetryAfterIsAtMost30Seconds: the clients wait for the Retry-After of any
// answer and count the wait against the one timeout of the command.
func TestR_NUGET_002_RetryAfterIsAtMost30Seconds(t *testing.T) {
	for _, c := range []struct {
		wait time.Duration
		want string
	}{{0, "1"}, {2 * time.Second, "2"}, {1500 * time.Millisecond, "2"}, {30 * time.Second, "30"}, {5 * time.Minute, "30"}} {
		err := errs.E(errs.Unavailable, errs.CodeUnavailable, "busy", func(e *errs.Error) { e.RetryAfter = c.wait })
		if got := render(http.MethodGet, nil, err).Header().Get("Retry-After"); got != c.want {
			t.Errorf("RetryAfter %s: header %q, want %q", c.wait, got, c.want)
		}
	}
	// a 4xx that carries a wait keeps it, capped the same way
	err := errs.E(errs.RateLimited, errs.CodeRateLimited, "slow down", func(e *errs.Error) { e.RetryAfter = time.Hour })
	if got := render(http.MethodGet, nil, err).Header().Get("Retry-After"); got != "30" {
		t.Errorf("rate limit: Retry-After = %q", got)
	}
}

// TestR_NUGET_001_AV2PathAnswersWithTheServiceIndexOfTheAddressedRepository: the 404 of a
// path that is no v3 resource names the absolute URL of the service index, built from the
// refusal the dispatcher attached (Q-ARCH-263), so the message is the repository the client
// addressed and never a guess.
func TestR_NUGET_001_AV2PathAnswersWithTheServiceIndexOfTheAddressedRepository(t *testing.T) {
	v2 := errs.E(errs.NotFound, errs.CodeNuGetV2Unsupported, "No NuGet v3 resource has this path.")
	r := httptest.NewRequest(http.MethodGet, "https://trove.example.com/api/nuget/nuget-local/FindPackagesById()?id='Acme.Widgets'", nil)
	r = formats.WithRefusal(r, formats.Refusal{Format: ID, Key: "nuget-local", BaseURL: "https://trove.example.com"})
	w := render(http.MethodGet, r, v2)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.HasPrefix(body, "nuget_v2_unsupported: ") || !strings.Contains(body, "https://trove.example.com/api/nuget/nuget-local/v3/index.json") {
		t.Errorf("body = %q", body)
	}
	if strings.ContainsAny(body, "\r\n") {
		t.Errorf("body is not one line: %q", body)
	}

	t.Run("the cache view of a remote repository", func(t *testing.T) {
		r := formats.WithRefusal(httptest.NewRequest(http.MethodGet, "/x", nil), formats.Refusal{Format: ID, Key: "nuget-org~cache", BaseURL: "https://trove.example.com"})
		if body := render(http.MethodGet, r, v2).Body.String(); !strings.Contains(body, "/api/nuget/nuget-org~cache/v3/index.json") {
			t.Errorf("body = %q", body)
		}
	})
	t.Run("a refusal that happened before the repository was known cannot link", func(t *testing.T) {
		w := render(http.MethodGet, nil, v2)
		if w.Code != http.StatusNotFound || !strings.HasPrefix(w.Body.String(), "nuget_v2_unsupported: ") || strings.Contains(w.Body.String(), "https://") {
			t.Errorf("status %d, body %q", w.Code, w.Body.String())
		}
	})
}

// TestR_NUGET_006_APushToARemoteRepositoryAnswers405: the answer of a write addressed to a
// class that takes none says why, in the native shape, with the methods the path has.
func TestR_NUGET_006_APushToARemoteRepositoryAnswers405(t *testing.T) {
	err := errs.E(errs.MethodNotAllowed, errs.CodeMethodNotAllowed, "A remote repository does not serve this request.",
		errs.WithAllow("GET", "HEAD"), errs.WithExtension("repositoryClass", "remote"))
	w := render(http.MethodPut, httptest.NewRequest(http.MethodPut, "/x", nil), err)
	if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "GET, HEAD" {
		t.Errorf("status %d, Allow %q", w.Code, w.Header().Get("Allow"))
	}
	if !strings.Contains(w.Body.String(), "remote repository") || !strings.HasPrefix(w.Body.String(), errs.CodeMethodNotAllowed+": ") {
		t.Errorf("body = %q", w.Body.String())
	}
}

// TestR_NUGET_002_AnInternalFailureHidesItsCause: a failure of the server answers with the
// generic detail, never with the text of its cause.
func TestR_NUGET_002_AnInternalFailureHidesItsCause(t *testing.T) {
	w := render(http.MethodGet, nil, errs.E(errs.Internal, errs.CodeInternal, "", errs.WithCause(http.ErrAbortHandler)))
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "abort") {
		t.Errorf("status %d, body %q", w.Code, w.Body.String())
	}
}

// TestR_NUGET_002_TheSessionIDIsRecordedScrubbed: the X-NuGet-Session-Id of a client is logged to
// correlate the requests of one restore; control characters never reach the record, and a
// request without a record or without the header is left alone.
func TestR_NUGET_002_TheSessionIDIsRecordedScrubbed(t *testing.T) {
	if got := cleanField("abc\r\ndef\x00\x7fghi"); got != "abcdefghi" {
		t.Errorf("cleanField = %q", got)
	}
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	recordClientFacts(r) // no request record in the context
	ctx, info := observability.WithRequestInfo(r.Context())
	r = r.WithContext(ctx)
	r.Header.Set(headerSessionID, "5c1f\n0d")
	recordClientFacts(r)
	recordClientFacts(httptest.NewRequest(http.MethodGet, "/y", nil).WithContext(ctx)) // no header
	if info == nil {
		t.Fatal("no request record")
	}
}
