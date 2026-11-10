package ansible

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/ansible/collection/collectiontest"
	"github.com/guided-traffic/trove/internal/formats/untrusted"
)

// spooled is an archive in memory, as the fill hook receives it.
type spooled struct{ b []byte }

func (s spooled) Digests() formats.Digests       { return formats.Digests{} }
func (s spooled) Size() int64                    { return int64(len(s.b)) }
func (s spooled) Open() (io.ReadCloser, error)   { return io.NopCloser(bytes.NewReader(s.b)), nil }
func (s spooled) ReaderAt() (io.ReaderAt, error) { return bytes.NewReader(s.b), nil }

func TestR_ANS_018_TheFillHookHoldsAnArchiveToTheIdentityOfItsPath(t *testing.T) {
	a := remoteAdapter{f: New()}
	path := formats.Path{Path: "collections/acme/tools/acme-tools-1.0.0.tar.gz"}
	good := collectiontest.Build(collectiontest.Spec{Namespace: "acme", Name: "tools", Version: "1.0.0"})

	ins, err := a.InspectFill(context.Background(), formats.RemoteContext{}, path, spooled{good})
	if err != nil || ins == nil || ins.Package != "acme.tools" || ins.Version != "1.0.0" || ins.Properties["ansible.namespace"] != "acme" {
		t.Fatalf("%+v %v", ins, err)
	}
	for name, b := range map[string][]byte{
		"another collection": collectiontest.Build(collectiontest.Spec{Namespace: "acme", Name: "other", Version: "1.0.0"}),
		"another namespace":  collectiontest.Build(collectiontest.Spec{Namespace: "evil", Name: "tools", Version: "1.0.0"}),
		"another version":    collectiontest.Build(collectiontest.Spec{Namespace: "acme", Name: "tools", Version: "1.0.1"}),
		"not an archive":     []byte("not a gzip stream"),
		"an empty body":      nil,
	} {
		_, err := a.InspectFill(context.Background(), formats.RemoteContext{}, path, spooled{b})
		e, ok := errs.As(err)
		if !ok || e.Kind != errs.UpstreamFailed {
			t.Errorf("%s: %v, want a 502", name, err)
			continue
		}
		if strings.HasPrefix(name, "another") && !strings.Contains(e.Detail, "identity_mismatch") {
			t.Errorf("%s: detail %q does not name the reason", name, e.Detail)
		}
	}
}

func TestR_ANS_020_ARoleArchiveMustBeAGzipTarArchive(t *testing.T) {
	a := remoteAdapter{f: New()}
	path := formats.Path{Path: "roles/16/1.0.0.tar.gz"}
	tarball := collectiontest.Pack([]collectiontest.Entry{{Name: "role-1.0.0/", Dir: true}, {Name: "role-1.0.0/tasks/main.yml", Body: []byte("---\n")}})

	if ins, err := a.InspectFill(context.Background(), formats.RemoteContext{}, path, spooled{tarball}); err != nil || ins != nil {
		t.Errorf("a role archive: %+v %v", ins, err)
	}
	for name, b := range map[string][]byte{"text": []byte("<html>not found</html>"), "empty": nil, "truncated": tarball[:len(tarball)/2]} {
		if _, err := a.InspectFill(context.Background(), formats.RemoteContext{}, path, spooled{b}); errs.KindOf(err) != errs.UpstreamFailed {
			t.Errorf("%s: %v, want a 502", name, err)
		}
	}
	// A bound of the reader is a refusal as well.
	tight := untrusted.Limits{Entries: 1}
	if err := checkRoleArchive(context.Background(), bytes.NewReader(tarball), tight); errs.KindOf(err) != errs.UpstreamFailed {
		t.Errorf("more entries than the bound: %v", err)
	}
}
