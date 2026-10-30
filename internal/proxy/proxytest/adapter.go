// Package proxytest holds the test doubles of the remote engine: a format-free
// counting adapter and a counting upstream that serves it over TLS. Nothing here
// knows a package format; the tests of internal/proxy and the integration tests of
// the engine use it, so the engine is tested on its own and not through npm.
//
// The adapter's paths: `pkg/<name>` is the metadata document of a package and
// `pkg/<name>/-/<file>` one of its artifacts.
package proxytest

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/proxy"
)

// Doc is the canonical path of the document of a package.
func Doc(name string) string { return "pkg/" + name }

// File is the canonical path of an artifact of a package.
func File(name, file string) string { return "pkg/" + name + "/-/" + file }

// Package returns the package a path belongs to, "" for none.
func Package(path string) string {
	rest, ok := strings.CutPrefix(path, "pkg/")
	if !ok {
		return ""
	}
	name, _, _ := strings.Cut(rest, "/")
	return name
}

// Adapter is a proxy.Adapter that counts its calls. The zero value is ready.
//
// A document body may carry URL-map lines, `map <path> <upstream url>`, which
// PrepareFill returns as mappings, so a test can show the own-package rule of
// R-REPO-043.
type Adapter struct {
	// Removal is TracksVersionRemoval: the format's rule replaces the generic
	// gone-package rule.
	Removal bool
	// Keep makes OnFill keep the stored document.
	Keep bool
	// Reject, when set, is AcceptResponse's refusal.
	Reject func(path string, resp *http.Response) error
	// Prepare, when set, validates the document in PrepareFill.
	Prepare func(path string, body []byte) error
	// Expected declares media types expected for a path, as "<path> <media type>"
	// (proxy.ExpectedTypes).
	Expected map[string]bool

	requests, accepts, prepares, fills atomic.Int64

	mu     sync.Mutex
	events []proxy.FillEvent
}

var (
	_ proxy.Adapter       = (*Adapter)(nil)
	_ proxy.ExpectedTypes = (*Adapter)(nil)
)

// UpstreamRequest is a GET of the path below the upstream URL.
func (a *Adapter) UpstreamRequest(ctx context.Context, b proxy.UpstreamBase, path, _ string) (*http.Request, error) {
	a.requests.Add(1)
	u, err := b.Path(path)
	if err != nil {
		return nil, err
	}
	return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
}

// AcceptResponse refuses what Reject refuses.
func (a *Adapter) AcceptResponse(path string, resp *http.Response) error {
	a.accepts.Add(1)
	if a.Reject != nil {
		return a.Reject(path, resp)
	}
	return nil
}

// PrepareFill reads the URL-map lines of the body.
func (a *Adapter) PrepareFill(_ context.Context, path, _ string, body io.ReaderAt, size int64) (proxy.Prepared, error) {
	a.prepares.Add(1)
	b := make([]byte, size)
	if _, err := body.ReadAt(b, 0); err != nil && err != io.EOF {
		return proxy.Prepared{}, err
	}
	if a.Prepare != nil {
		if err := a.Prepare(path, b); err != nil {
			return proxy.Prepared{}, errs.E(errs.UpstreamFailed, errs.CodeUpstreamError, "the document is invalid", errs.WithCause(err))
		}
	}
	var p proxy.Prepared
	for _, line := range bytes.Split(b, []byte("\n")) {
		f := strings.Fields(string(line))
		if len(f) == 3 && f[0] == "map" {
			p.Mappings = append(p.Mappings, proxy.URLMapping{Path: f[1], Upstream: f[2]})
		}
	}
	return p, nil
}

// OnFill records the event.
func (a *Adapter) OnFill(_ context.Context, _ meta.Tx, f proxy.FillEvent) (proxy.FillDecision, error) {
	a.fills.Add(1)
	a.mu.Lock()
	a.events = append(a.events, f)
	a.mu.Unlock()
	if a.Keep && f.Replaces {
		return proxy.FillKeepStored, nil
	}
	return proxy.FillByFetchTime, nil
}

// TracksVersionRemoval returns Removal.
func (a *Adapter) TracksVersionRemoval() bool { return a.Removal }

// PackageDocumentPath is pkg/<name>.
func (a *Adapter) PackageDocumentPath(name string) (string, bool) { return Doc(name), name != "" }

// OwnPath is true for the package's document and its artifacts.
func (a *Adapter) OwnPath(pkg, path string) bool { return pkg != "" && Package(path) == pkg }

// ExpectsType declares the media types of Expected.
func (a *Adapter) ExpectsType(path, mediaType string) bool { return a.Expected[path+" "+mediaType] }

// Requests is the number of upstream requests the adapter built.
func (a *Adapter) Requests() int { return int(a.requests.Load()) }

// Accepts is the number of responses AcceptResponse saw.
func (a *Adapter) Accepts() int { return int(a.accepts.Load()) }

// Prepares is the number of documents PrepareFill validated.
func (a *Adapter) Prepares() int { return int(a.prepares.Load()) }

// Fills is the number of OnFill calls.
func (a *Adapter) Fills() int { return int(a.fills.Load()) }

// FillEvents returns the events OnFill saw.
func (a *Adapter) FillEvents() []proxy.FillEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]proxy.FillEvent(nil), a.events...)
}
