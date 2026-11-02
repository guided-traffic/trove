package formattest

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest/crafted"
	"github.com/guided-traffic/trove/internal/formats/untrusted"
)

// discovery is the part of check 6 for a remote adapter that follows a discovery document
// (formats.Discoverer, Q-ARCH-268, R-ARCH-025: a kit check for every new behaviour). The engine
// stores the document as a metadata document of the repository and hands it, within its size
// bound, to ParseDiscovery, whose result decides every upstream address the remote contacts. The
// document is as untrusted as an upload (R-ARCH-028), so the kit holds the parser to the bounds of
// check 6 on the kit's crafted documents - JSON nested 100,000 deep, an array of a million values,
// a page of HTML, invalid UTF-8, nothing - and on the fixture's own: each returns within five
// seconds, within the heap budget and without a panic; a crafted document is refused and a valid
// one is read. The document the adapter names is a metadata path of the format, which is how the
// engine stores it.
func (r *run) discovery(d formats.Discoverer, lim untrusted.Limits) {
	p, _, ok := d.DiscoveryDocument()
	if !ok {
		return
	}
	if info := r.f.Classify(p); info.Kind != formats.PathMetadata {
		r.failf("the discovery document %q is classified %s, the engine stores it as a metadata document", p.Path, info.Kind)
	}
	valid := 0
	for _, c := range r.fx.Discovery {
		if c.Valid {
			valid++
		}
	}
	if valid == 0 {
		r.failf("the format's remote adapter follows a discovery document and the fixture has no valid Discovery case")
	}
	rc := formats.RemoteContext{Repo: r.host(formats.Remote).Repo()}
	for _, c := range append(craftedDiscovery(lim), r.fx.Discovery...) {
		m := measure(func(ctx context.Context) error {
			_, err := d.ParseDiscovery(ctx, rc, c.Doc)
			return err
		}, lim)
		switch {
		case m.elapsed > inspectDeadline:
			r.failf("discovery case %q: ParseDiscovery took %v, the bound is %v", c.Name, m.elapsed.Round(time.Millisecond), inspectDeadline)
		case errors.Is(m.err, errPanicked):
			r.failf("discovery case %q: ParseDiscovery %s", c.Name, strings.TrimPrefix(m.err.Error(), "inspect "))
		case c.Valid && m.err != nil:
			r.failf("discovery case %q: ParseDiscovery refused a valid document: %v", c.Name, m.err)
		case !c.Valid && m.err == nil:
			r.failf("discovery case %q: ParseDiscovery accepted a crafted document", c.Name)
		}
		if m.peak > heapBudget {
			r.failf("discovery case %q: ParseDiscovery grew the heap by %d MiB, the bound is %d MiB", c.Name, m.peak>>20, heapBudget>>20)
		}
	}
}

// craftedDiscovery are the kit's crafted discovery documents: no shape a discovery document has,
// each within the size bound the engine applies before ParseDiscovery.
func craftedDiscovery(lim untrusted.Limits) []DiscoveryCase {
	out := []DiscoveryCase{
		{Name: "a page of HTML", Doc: []byte("<!DOCTYPE html><html><body>maintenance</body></html>")},
		{Name: "invalid UTF-8", Doc: []byte("\xff\xfe{\"resources\":[]}")},
		{Name: "an empty document", Doc: []byte{}},
	}
	for _, in := range []crafted.Input{crafted.DeepJSON(100_000), crafted.WideJSON(1_000_000)} {
		if lim.DocumentBytes > 0 && in.Size > lim.DocumentBytes {
			continue // the engine refuses it before the parser sees it
		}
		rc := in.Open()
		b, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			continue
		}
		out = append(out, DiscoveryCase{Name: in.Name, Doc: b})
	}
	return out
}
