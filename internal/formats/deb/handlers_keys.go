package deb

import (
	"context"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
)

// The public key of a local or virtual repository (R-DEB-018): GET <repo URL>/signing-key.asc. The handler of the remote class, which
// serves the certificates of deb.upstreamKeys (404 deb_no_upstream_keys without them), is the remote class's (T-17 WP6).

func init() {
	registerHandler(routeSigningKey, func(f *Format) formats.HandlerFunc { return f.serveSigningKey })
}

// KeyContentType is the media type of signing-key.asc.
const KeyContentType = "application/pgp-keys"

// serveSigningKey answers one ASCII-armored block holding the primary key and, while one exists, the next key, with the SHA-256 of the
// block as its ETag. A repository without a primary or next key (its keys withheld by a replay) has nothing to serve: 404.
func (f *Format) serveSigningKey(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	if rq.Repo.Class == formats.Remote {
		return notImplemented()
	}
	// Read without package restriction (R-DEB-004): the target names the file and no package, so a grant limited to packages does not
	// reach the key, and a pattern on paths decides on the file's own path.
	if err := rq.Host.Authorize(ctx, rq, formats.Target{Path: SigningKeyFile, Actions: []formats.Action{formats.ActionRead}}); err != nil {
		return err
	}
	st, ok := rq.Host.FormatData(ID).(KeyStore)
	if !ok {
		return errs.E(errs.Internal, errs.CodeInternal, "The Debian data port is not bound.")
	}
	armored, sum, err := st.PublicCertificates(ctx, rq.Repo.NumericID())
	if err != nil {
		return err
	}
	if len(armored) == 0 {
		return errs.E(errs.NotFound, errs.CodeNotFound, "The repository has no signing key to serve.")
	}
	etag := `"` + hex.EncodeToString(sum[:]) + `"`
	h := w.Header()
	h.Set("ETag", etag)
	h.Set("Content-Type", KeyContentType)
	h.Set("Cache-Control", "private, max-age=0, must-revalidate")
	if ifNoneMatch(rq.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return nil
	}
	h.Set("Content-Length", strconv.Itoa(len(armored)))
	w.WriteHeader(http.StatusOK)
	if rq.Method != http.MethodHead {
		_, _ = w.Write(armored)
	}
	return nil
}

// ifNoneMatch reports whether a client's If-None-Match names the ETag (weak comparison) or is "*".
func ifNoneMatch(header, etag string) bool {
	for _, c := range strings.Split(header, ",") {
		c = strings.TrimSpace(c)
		if c == "*" || strings.TrimPrefix(c, "W/") == etag {
			return true
		}
	}
	return false
}
