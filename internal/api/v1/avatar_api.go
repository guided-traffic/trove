package v1

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/avatar"
	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// The avatar operations of R-API-055. The image is untrusted input: internal/avatar decides the format by the bytes, bounds
// the size before decoding and stores a new PNG made from the pixels. These handlers only read the bounded body, map the
// refusals to the problems of the API, and set the headers of the image answer.

// AvatarService is the avatar store behind the operations (*avatar.Store).
type AvatarService interface {
	Put(ctx context.Context, u avatar.User, who avatar.Who, declared string, body []byte) (string, error)
	Remove(ctx context.Context, u avatar.User, who avatar.Who) (bool, error)
	Has(ctx context.Context, hexDigest string) error
	Open(ctx context.Context, hexDigest string) ([]byte, error)
}

// avatarPathPrefix is where the image answer lives; a user record names an avatar by it.
const avatarPathPrefix = "/api/v1/avatars/"

// avatarURL is the avatarUrl of a user record: the path of the image, nil for a user without one.
func avatarURL(sha256 string) *string {
	if sha256 == "" {
		return nil
	}
	u := avatarPathPrefix + sha256
	return &u
}

func (s *Server) avatars() (AvatarService, error) {
	if s.o.Avatars == nil {
		return nil, unavailable("The avatar store")
	}
	return s.o.Avatars, nil
}

// avatarOwner answers 403 for a caller that is not a user: a service account or a CI token has no avatar.
func avatarOwner(r *http.Request) (avatar.User, error) {
	p := httpserver.PrincipalFrom(r.Context())
	if p.Kind != httpserver.KindUser && p.Kind != httpserver.KindBreakGlass {
		return avatar.User{}, errs.E(errs.Forbidden, errs.CodeForbidden, "Service accounts and CI tokens have no avatar.")
	}
	// A token whose scope is narrower than its owner's rights (a token made for one repository or one capability) does not
	// change the owner's profile picture; a session and a token that inherits the owner's rights do (Q-API-211).
	if sc := p.Credential.Scope; sc != nil && !sc.Inherit {
		return avatar.User{}, errs.E(errs.Forbidden, errs.CodeForbidden, "A token limited to part of its owner's rights cannot change the avatar; sign in or use a token that inherits the owner's rights.")
	}
	return avatar.User{ID: p.ID, Name: p.Name}, nil
}

// readImage returns the declared media type and the body of an upload. The type is checked before a byte is read, and the body
// is read through a reader that stops one byte above the limit: a larger body, whatever Content-Length says, is refused while
// it is read and never held beyond the limit.
func readImage(w http.ResponseWriter, r *http.Request) (string, []byte, error) {
	declared := r.Header.Get("Content-Type")
	switch mt, _, err := mime.ParseMediaType(declared); {
	case err != nil, mt != "image/png" && mt != "image/jpeg" && mt != "image/webp":
		return "", nil, errs.E(errs.Unsupported, errs.CodeUnsupported, "The request body must be image/png, image/jpeg or image/webp.")
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, avatar.MaxUpload))
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		return "", nil, avatarTooLarge()
	}
	if err != nil {
		return "", nil, errs.E(errs.Invalid, errs.CodeInvalid, "The request body could not be read.", errs.WithCause(err))
	}
	return declared, data, nil
}

func avatarTooLarge() error {
	return errs.E(errs.TooLarge, errs.CodeTooLarge, "The image is larger than "+strconv.Itoa(avatar.MaxUpload)+" bytes.")
}

// avatarErr maps a refusal of internal/avatar to its problem (R-API-055).
func avatarErr(err error) error {
	invalid := func(code, detail string) error {
		return errs.E(errs.Invalid, errs.CodeValidation, "The image cannot be used as an avatar.",
			errs.WithFields(errs.FieldError{Pointer: "", Code: code, Detail: detail}))
	}
	switch {
	case errors.Is(err, avatar.ErrEmpty):
		return errs.E(errs.Invalid, errs.CodeInvalid, "The request body is empty.")
	case errors.Is(err, avatar.ErrTooLarge):
		return avatarTooLarge()
	case errors.Is(err, avatar.ErrUnsupportedType):
		return errs.E(errs.Unsupported, errs.CodeUnsupported, "The content is not a PNG, JPEG or WebP image.")
	case errors.Is(err, avatar.ErrTypeMismatch):
		return errs.E(errs.Unsupported, errs.CodeUnsupported, "The content is not the image format that Content-Type names.")
	case errors.Is(err, avatar.ErrAnimated):
		return invalid("animated_image", "only a still image is accepted")
	case errors.Is(err, avatar.ErrDimensions):
		return invalid("invalid_dimensions", "the image must be at least "+strconv.Itoa(avatar.MinSide)+" x "+strconv.Itoa(avatar.MinSide)+
			" pixels and at most "+strconv.Itoa(avatar.MaxPixels)+" pixels in all; a progressive or four-component JPEG, or a 16-bit PNG, fewer")
	case errors.Is(err, avatar.ErrComplex):
		return invalid("too_complex", "the image has too many scans or takes too long to decode")
	case errors.Is(err, avatar.ErrCorrupt):
		return invalid("undecodable_image", "the image is damaged or truncated")
	case errors.Is(err, avatar.ErrBusy):
		return errs.E(errs.Unavailable, errs.CodeServerBusy, "The replica is processing other avatars; try again.", errs.WithRetryAfter(time.Second))
	case errors.Is(err, blob.ErrCorrupt):
		return errs.E(errs.Unavailable, errs.CodeObjectStoreUnavail, "The stored image failed its check.", errs.WithRetryAfter(5*time.Second), errs.WithCause(err))
	}
	return errs.Wrap(err, "api.avatar")
}

