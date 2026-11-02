package formattest_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/internal/formats/formattest/testformat"
)

// discovering gives a remote adapter the discovery document of formats.Discoverer.
type discovering struct {
	formats.RemoteAdapter
	path  string
	parse func(doc []byte) (any, error)
}

func (d discovering) DiscoveryDocument() (formats.Path, string, bool) {
	return formats.Path{Path: d.path}, "doc", true
}

func (d discovering) ParseDiscovery(_ context.Context, _ formats.RemoteContext, doc []byte) (any, error) {
	return d.parse(doc)
}

// TestR_ARCH_025_TheDiscoveryParserMeetsTheBoundsOfCheck6 (R-ARCH-025): a remote adapter that follows a
// discovery document runs the kit's crafted documents and the fixture's through ParseDiscovery
// under the bounds of check 6; a crafted document must be refused, a valid one read, and the
// document is a metadata path of the format. Before, the kit had no check of the behaviour, so a
// parser that read anything, or panicked, passed.
func TestR_ARCH_025_TheDiscoveryParserMeetsTheBoundsOfCheck6(t *testing.T) {
	valid := []byte(`{"resources":[{"@id":"https://h.test/c/","@type":"Content"}]}`)
	fx := testformat.Fixture()
	fx.Inspect = fx.Inspect[:1]
	fx.Discovery = []formattest.DiscoveryCase{
		{Name: "valid", Doc: valid, Valid: true},
		{Name: "an address with user info", Doc: []byte(`{"resources":[{"@id":"https://u:p@h.test/c/","@type":"Content"}]}`)},
	}
	strict := func(doc []byte) (any, error) {
		if bytes.Equal(doc, valid) {
			return struct{}{}, nil
		}
		return nil, errors.New("no service index")
	}
	withDiscovery := func(path string, parse func([]byte) (any, error)) formats.Format {
		return override{Format: testformat.New(), remote: discovering{RemoteAdapter: testformat.New().Remote(), path: path, parse: parse}}
	}
	metadataPath := "pkg"
	if testformat.New().Classify(formats.Path{Path: metadataPath}).Kind != formats.PathMetadata {
		t.Fatalf("the test format does not classify %q as metadata", metadataPath)
	}
	t.Run("R-ARCH-028 a parser that reads its own documents and refuses the rest passes", func(t *testing.T) {
		if got := messages(formattest.Evaluate(withDiscovery(metadataPath, strict), fx), formattest.CheckInspect); got != "" {
			t.Fatalf("got %q", got)
		}
	})
	for name, tc := range map[string]struct {
		path  string
		parse func([]byte) (any, error)
		fx    func(formattest.Fixture) formattest.Fixture
		want  string
	}{
		"a parser that accepts anything": {
			path: metadataPath, parse: func([]byte) (any, error) { return struct{}{}, nil },
			want: `discovery case "JSON nested 100000 deep": ParseDiscovery accepted a crafted document`,
		},
		"a parser that panics": {
			path: metadataPath, parse: func([]byte) (any, error) { panic("boom") },
			want: "ParseDiscovery panicked: boom",
		},
		"a parser that refuses its own document": {
			path: metadataPath, parse: func([]byte) (any, error) { return nil, errors.New("no") },
			want: `discovery case "valid": ParseDiscovery refused a valid document`,
		},
		"a fixture without a valid document": {
			path: metadataPath, parse: strict,
			fx:   func(f formattest.Fixture) formattest.Fixture { f.Discovery = f.Discovery[1:]; return f },
			want: "the fixture has no valid Discovery case",
		},
		"a discovery document that is no metadata path": {
			path: "pkg/-/pkg-1.0.0.tgz", parse: strict,
			want: "is classified content, the engine stores it as a metadata document",
		},
	} {
		t.Run("R-ARCH-028 "+name+" fails check 6", func(t *testing.T) {
			f := fx
			if tc.fx != nil {
				f = tc.fx(fx)
			}
			if got := messages(formattest.Evaluate(withDiscovery(tc.path, tc.parse), f), formattest.CheckInspect); !strings.Contains(got, tc.want) {
				t.Fatalf("got %q, want it to contain %q", got, tc.want)
			}
		})
	}
	t.Run("R-ARCH-010 check 6 cannot be skipped for a format without uploads that follows a discovery document", func(t *testing.T) {
		f := formattest.Fixture{Skip: map[formattest.Check]string{formattest.CheckInspect: "no uploads"}}
		b := bareRemote{remote: discovering{RemoteAdapter: testformat.New().Remote(), path: metadataPath, parse: strict}}
		got := messages(formattest.Evaluate(b, f), formattest.CheckInspect)
		if !strings.Contains(got, "reads what an upstream sends") {
			t.Fatalf("got %q", got)
		}
	})
}

// bareRemote is bare with a remote adapter: no routes, so no upload route.
type bareRemote struct {
	bare
	remote formats.RemoteAdapter
}

func (b bareRemote) Remote() formats.RemoteAdapter { return b.remote }
