package v1

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/avatar"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// fakeAvatars answers for one digest and counts the reads of the image.
type fakeAvatars struct {
	digest string
	opens  int
}

func (f *fakeAvatars) Put(context.Context, avatar.User, avatar.Who, string, []byte) (string, error) {
	return f.digest, nil
}

func (f *fakeAvatars) Remove(context.Context, avatar.User, avatar.Who) (bool, error) {
	return true, nil
}

func (f *fakeAvatars) Has(_ context.Context, d string) error {
	if d != f.digest {
		return avatar.ErrNotFound
	}
	return nil
}

func (f *fakeAvatars) Open(ctx context.Context, d string) ([]byte, error) {
	f.opens++
	if err := f.Has(ctx, d); err != nil {
		return nil, err
	}
	return []byte("\x89PNG-bytes"), nil
}

func TestR_API_055_AConditionalReadAnswers304WithTheImagesCachePolicyAndWithoutReadingTheBlob(t *testing.T) {
	digest := strings.Repeat("ab", 32)
	fake := &fakeAvatars{digest: digest}
	s := New(Options{Avatars: fake})
	get := func(hdr ...string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/avatars/"+digest, nil)
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		w := httptest.NewRecorder()
		s.GetAvatar(w, r, digest, gen.GetAvatarParams{})
		return w
	}

	t.Run("R-API-055 a matching If-None-Match is 304 with the immutable policy, and the blob stays closed", func(t *testing.T) {
		w := get("If-None-Match", `"`+digest+`"`)
		if w.Code != http.StatusNotModified {
			t.Fatalf("status %d", w.Code)
		}
		if got := w.Header().Get("Cache-Control"); got != avatarCache {
			t.Errorf("Cache-Control = %q, want %q", got, avatarCache)
		}
		if w.Header().Get("ETag") != `"`+digest+`"` {
			t.Errorf("ETag = %q", w.Header().Get("ETag"))
		}
		if fake.opens != 0 {
			t.Errorf("the blob was opened %d times for a 304", fake.opens)
		}
	})
	t.Run("R-API-055 a 200 reads the blob once and carries the same policy", func(t *testing.T) {
		fake.opens = 0
		w := get()
		if w.Code != http.StatusOK || fake.opens != 1 || w.Header().Get("Cache-Control") != avatarCache {
			t.Fatalf("status %d, opens %d, Cache-Control %q", w.Code, fake.opens, w.Header().Get("Cache-Control"))
		}
	})
	t.Run("R-API-055 a digest no avatar has is 404 also with a matching tag", func(t *testing.T) {
		other := strings.Repeat("cd", 32)
		r := httptest.NewRequest(http.MethodGet, "/api/v1/avatars/"+other, nil)
		r.Header.Set("If-None-Match", `"`+other+`"`)
		w := httptest.NewRecorder()
		s.GetAvatar(w, r, other, gen.GetAvatarParams{})
		if w.Code != http.StatusNotFound {
			t.Fatalf("status %d", w.Code)
		}
	})
}

func TestR_API_055_ATokenLimitedToPartOfItsOwnersRightsDoesNotChangeTheAvatar(t *testing.T) {
	owner := func(c httpserver.Credential) error {
		r := httptest.NewRequest(http.MethodPut, "/api/v1/users/me/avatar", nil)
		p := httpserver.Principal{Kind: httpserver.KindUser, ID: "u1", Name: "alice", Credential: c}
		_, err := avatarOwner(r.WithContext(httpserver.WithPrincipal(r.Context(), p)))
		return err
	}
	t.Run("R-API-055 a session", func(t *testing.T) {
		if err := owner(httpserver.Credential{Method: httpserver.MethodSession}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("R-API-055 a token that inherits the owner's rights", func(t *testing.T) {
		if err := owner(httpserver.Credential{Method: httpserver.MethodBearer, Scope: &scope.Scope{Inherit: true}}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("R-API-055 a token limited to one capability or to one repository", func(t *testing.T) {
		for _, sc := range []scope.Scope{{System: []string{"settings:read"}}, {Repositories: []scope.Grant{{}}}} {
			if err := owner(httpserver.Credential{Method: httpserver.MethodBearer, Scope: &sc}); err == nil {
				t.Errorf("scope %+v was admitted", sc)
			}
		}
	})
}
