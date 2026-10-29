// Package webui serves the embedded Angular UI under /ui/{locale}/ (R-ARCH-004, R-UI-001).
//
// Mount the handler at the pattern "/ui/" of the client listener without stripping the prefix:
//
//	h, err := webui.New(webui.Options{Locales: []string{"en", "de"}, DefaultLocale: "en"})
//	mux.Handle("/ui/", h)
//
// Behavior:
//   - /ui/ and /ui/<route> without a locale redirect (302) to the same route under the preferred
//     locale: Options.Preference, then Accept-Language, then Options.DefaultLocale.
//   - /ui/<locale> redirects to /ui/<locale>/.
//   - /ui/<locale>/<file> serves the file; hashed build output is cached as immutable, other
//     files are revalidated. A missing path whose last segment has an extension is a 404.
//   - every other path under a locale is a client-side route and answers with the locale's
//     index.html (SPA fallback), with a fresh CSP nonce and the document headers of R-UI-001.
package webui

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// NoncePlaceholder is the literal in index.html that is replaced with the per-response nonce. The
// Angular build copies it into the nonce attribute of every script and style tag it writes.
const NoncePlaceholder = "TROVE_CSP_NONCE"

// MountPath is the path prefix the handler serves. Register it as "/ui/".
const MountPath = "/ui/"

// Options configures New.
type Options struct {
	// Locales are the served locale codes (ui.locales), for example ["en", "de"]. Required.
	Locales []string
	// DefaultLocale (ui.default_locale) must be one of Locales. Required.
	DefaultLocale string
	// ImageOrigins are exact https origins added to the CSP img-src
	// (ui.readme.external_image_origins and server.content_base_url). Optional.
	ImageOrigins []string
	// Preference returns the signed-in user's locale preference (R-API-021) or "". Optional;
	// a value outside Locales is ignored.
	Preference func(r *http.Request) string
	// Assets replaces the embedded UI. It is rooted like dist/: <locale>/index.html per locale,
	// or a single index.html as placeholder. Tests use it; production leaves it nil.
	Assets fs.FS
}

// Handler serves the UI. It is safe for concurrent use.
type Handler struct {
	locales     []string
	def         string
	preference  func(*http.Request) string
	cspFormat   string
	bundles     map[string]*bundle
	placeholder bool
}

// bundle is one locale: the index.html template and, for a real build, the asset tree.
type bundle struct {
	index  []byte
	assets fs.FS // nil in placeholder mode
}

var localeCode = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)

// New validates the options and the UI tree and returns the handler.
//
// If no locale directory exists in the tree, New serves the committed placeholder index.html for
// every locale (a binary built without the web build). If some locales are built and a configured
// one is missing, New fails: a half-built UI must not start.
func New(opts Options) (*Handler, error) {
	if len(opts.Locales) == 0 {
		return nil, errors.New("webui: no locales configured")
	}
	seen := map[string]bool{}
	for _, l := range opts.Locales {
		if !localeCode.MatchString(l) {
			return nil, fmt.Errorf("webui: locale %q is not a valid locale code", l)
		}
		if seen[l] {
			return nil, fmt.Errorf("webui: locale %q is listed twice", l)
		}
		seen[l] = true
	}
	if !seen[opts.DefaultLocale] {
		return nil, fmt.Errorf("webui: default locale %q is not in the locale list", opts.DefaultLocale)
	}
	csp, err := buildCSP(opts.ImageOrigins)
	if err != nil {
		return nil, err
	}

	root := opts.Assets
	if root == nil {
		if root, err = embeddedRoot(); err != nil {
			return nil, fmt.Errorf("webui: embedded UI: %w", err)
		}
	}

	h := &Handler{
		locales:    append([]string(nil), opts.Locales...),
		def:        opts.DefaultLocale,
		preference: opts.Preference,
		cspFormat:  csp,
		bundles:    make(map[string]*bundle, len(opts.Locales)),
	}

	var missing []string
	for _, l := range h.locales {
		index, err := fs.ReadFile(root, l+"/index.html")
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				missing = append(missing, l)
				continue
			}
			return nil, fmt.Errorf("webui: read %s/index.html: %w", l, err)
		}
		sub, err := fs.Sub(root, l)
		if err != nil {
			return nil, fmt.Errorf("webui: locale %s: %w", l, err)
		}
		if err := checkIndex(l, index); err != nil {
			return nil, err
		}
		h.bundles[l] = &bundle{index: index, assets: sub}
	}

	switch {
	case len(missing) == len(h.locales):
		index, err := fs.ReadFile(root, "index.html")
		if err != nil {
			return nil, fmt.Errorf("webui: neither locale directories nor a placeholder index.html: %w", err)
		}
		if err := checkIndex("placeholder", index); err != nil {
			return nil, err
		}
		for _, l := range h.locales {
			h.bundles[l] = &bundle{index: index}
		}
		h.placeholder = true
	case len(missing) > 0:
		return nil, fmt.Errorf("webui: built UI lacks configured locale(s) %s", strings.Join(missing, ", "))
	}
	return h, nil
}