// SetAvatar implements PUT /api/v1/users/me/avatar (R-API-055).
func (s *Server) SetAvatar(w http.ResponseWriter, r *http.Request) {
	if !s.identityReady(w, r) {
		return
	}
	svc, err := s.avatars()
	var owner avatar.User
	if err == nil {
		owner, err = avatarOwner(r)
	}
	var declared string
	var body []byte
	if err == nil {
		declared, body, err = readImage(w, r)
	}
	if err == nil {
		c := s.caller(r)
		_, err = svc.Put(r.Context(), owner, avatar.Who{Actor: c.Actor, Source: c.Source, Self: true}, declared, body)
		err = avatarOrNil(err)
	}
	var u principal.User
	if err == nil {
		u, err = s.o.Auth.Manager.GetUserByID(r.Context(), owner.ID)
	}
	if err != nil {
		failAvatar(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, toUser(u))
}

func avatarOrNil(err error) error {
	if err == nil {
		return nil
	}
	return avatarErr(err)
}

// failAvatar answers a problem that is already mapped, or an identity error of the user lookup.
func failAvatar(w http.ResponseWriter, r *http.Request, err error) {
	if _, ok := errs.As(err); ok {
		httpserver.Problem(w, r, err)
		return
	}
	failIdentity(w, r, err)
}

// RemoveAvatar implements DELETE /api/v1/users/me/avatar (R-API-055).
func (s *Server) RemoveAvatar(w http.ResponseWriter, r *http.Request) {
	svc, err := s.avatars()
	var owner avatar.User
	if err == nil {
		owner, err = avatarOwner(r)
	}
	if err == nil {
		c := s.caller(r)
		_, err = svc.Remove(r.Context(), owner, avatar.Who{Actor: c.Actor, Source: c.Source, Self: true})
		err = avatarOrNil(err)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RemoveUserAvatar implements DELETE /api/v1/users/{username}/avatar (R-API-055): the moderation of an administrator.
func (s *Server) RemoveUserAvatar(w http.ResponseWriter, r *http.Request, username string) {
	if !s.identityReady(w, r) {
		return
	}
	svc, err := s.avatars()
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	u, err := s.o.Auth.Manager.GetUser(r.Context(), username)
	if err != nil {
		failIdentity(w, r, err)
		return
	}
	c := s.caller(r)
	if _, err := svc.Remove(r.Context(), avatar.User{ID: u.ID, Name: u.Name}, avatar.Who{Actor: c.Actor, Source: c.Source}); err != nil {
		httpserver.Problem(w, r, avatarErr(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// avatarCache is how a browser keeps the image: the URL names the content, so it never changes; private, because the answer is
// for signed-in callers.
const avatarCache = "private, max-age=31536000, immutable"

// GetAvatar implements GET /api/v1/avatars/{digest} (R-API-055).
func (s *Server) GetAvatar(w http.ResponseWriter, r *http.Request, digest string, _ gen.GetAvatarParams) {
	svc, err := s.avatars()
	if err == nil {
		err = svc.Has(r.Context(), digest)
	}
	switch {
	case errors.Is(err, avatar.ErrNotFound):
		httpserver.Problem(w, r, errs.E(errs.NotFound, errs.CodeNotFound, "No avatar has this digest."))
		return
	case err != nil:
		httpserver.Problem(w, r, avatarErr(err))
		return
	}
	h := w.Header()
	tag := `"` + strings.ToLower(digest) + `"`
	// The answers of this route keep the cache policy of the image, a 304 included: a 304 that said no-store would replace
	// the stored entry's immutable policy (RFC 9111 section 4.3.4). A 304 does not read the blob.
	if IfNoneMatchHits(r, tag) {
		h.Set("ETag", tag)
		h.Set("Cache-Control", avatarCache)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	img, err := svc.Open(r.Context(), digest)
	if errors.Is(err, avatar.ErrNotFound) {
		httpserver.Problem(w, r, errs.E(errs.NotFound, errs.CodeNotFound, "No avatar has this digest."))
		return
	}
	if err != nil {
		httpserver.Problem(w, r, avatarErr(err))
		return
	}
	// The image answer is not a JSON answer: it replaces the headers of R-API-017 with those of a response the browser
	// renders as a picture and nothing else.
	h.Set("Content-Type", avatar.ContentType)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Content-Disposition", `inline; filename="avatar.png"`)
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("Cache-Control", avatarCache)
	h.Set("ETag", tag)
	h.Set("Content-Length", strconv.Itoa(len(img)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(img) //nolint:gosec // G705: image/png with nosniff and a sandboxing CSP; the bytes are the PNG this server encoded
	}
}
