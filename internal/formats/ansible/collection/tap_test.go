package collection_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/formats/ansible/collection"
	"github.com/guided-traffic/trove/internal/formats/ansible/collection/collectiontest"
)

type recordingTap struct {
	max   int64
	wants string
	took  map[string]string
}

func (t *recordingTap) Wants(path string) (int64, bool) {
	return t.max, strings.HasSuffix(path, t.wants)
}
func (t *recordingTap) Take(path string, body []byte) { t.took[path] = string(body) }

// TestR_ANS_034_TheTapSeesTheFirstBytesOfAWantedMember: the reader hands a tap the first bytes of the members it wants, cut at the bound
// it named, and the digests of the archive stay complete, so the archive is accepted as it is without a tap.
func TestR_ANS_034_TheTapSeesTheFirstBytesOfAWantedMember(t *testing.T) {
	s := spec()
	s.Files["docs/long.md"] = strings.Repeat("0123456789", 100)
	data := collectiontest.Build(s)
	tap := &recordingTap{max: 25, wants: ".md", took: map[string]string{}}
	a, err := collection.ReadArchive(context.Background(), bytes.NewReader(data), collection.DefaultLimits(), collection.WithTap(tap))
	if err != nil {
		t.Fatal(err)
	}
	if got := tap.took["docs/long.md"]; got != strings.Repeat("0123456789", 100)[:25] {
		t.Errorf("docs/long.md = %q, want its first 25 bytes", got)
	}
	if got := tap.took["README.md"]; got != "# tools\n" {
		t.Errorf("README.md = %q", got)
	}
	if _, ok := tap.took["plugins/modules/x.py"]; ok {
		t.Error("the tap received a member it did not want")
	}
	plain, err := read(t, data)
	if err != nil || plain.Files != a.Files {
		t.Errorf("without a tap: %v, %d files, with one %d", err, plain.Files, a.Files)
	}
	if _, err := collection.ReadArchive(context.Background(), bytes.NewReader(data), collection.DefaultLimits(), collection.WithTap(nil)); err != nil {
		t.Errorf("a nil tap: %v", err)
	}
}
