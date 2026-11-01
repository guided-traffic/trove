package formathost

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/packages"
)

// lengthItems records the options of the Spool it is asked for.
type lengthItems struct {
	ItemOps
	got items.IngestOptions
}

func (l *lengthItems) Spool(_ context.Context, _ io.Reader, o items.IngestOptions) (*items.Spooled, error) {
	l.got = o
	return nil, errs.E(errs.Internal, errs.CodeInternal, "stop after the options")
}

func TestR_ARCH_014_TheDeclaredLengthOfASpooledBodyIsAnOptionNotTheRequestsLength(t *testing.T) {
	pk, err := packages.NewStore(nopRunner{})
	if err != nil {
		t.Fatal(err)
	}
	li := &lengthItems{}
	h, err := New(Options{Runner: nopRunner{}, Decider: &fakeDecider{}, Items: li, Packages: pk})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name   string
		length int64
		want   int64
	}{
		{"a length the format states", 42, 42},
		{"none stated is unknown", 0, -1},
		{"a negative length is unknown", -1, -1},
	} {
		t.Run(c.name, func(t *testing.T) {
			rq := testRequest(formats.Upload)
			rq.ContentLength = 7777 // what the request says is not what the spool is told
			defer h.Begin(rq)()
			_, _ = h.Ingest().Spool(context.Background(), rq, strings.NewReader("x"), formats.IngestOptions{Length: c.length})
			if li.got.ContentLength != c.want {
				t.Fatalf("the spool is told %d, want %d", li.got.ContentLength, c.want)
			}
		})
	}
}

func TestR_ARCH_011_ARequestDerivedFromABegunRequestSharesItsState(t *testing.T) {
	h := &Host{}
	other := testRequest(formats.Download)
	rq := testRequest(formats.Download)
	finish := h.Begin(rq)
	defer finish()
	if stateOf(other) == stateOf(rq) {
		t.Fatal("two requests share one state")
	}
	c := *rq
	if stateOf(&c) != stateOf(rq) {
		t.Fatal("a copy of a begun request has another state")
	}
	finish() // twice is harmless: the spools are gone after the first
}
