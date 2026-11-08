package oci

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
)

// The codes of the distribution specification (R-OCI-008).
const (
	codeNameInvalid         = "NAME_INVALID"
	codeTagInvalid          = "TAG_INVALID"
	codeDigestInvalid       = "DIGEST_INVALID"
	codeNameUnknown         = "NAME_UNKNOWN"
	codeManifestUnknown     = "MANIFEST_UNKNOWN"
	codeBlobUnknown         = "BLOB_UNKNOWN"
	codeBlobUploadUnknown   = "BLOB_UPLOAD_UNKNOWN"
	codeBlobUploadInvalid   = "BLOB_UPLOAD_INVALID"
	codeManifestInvalid     = "MANIFEST_INVALID"
	codeManifestBlobUnknown = "MANIFEST_BLOB_UNKNOWN"
	codeSizeInvalid         = "SIZE_INVALID"
	codeUnauthorized        = "UNAUTHORIZED"
	codeDenied              = "DENIED"
	codeUnsupported         = "UNSUPPORTED"
	codeTooManyRequests     = "TOOMANYREQUESTS"
	codeUnavailable         = "UNAVAILABLE"
	codeUnknown             = "UNKNOWN"
)

// codeOf maps the codes a handler names (internal/errs, CodeOCI…) onto the distribution specification's codes; ok is false for a
// code that is none of them.
func codeOf(code string) (string, bool) {
	switch code {
	case errs.CodeOCINameInvalid:
		return codeNameInvalid, true
	case errs.CodeOCITagInvalid:
		return codeTagInvalid, true
	case errs.CodeOCIDigestInvalid:
		return codeDigestInvalid, true
	case errs.CodeOCINameUnknown:
		return codeNameUnknown, true
	case errs.CodeOCIManifestUnknown:
		return codeManifestUnknown, true
	case errs.CodeOCIBlobUnknown:
		return codeBlobUnknown, true
	case errs.CodeOCIBlobUploadUnknown:
		return codeBlobUploadUnknown, true
	case errs.CodeOCIBlobUploadInvalid:
		return codeBlobUploadInvalid, true
	case errs.CodeOCIManifestInvalid:
		return codeManifestInvalid, true
	case errs.CodeOCIManifestBlobUnknown:
		return codeManifestBlobUnknown, true
	case errs.CodeOCISizeInvalid:
		return codeSizeInvalid, true
	case errs.CodeOCIReferenced:
		return codeDenied, true
	case errs.CodeOCIUnsupported:
		return codeUnsupported, true
	case errs.CodeInvalidPath:
		// A path with an empty or dotted segment names no image: the grammar of R-OCI-002 refuses it.
		return codeNameInvalid, true
	}
	return "", false
}

// registryError maps a core error onto the status and the code of R-OCI-008. A code that a handler named wins; otherwise the
// kind of the error decides. The status of a quota refusal is 413, as the table says, whatever number the kind has.
func registryError(e *errs.Error) (status int, code string) {
	status = e.Status()
	if c, ok := codeOf(e.Code); ok {
		return status, c
	}
	switch e.Kind {
	case errs.Unauthenticated:
		return status, codeUnauthorized
	case errs.Forbidden, errs.Conflict:
		// 403 from the decision point, 409 from a refusal of the registry itself (a push of a withheld digest, a deletion that would
		// leave a dangling reference): both are DENIED (R-OCI-008, R-OCI-013, R-OCI-018).
		return status, codeDenied
	case errs.NotFound:
		return status, codeNameUnknown
	case errs.TooLarge:
		return status, codeSizeInvalid
	case errs.InsufficientStorage:
		return http.StatusRequestEntityTooLarge, codeSizeInvalid
	case errs.RangeNotSatisfiable:
		return status, codeBlobUploadInvalid
	case errs.RateLimited:
		return status, codeTooManyRequests
	case errs.Unavailable:
		// The upstream's 429 reaches the client as 503 with the upstream's wait (R-REPO-029, R-OCI-025).
		if e.Code == errs.CodeUpstreamRateLimited || e.Code == errs.CodeMirrorRateLimited {
			return status, codeTooManyRequests
		}
		return status, codeUnavailable
	case errs.UpstreamFailed, errs.UpstreamTimeout:
		return status, codeUnavailable
	case errs.Internal:
		return status, codeUnknown
	}
	return status, codeUnsupported
}

// WriteError renders an error in the native shape of the distribution specification (R-OCI-008): the status of the error and
// {"errors":[{"code":…,"message":…,"detail":null}]}. The chain's refusals on this format's paths are rendered here too
// (Q-ARCH-214), which is why it does not call back into the chain's problem writer.
func (f *Format) WriteError(w http.ResponseWriter, r *http.Request, err error) {
	p, e := errs.ProblemOf(err, "", "")
	status, code := registryError(e)
	text := p.Detail
	if text == "" {
		text = p.Title
	}
	h := w.Header()
	for k, v := range e.Headers {
		h.Set(k, v)
	}
	h.Set("Content-Type", "application/json")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Docker-Distribution-API-Version", "registry/2.0")
	switch e.Kind {
	case errs.Unauthenticated:
		// The challenge of R-OCI-004 is the stage's: the error carries it.
		if e.Challenge != "" {
			h.Set("WWW-Authenticate", e.Challenge)
		}
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
	body, _ := json.Marshal(struct {
		Errors []registryErrorBody `json:"errors"`
	}{[]registryErrorBody{{Code: code, Message: oneLine(text)}}})
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
	if status >= 500 && e.Kind != errs.NotImplemented {
		attrs := []any{"status", status, "code", p.Code, "kind", e.Kind.String()}
		if e.Op != "" {
			attrs = append(attrs, "op", e.Op)
		}
		if e.Err != nil {
			attrs = append(attrs, "cause", e.Err.Error())
		}
		slog.Error("oci request failed", attrs...)
	}
}

// registryErrorBody is one entry of the error list. detail is always null: the message carries what a person needs.
type registryErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  any    `json:"detail"`
}

// oneLine keeps a message to one line.
func oneLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' {
			return ' '
		}
		return r
	}, s)
}

func retryAfterSeconds(d time.Duration) string {
	s := int64((d + time.Second - 1) / time.Second)
	if s < 1 {
		s = 1
	}
	return strconv.FormatInt(s, 10)
}

// notImplemented is the answer of an operation this build does not serve yet: 501 UNSUPPORTED, so a client reads a failure of
// the server and not a missing image.
func notImplemented(what string) error {
	return errs.E(errs.NotImplemented, errs.CodeNotImplemented, "The registry operation "+what+" is not served by this build of trove yet.")
}
