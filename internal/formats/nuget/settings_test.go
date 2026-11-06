package nuget

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/internal/formats/nuget/nugettest"
	"github.com/guided-traffic/trove/internal/formats/nuget/nupkg"
)

// TestR_NUGET_007_SettingsOfAStoredDocument: the nuget member is read back with the defaults
// of what the document leaves out; a value the schema does not allow, which only a row written
// around the service can hold, is the default.
func TestR_NUGET_007_SettingsOfAStoredDocument(t *testing.T) {
	cases := []struct {
		name, doc string
		want      settings
	}{
		{"no member", `{"description":"x"}`, settings{DeleteMode: "unlist", ForwardSearch: true}},
		{"not a document", `not json`, settings{DeleteMode: "unlist", ForwardSearch: true}},
		{"delete mode delete", `{"nuget":{"deleteMode":"delete"}}`, settings{DeleteMode: "delete", ForwardSearch: true}},
		{"delete mode unlist", `{"nuget":{"deleteMode":"unlist"}}`, settings{DeleteMode: "unlist", ForwardSearch: true}},
		{"a delete mode that is none", `{"nuget":{"deleteMode":"purge"}}`, settings{DeleteMode: "unlist", ForwardSearch: true}},
		{"forwarding off", `{"nuget":{"forwardSearch":false}}`, settings{DeleteMode: "unlist", ForwardSearch: false}},
		{"both", `{"nuget":{"deleteMode":"delete","forwardSearch":false}}`, settings{DeleteMode: "delete", ForwardSearch: false}},
	}
	for _, c := range cases {
		t.Run("R-NUGET-007 "+c.name, func(t *testing.T) {
			if got := settingsOf([]byte(c.doc)); got != c.want {
				t.Errorf("settingsOf = %+v, want %+v", got, c.want)
			}
		})
	}
	if d := defaultSettings(); d.DeleteMode != DeleteModeUnlist || !d.ForwardSearch {
		t.Errorf("defaults = %+v", d)
	}
}

// TestR_ARCH_008_TheSettingsSchemaIsEnforcedPerClass: the schema the format registers is the
// one the repository service validates against - a value outside the enum is refused, an
// unknown member is refused, and a setting of another class is not applicable (R-REPO-004).
func TestR_ARCH_008_TheSettingsSchemaIsEnforcedPerClass(t *testing.T) {
	reg := catalog.NewRegistry()
	reg.Register(formats.CatalogEntry(New()))
	e, ok := reg.Lookup("nuget")
	if !ok {
		t.Fatal("nuget is not in the catalog")
	}
	cases := []struct {
		name  string
		class catalog.Class
		doc   string
		want  []string // codes
	}{
		{"deleteMode delete on a local repository", catalog.Local, `{"deleteMode":"delete"}`, nil},
		{"deleteMode unlist on a local repository", catalog.Local, `{"deleteMode":"unlist"}`, nil},
		{"an empty member", catalog.Local, `{}`, nil},
		{"deleteMode of another value", catalog.Local, `{"deleteMode":"purge"}`, []string{catalog.SettingInvalid}},
		{"an unknown member", catalog.Local, `{"symbolServerUrl":"https://symbols.nuget.org/download/symbols"}`, []string{catalog.SettingInvalid}},
		{"forwardSearch on a local repository", catalog.Local, `{"forwardSearch":true}`, []string{catalog.SettingNotApplicable}},
		{"deleteMode on a remote repository", catalog.Remote, `{"deleteMode":"delete"}`, []string{catalog.SettingNotApplicable}},
		{"forwardSearch on a remote repository", catalog.Remote, `{"forwardSearch":false}`, nil},
		{"forwardSearch of the wrong type", catalog.Remote, `{"forwardSearch":"no"}`, []string{catalog.SettingInvalid}},
	}
	for _, c := range cases {
		t.Run("R-ARCH-008 "+c.name, func(t *testing.T) {
			var got []string
			for _, v := range e.ValidateSettings(c.class, json.RawMessage(c.doc)) {
				got = append(got, v.Code)
			}
			if len(got) != len(c.want) || (len(got) == 1 && got[0] != c.want[0]) {
				t.Errorf("violations %v, want %v", got, c.want)
			}
		})
	}
}

