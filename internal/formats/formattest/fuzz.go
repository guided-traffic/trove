package formattest

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/untrusted"
	"github.com/guided-traffic/trove/internal/repository"
)

// FuzzMatch is the fuzz target of a format's Match (R-ARCH-028, R-ARCH-005). The
// seeds are the examples of its routes and the kit's traversal inputs. For any
// method and path it checks that Match does not panic, answers an error of the
// kinds the dispatcher renders, never resolves a path that is no clean repository
// path (traversal, NUL, control characters, overlong paths), and resolves the same
// input to the same route twice. A format calls it from its own test:
//
//	func FuzzR_ARCH_028_Match(f *testing.F) { formattest.FuzzMatch(f, npm.Format{}) }
func FuzzMatch(f *testing.F, format formats.Format) {
	for _, rt := range format.Routes() {
		for _, ex := range rt.Examples {
			f.Add(rt.Method, ex)
			for _, m := range mutate(ex) {
				f.Add(rt.Method, m)
			}
		}
	}
	for _, in := range traversalInputs() {
		f.Add(http.MethodGet, in)
	}
	f.Add(http.MethodGet, "")
	f.Add(http.MethodHead, "a")
	f.Fuzz(func(t *testing.T, method, escaped string) {
		rt, params, err := format.Match(method, escaped)
		_, cerr := repository.CleanPath(escaped)
		if err != nil {
			e, ok := errs.As(err)
			if !ok || (e.Kind != errs.NotFound && e.Kind != errs.MethodNotAllowed && e.Kind != errs.Invalid) {
				t.Fatalf("Match(%q, %q) answers %v, want not_found, method_not_allowed or invalid_path", method, escaped, err)
			}
			if e.Kind == errs.MethodNotAllowed && len(e.Allow) == 0 {
				t.Fatalf("Match(%q, %q): method not allowed without an Allow list", method, escaped)
			}
			return
		}
		if cerr != nil {
			t.Fatalf("Match(%q, %q) resolves to route %q although the path is not clean: %v", method, escaped, rt.Name, cerr)
		}
		want := method
		if want == http.MethodHead {
			want = http.MethodGet
		}
		if rt == nil || rt.Method != want {
			t.Fatalf("Match(%q, %q) = %+v: not a route of the method", method, escaped, rt)
		}
		rt2, params2, err2 := format.Match(method, escaped)
		if err2 != nil || rt2.Name != rt.Name || len(params) != len(params2) {
			t.Fatalf("Match(%q, %q) is not deterministic: %v %v", method, escaped, rt.Name, err2)
		}
		for k, v := range params {
			if params2[k] != v {
				t.Fatalf("Match(%q, %q): parameter %q differs between two calls", method, escaped, k)
			}
		}
	})
}

// fuzzLimits are the limits of the fuzz target: the format's own, tightened so that
// one input costs milliseconds.
func fuzzLimits(d formats.Descriptor) untrusted.Limits {
	l := untrusted.DefaultLimits().Tighten(d.Limits)
	return l.Tighten(untrusted.Limits{
		Entries: 200, UncompressedBytes: 1 << 20, ManifestEntryBytes: 256 << 10, StreamBytes: 8 << 20,
		DocumentBytes: 1 << 20, JSONMembers: 100_000, Deadline: 3 * time.Second,
	})
}

// FuzzInspect is the fuzz target of a format's Inspect. path is the repository
// path an upload is committed at; seeds are valid and near-valid uploads of the
// format. For any body it checks that Inspect does not panic, finishes within its
// deadline, fails only with a client error (never an internal error, never an
// unclassified one), and returns properties below the format's namespace only.
//
//	func FuzzR_ARCH_028_Inspect(f *testing.F) {
//		formattest.FuzzInspect(f, npm.Format{}, "left-pad/-/left-pad-1.0.0.tgz", validTarball)
//	}
func FuzzInspect(f *testing.F, format formats.Format, path string, seeds ...[]byte) {
	for _, s := range seeds {
		f.Add(path, s)
	}
	f.Add(path, []byte{})
	f.Add(path, []byte("{}"))
	f.Add(path, []byte("<a/>"))
	f.Add(path, []byte("PK\x05\x06"+strings.Repeat("\x00", 18)))
	f.Add(path, []byte("\x1f\x8b\x08\x00"))
	f.Fuzz(func(t *testing.T, p string, body []byte) {
		clean, err := repository.CleanPath(p)
		if err != nil {
			t.Skip() // the dispatcher never passes a path that is not clean
		}
		d := format.Descriptor()
		lim := fuzzLimits(d)
		ctx, cancel := lim.Context(context.Background())
		defer cancel()
		start := time.Now()
		ins, err := format.Inspect(ctx, formats.InspectInput{Path: clean, MediaType: "application/octet-stream", Body: Spool(body), Limits: lim})
		if el := time.Since(start); el > lim.Deadline+time.Second {
			t.Fatalf("Inspect took %v, past its deadline of %v", el, lim.Deadline)
		}
		if err != nil {
			e, ok := errs.As(err)
			switch {
			case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled):
				t.Fatalf("Inspect ran into its deadline on %d bytes: %v", len(body), err)
			case !ok:
				t.Fatalf("Inspect failed with an unclassified error: %v", err)
			case e.Kind == errs.Internal:
				t.Fatalf("Inspect failed with an internal error on client input: %v", err)
			}
			return
		}
		if ins == nil {
			return
		}
		for k := range ins.Properties {
			if d.PropertyPrefix == "" || !strings.HasPrefix(k, d.PropertyPrefix) {
				t.Fatalf("Inspect returned the property %q outside the namespace %q", k, d.PropertyPrefix)
			}
		}
	})
}