func checkIndex(name string, index []byte) error {
	if !bytes.Contains(index, []byte(NoncePlaceholder)) {
		return fmt.Errorf("webui: %s index.html lacks the %s placeholder", name, NoncePlaceholder)
	}
	return nil
}

// Placeholder reports whether the handler serves the placeholder page because the binary holds
// no web build.
func (h *Handler) Placeholder() bool { return h.placeholder }

// Locales returns the served locale codes.
func (h *Handler) Locales() []string { return append([]string(nil), h.locales...) }

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		problem(w, http.StatusMethodNotAllowed, "Method Not Allowed")
		return
	}
	rest, ok := strings.CutPrefix(r.URL.Path, MountPath)
	if !ok {
		problem(w, http.StatusNotFound, "Not Found")
		return
	}

	first, tail, hasTail := strings.Cut(rest, "/")
	if b := h.bundles[first]; b != nil {
		if !hasTail {
			h.redirect(w, r, MountPath+first+"/")
			return
		}
		h.serveLocale(w, r, b, tail)
		return
	}
	h.redirect(w, r, MountPath+h.preferred(r)+"/"+rest)
}

// preferred returns the locale for a request without one in its path.
func (h *Handler) preferred(r *http.Request) string {
	if h.preference != nil {
		if l := h.preference(r); h.bundles[l] != nil {
			return l
		}
	}
	if l := negotiate(r.Header.Get("Accept-Language"), h.locales); l != "" {
		return l
	}
	return h.def
}

func (h *Handler) redirect(w http.ResponseWriter, r *http.Request, target string) {
	u := url.URL{Path: target, RawQuery: r.URL.RawQuery}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Add("Vary", "Accept-Language")
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (h *Handler) serveLocale(w http.ResponseWriter, r *http.Request, b *bundle, tail string) {
	last := tail[strings.LastIndexByte(tail, '/')+1:]
	if tail == "" || tail == "index.html" || !strings.Contains(last, ".") {
		h.serveIndex(w, r, b)
		return
	}
	// A path whose last segment has an extension names a file. It never falls back to the
	// index, so a missing script or stylesheet is a 404 and not an HTML page.
	if b.assets == nil || !fs.ValidPath(tail) {
		problem(w, http.StatusNotFound, "Not Found")
		return
	}
	f, err := b.assets.Open(tail)
	if err != nil {
		problem(w, http.StatusNotFound, "Not Found")
		return
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		problem(w, http.StatusNotFound, "Not Found")
		return
	}
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		data, err := io.ReadAll(f)
		if err != nil {
			problem(w, http.StatusInternalServerError, "Internal Server Error")
			return
		}
		rs = bytes.NewReader(data)
	}

	hd := w.Header()
	hd.Set("Content-Type", contentType(tail))
	hd.Set("X-Content-Type-Options", "nosniff")
	if hashed.MatchString(last) {
		hd.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		hd.Set("Cache-Control", "no-cache")
	}
	http.ServeContent(w, r, last, time.Time{}, rs)
}

func (h *Handler) serveIndex(w http.ResponseWriter, r *http.Request, b *bundle) {
	nonce := rand.Text()
	body := bytes.ReplaceAll(b.index, []byte(NoncePlaceholder), []byte(nonce))

	hd := w.Header()
	hd.Set("Content-Type", "text/html; charset=utf-8")
	hd.Set("Content-Length", strconv.Itoa(len(body)))
	hd.Set("Cache-Control", "no-cache")
	hd.Set("Content-Security-Policy", fmt.Sprintf(h.cspFormat, nonce))
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Referrer-Policy", "same-origin")
	hd.Set("Cross-Origin-Opener-Policy", "same-origin")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

// hashed matches the content-hashed file names of the Angular production build, for example
// main-NBQ6SANY.js or material-icons-LEZCGFVT.woff2.
var hashed = regexp.MustCompile(`-[A-Z0-9]{8}\.[A-Za-z0-9]+$`)

func contentType(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".woff2":
		return "font/woff2"
	case ".woff":
		return "font/woff"
	case ".json", ".map":
		return "application/json"
	case ".ico":
		return "image/x-icon"
	case ".txt":
		return "text/plain; charset=utf-8"
	}
	if t := mime.TypeByExtension(path.Ext(name)); t != "" {
		return t
	}
	return "application/octet-stream"
}

// problem writes a minimal problem+json answer. The edge's error model (R-ARCH-023) is not
// imported on purpose: webui stays free of other internal packages.
func problem(w http.ResponseWriter, status int, title string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"type":"about:blank","title":%q,"status":%d}`+"\n", title, status)
}