// TestR_NUGET_001_TheDescriptorCarriesTheCatalogFacts: what the catalog answers for the format
// (GET /api/v1/formats, the create form, the repository model) is the registration of
// R-NUGET-001 - the upstream, the periods of Q-NUGET-2, the checksum rule of R-NUGET-015.
func TestR_NUGET_001_TheDescriptorCarriesTheCatalogFacts(t *testing.T) {
	d := New().Descriptor()
	if d.ID != "nuget" || d.Layout != "nuget" || d.ProtocolBasePath != "/api/nuget/{project}/{key}/" || d.DisplayName != "NuGet" {
		t.Errorf("identity = %s %s %s %s", d.ID, d.Layout, d.ProtocolBasePath, d.DisplayName)
	}
	if !slices.Contains(d.Classes, formats.Local) || !slices.Contains(d.Classes, formats.Remote) || !slices.Contains(d.Classes, formats.Virtual) {
		t.Errorf("classes = %v, want local, remote and virtual (Q-NUGET-28, Q-NUGET-56)", d.Classes)
	}
	if d.DefaultUpstreamURL != "https://api.nuget.org/v3/index.json" || len(d.DefaultExtraHosts) != 1 || d.DefaultExtraHosts[0] != "*.nuget.org" || d.ProbePath != "" {
		t.Errorf("upstream = %q %v probe %q", d.DefaultUpstreamURL, d.DefaultExtraHosts, d.ProbePath)
	}
	if d.DefaultMetadataTTLSeconds == nil || *d.DefaultMetadataTTLSeconds != 300 || d.DefaultNotFoundTTLSeconds == nil || *d.DefaultNotFoundTTLSeconds != 1800 {
		t.Errorf("periods = %v %v, want 300 and 1800 (Q-NUGET-2)", d.DefaultMetadataTTLSeconds, d.DefaultNotFoundTTLSeconds)
	}
	if !d.ChecksumRequireUnsupported {
		t.Error("checksumPolicy: require is not marked unsupported (R-NUGET-015, Q-ARCH-269)")
	}
	if d.PatternSubject != formats.SubjectName || !d.PackageNames || !d.PriorityMembers || !d.PinsAndClaims || d.OwnershipDefault != catalog.OwnershipEnforce {
		t.Errorf("name facts = %+v", d.Facts)
	}
	if len(d.NotifyAreas) != 1 || d.NotifyAreas[0] != catalog.MetadataChangedArea {
		t.Errorf("notify areas = %v", d.NotifyAreas)
	}
	if d.PropertyPrefix != "nuget." || d.DataPort == nil || d.DataPort.Name() != "Store" {
		t.Errorf("prefix %q, data port %v", d.PropertyPrefix, d.DataPort)
	}
	if err := formats.Validate(New()); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

// TestR_NUGET_034_TheExposureAdapter: NuGet rules narrow packages and versions, never tags;
// ranges are NuGet's grammar without floating versions (R-EP-012, R-NUGET-003).
func TestR_NUGET_034_TheExposureAdapter(t *testing.T) {
	x := New().Exposure()
	if x == nil {
		t.Fatal("NuGet is not exposable")
	}
	if dim := x.Dimensions(); !dim.Packages || !dim.Versions || dim.Tags {
		t.Errorf("dimensions = %+v", dim)
	}
	for _, r := range []string{"1.0", "[1.0,2.0)", "(,1.0]", "[1.0]", " [1.0.0-rc.1, ) "} {
		if err := x.ValidateRange(r); err != nil {
			t.Errorf("ValidateRange(%q): %v", r, err)
		}
	}
	for _, r := range []string{"", "1.0.*", "*", "[1.0", "(1.0)x", "[2.0,1.0]", "^1.0.0", ">=1.0.0"} {
		if err := x.ValidateRange(r); err == nil {
			t.Errorf("ValidateRange(%q) accepts a range outside the grammar", r)
		}
	}
	for v, want := range map[string]bool{"1.0.0": false, "1.0.0-rc.1": true, "2.0.0-beta+meta": true, "1.0.0+meta": false} {
		if pre, ok := x.Prerelease(v); !ok || pre != want {
			t.Errorf("Prerelease(%q) = %v, %v, want %v", v, pre, ok, want)
		}
	}
	if _, ok := x.Prerelease("one"); ok {
		t.Error("Prerelease accepts a text that is no version")
	}
	inRange := map[[2]string]bool{
		{"1.5.0", "[1.0,2.0)"}: true, {"2.0.0", "[1.0,2.0)"}: false, {"1.0.0", "1.0"}: true, {"0.9.0", "1.0"}: false,
		{"1.0.0", "[1.0]"}: true, {"1.0.1", "[1.0]"}: false, {"1.0.0-rc.1", "[1.0.0, )"}: false, {"junk", "1.0"}: false, {"1.0.0", "junk"}: false,
	}
	for k, want := range inRange {
		if got := x.InRange(k[0], k[1]); got != want {
			t.Errorf("InRange(%q, %q) = %v, want %v", k[0], k[1], got, want)
		}
		if again := x.InRange(k[0], k[1]); again != want { // the memoized range answers alike
			t.Errorf("InRange(%q, %q) differs on the second call", k[0], k[1])
		}
	}
}

// TestR_NUGET_005_TheReplicaIsBoundPerHost: the nuget section of the configuration reaches the
// handlers by the Host of the request, a Host nobody bound runs on the defaults, and the
// archive bounds map onto the limits of the package reader (Q-NUGET-23).
func TestR_NUGET_005_TheReplicaIsBoundPerHost(t *testing.T) {
	f := New()
	unbound := formattest.NewHost(f, formattest.HostOptions{Data: nugettest.New()})
	def := f.replicaOf(unbound)
	if got := def.archiveLimits().WithDefaults(); got != (nupkg.Limits{}).WithDefaults() {
		t.Errorf("the default archive limits = %+v, the reader's defaults are %+v", got, (nupkg.Limits{}).WithDefaults())
	}
	if def.cfg.Registration.PageSize != 64 || def.cfg.Registration.InlineBelow != 128 || def.cfg.Search.DefaultTake != 20 || def.cfg.Search.MaxTake != 1000 || def.cfg.Search.MaxSkip != 3000 {
		t.Errorf("default replica = %+v", def.cfg)
	}

	bound := formattest.NewHost(f, formattest.HostOptions{Data: nugettest.New()})
	cfg := &config.Config{NuGet: config.NuGet{
		Archive:      config.NuGetArchive{MaxNuspecBytes: 64 * config.KiB, MaxEntries: 100, MaxUncompressedBytes: 10 * config.MiB, MaxCompressionRatio: 20},
		Registration: config.NuGetRegistration{PageSize: 10, InlineBelow: 20},
		Search:       config.NuGetSearch{DefaultTake: 5, MaxTake: 50, MaxSkip: 100},
	}}
	f.Bind(bound, cfg, nil)
	r := f.replicaOf(bound)
	if r.cfg != cfg.NuGet {
		t.Errorf("bound replica = %+v, want %+v", r.cfg, cfg.NuGet)
	}
	want := nupkg.Limits{MaxEntries: 100, MaxUncompressedBytes: 10 << 20, MaxRatio: 20, MaxNuspecBytes: 64 << 10}
	if got := r.archiveLimits(); got != want {
		t.Errorf("archiveLimits = %+v, want %+v", got, want)
	}
	if f.replicaOf(unbound) == r {
		t.Error("binding one Host bound another")
	}
	if f.replicaOfPort(bound.FormatData(ID)) != r {
		t.Error("the replica is not found by its data port")
	}
	if f.replicaOfPort(nil).cfg != def.cfg || f.replicaOfPort(struct{}{}).cfg != def.cfg {
		t.Error("a port nobody bound does not run on the defaults")
	}
}
