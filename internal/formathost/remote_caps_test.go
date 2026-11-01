package formathost

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/proxy"
	"github.com/guided-traffic/trove/internal/repository"
)

// capsRemote is a remote adapter with the two optional interfaces; its upstream requests are built below the
// base the discovery document named.
type capsRemote struct {
	formats.RemoteAdapter
	refuse bool
}

func (capsRemote) UpstreamRequest(ctx context.Context, rc formats.RemoteContext, p formats.Path, _ string) (*http.Request, error) {
	u := "https://up.example/" + p.Path
	if base, ok := rc.Discovery.(string); ok {
		u = base + p.Path
	}
	return http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
}

func (capsRemote) DiscoveryDocument() (formats.Path, string, bool) {
	return formats.Path{Path: "svc/index.json"}, "v", true
}

func (capsRemote) ParseDiscovery(_ context.Context, _ formats.RemoteContext, doc []byte) (any, error) {
	if len(doc) == 0 {
		return nil, errors.New("empty")
	}
	return string(doc), nil
}

func (capsRemote) InspectsFill(p formats.Path) bool { return p.Path == "a.pkg" }

func (c capsRemote) InspectFill(_ context.Context, rc formats.RemoteContext, _ formats.Path, sp formats.Spooled) (*formats.Inspection, error) {
	if c.refuse {
		return nil, errors.New("refused")
	}
	b, err := io.ReadAll(mustOpen(sp))
	if err != nil {
		return nil, err
	}
	return &formats.Inspection{Package: string(b), Version: rc.Discovery.(string)}, nil
}

func mustOpen(sp formats.Spooled) io.Reader {
	r, err := sp.Open()
	if err != nil {
		panic(err)
	}
	return r
}

func capsAdapter(ra capsRemote) *adapter {
	return &adapter{ra: ra, repo: &repository.Repository{}, caps: formats.RemoteCaps{Discovery: ra, Fill: ra}}
}

// TestR_NUGET_014_AdapterBindsTheParsedDiscoveryDocument: the engine's adapter answers the names of the discovery
// document from the format's Discoverer, and the adapter Discover returns hands the parsed data to every call as
// RemoteContext.Discovery (WP3.0b).
func TestR_NUGET_014_AdapterBindsTheParsedDiscoveryDocument(t *testing.T) {
	a := capsAdapter(capsRemote{})
	if p, v, ok := a.DiscoveryDocument(); !ok || p != "svc/index.json" || v != "v" {
		t.Fatalf("DiscoveryDocument = %q %q %v", p, v, ok)
	}
	base := proxy.UpstreamBase{URL: &url.URL{Scheme: "https", Host: "up.example"}}
	before, err := a.UpstreamRequest(context.Background(), base, "x", "")
	if err != nil || before.URL.String() != "https://up.example/x" {
		t.Fatalf("before discovery: %v %v", before, err)
	}
	bound, err := a.Discover(context.Background(), []byte("https://addr.example/"))
	if err != nil {
		t.Fatal(err)
	}
	after, err := bound.UpstreamRequest(context.Background(), base, "x", "")
	if err != nil || after.URL.String() != "https://addr.example/x" {
		t.Fatalf("after discovery: %v %v", after, err)
	}
	if again, _ := a.UpstreamRequest(context.Background(), base, "x", ""); again.URL.String() != "https://up.example/x" {
		t.Fatal("Discover changed the adapter it was called on; requests share it")
	}
	if _, err := a.Discover(context.Background(), nil); err == nil {
		t.Fatal("a document the format cannot parse was accepted")
	}
}

// TestR_NUGET_015_AdapterRunsTheFillHook: InspectsFill follows the format's declaration per path, InspectFill hands the
// spooled artifact to the format and the Inspection travels to OnCommit as the payload of the commit; a refusal is an
// error (WP3.0a).
func TestR_NUGET_015_AdapterRunsTheFillHook(t *testing.T) {
	a := capsAdapter(capsRemote{})
	if !a.InspectsFill("a.pkg") || a.InspectsFill("a.nuspec") {
		t.Fatal("InspectsFill does not follow the format's declaration")
	}
	bound, err := a.Discover(context.Background(), []byte("1.0"))
	if err != nil {
		t.Fatal(err)
	}
	sp := spoolOf(t, "demo")
	got, err := bound.InspectFill(context.Background(), "a.pkg", sp)
	if err != nil {
		t.Fatal(err)
	}
	cp, ok := got.(*commitPayload)
	if !ok || cp.inspection == nil || cp.inspection.Package != "demo" || cp.inspection.Version != "1.0" {
		t.Fatalf("payload = %#v; the inspection must carry the body and the discovery data", got)
	}
	if _, err := capsAdapter(capsRemote{refuse: true}).InspectFill(context.Background(), "a.pkg", sp); err == nil {
		t.Fatal("a refusal was swallowed")
	}
}

// TestR_NUGET_015_AdapterWithoutCapabilitiesHasNoHook: a format without the optional interfaces has no discovery
// document and no hook.
func TestR_NUGET_015_AdapterWithoutCapabilitiesHasNoHook(t *testing.T) {
	a := &adapter{ra: capsRemote{}, repo: &repository.Repository{}}
	if _, _, ok := a.DiscoveryDocument(); ok {
		t.Fatal("a discovery document without a Discoverer")
	}
	if a.InspectsFill("a.pkg") {
		t.Fatal("a hook without a FillInspector")
	}
	if got, err := a.InspectFill(context.Background(), "a.pkg", spoolOf(t, "x")); got != nil || err != nil {
		t.Fatalf("InspectFill = %v, %v", got, err)
	}
	if same, err := a.Discover(context.Background(), []byte("x")); err != nil || same != proxy.Adapter(a) {
		t.Fatalf("Discover = %v, %v", same, err)
	}
}

func spoolOf(t *testing.T, body string) *items.Spooled {
	t.Helper()
	spool, err := blob.NewSpool(config.StorageSpool{Dir: t.TempDir(), MaxBytes: 64 << 20, MemoryThreshold: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	sp, err := spool.Ingest(context.Background(), strings.NewReader(body), blob.IngestOptions{ContentLength: int64(len(body))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sp.Close() })
	return sp
}
