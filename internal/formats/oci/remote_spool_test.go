package oci

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
)

// The digest reader of a tag gives its verdict when the body ends, so it holds under the real spool, on each of its three paths: a body
// of a known length that fits in memory, one of a known length that goes to a file, and one of no known length (a chunked answer). A
// change of the spool that stops reading before the end of the body fails this test (Q-OCI-120).
//
// Mechanism of R-OCI-020, not a proof of its criteria: it checks digest verification or an ambiguity rule of the parser; the
// criteria are the crafted inputs refused before any blob is looked up and the memory bound.
func TestTheSpoolReadsATagToItsEndSoTheDigestVerdictIsGiven(t *testing.T) {
	body := string(imageManifestBytes(sha([]byte("cfg")), sha([]byte("layer"))))
	for name, c := range map[string]struct {
		memory int64
		known  bool
	}{
		"known length, in memory": {memory: 1 << 20, known: true},
		"known length, in a file": {memory: 0, known: true},
		"unknown length":          {memory: 1 << 20, known: false},
	} {
		t.Run(name, func(t *testing.T) {
			sp, err := blob.NewSpool(config.StorageSpool{Dir: t.TempDir(), MaxBytes: 64 << 20, MemoryThreshold: config.ByteSize(c.memory)})
			if err != nil {
				t.Fatal(err)
			}
			ingest := func(announce string) error {
				length := int64(-1)
				if c.known {
					length = int64(len(body))
				}
				resp := &http.Response{
					StatusCode: 200, ContentLength: length,
					Header: http.Header{"Docker-Content-Digest": {announce}, "Content-Type": {ctImage}},
					Body:   io.NopCloser(strings.NewReader(body)),
				}
				defer resp.Body.Close()
				if err := adapter().AcceptResponse(formats.Path{Path: "app/_tags/1"}, resp); err != nil {
					t.Fatal(err)
				}
				out, err := sp.Ingest(context.Background(), resp.Body, blob.IngestOptions{ContentLength: resp.ContentLength})
				if err == nil {
					_ = out.Close()
				}
				return err
			}
			if err := ingest(sha([]byte(body))); err != nil {
				t.Errorf("the right digest: %v", err)
			}
			err = ingest(sha([]byte("another")))
			var e *errs.Error
			if !errors.As(err, &e) || e.Code != errs.CodeUpstreamIntegrityMismatch {
				t.Errorf("the wrong digest: %v, want upstream_integrity_mismatch from the spool", err)
			}
		})
	}
}

// A cut tags list with a length is stored by the same spool: the wrapper drops the announced length, so the spool reads it to its end.
//
// Mechanism of R-OCI-024, not a proof of its criteria: it checks a window, a bound or a spool detail of the tags list; the
// criteria are the first five tags with a Link to trove and the referrers pass-through.
func TestTheSpoolStoresACutTagsListThatTheWrapperExtended(t *testing.T) {
	doc := `{"name":"app","tags":["a","b"]}`
	sp, err := blob.NewSpool(config.StorageSpool{Dir: t.TempDir(), MaxBytes: 64 << 20, MemoryThreshold: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	resp := &http.Response{
		StatusCode: 200, ContentLength: int64(len(doc)),
		Header: http.Header{"Link": {`<x>; rel="next"`}, "Content-Length": {"31"}},
		Body:   io.NopCloser(strings.NewReader(doc)),
	}
	defer resp.Body.Close()
	if err := adapter().AcceptResponse(formats.Path{Path: "app/_taglist"}, resp); err != nil {
		t.Fatal(err)
	}
	out, err := sp.Ingest(context.Background(), resp.Body, blob.IngestOptions{ContentLength: resp.ContentLength})
	if err != nil {
		t.Fatalf("the spool refused the extended list: %v", err)
	}
	_ = out.Close()
}
