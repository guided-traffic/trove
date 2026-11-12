package repository

import (
	"reflect"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/catalog"
)

// presetCatalog has a format with two presets, as a container registry format has: the default whose token service is on a host
// of its own, and a second registry whose token service is on another host. Neither shares a host with the other.
func presetCatalog() *catalog.Registry {
	r := testCatalog()
	r.Register(catalog.Entry{Facts: catalog.Facts{
		ID: "twopreset", Classes: []Class{Local, Remote, Virtual}, Layout: "twopreset", ProtocolBasePath: "/api/twopreset/{project}/{key}/",
		PatternSubject: SubjectName, PackageNames: true,
		UpstreamPresets: []catalog.UpstreamPreset{
			{
				ID: "hub", DisplayName: "Hub", URL: "https://registry.hub.example/", Default: true,
				ExtraHosts: []catalog.PresetHost{{Pattern: "auth.hub.example", SendCredentials: true}},
			},
			{
				ID: "lab", DisplayName: "Lab", URL: "https://registry.lab.example/",
				ExtraHosts: []catalog.PresetHost{{Pattern: "lab.example", SendCredentials: true}, {Pattern: "cdn.lab.example"}},
			},
		},
		DefaultMetadataTTLSeconds: intp(300), DefaultNotFoundTTLSeconds: intp(300),
	}, Names: lower{}, Mirror: true})
	return r
}

func validatePresetDoc(t *testing.T, include, remote string) (Document, *validation) {
	t.Helper()
	body := `{"key":"p-remote","class":"remote","format":"twopreset"`
	if include != "" {
		body += `,"includePatterns":` + include
	}
	if remote != "" {
		body += `,"remote":` + remote
	}
	d, err := DecodeDocument([]byte(body + `}`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	_, v := validate(presetCatalog(), &d, true)
	return d, v
}

// TestR_REPO_062_APresetsHostsComeWithItsURL: a create whose url is a preset's and that omits extraHosts stores that preset's
// hosts, with their credential flags; the default preset's when url is omitted; none for any other URL; and an explicit list as
// given (Q-REPO-322).
func TestR_REPO_062_APresetsHostsComeWithItsURL(t *testing.T) {
	for name, c := range map[string]struct {
		remote string
		url    string
		want   []ExtraHost
	}{
		"an omitted url takes the default preset and its hosts": {
			``, "https://registry.hub.example/", []ExtraHost{{Pattern: "auth.hub.example", SendCredentials: true}},
		},
		"the second preset's URL takes its hosts, not the default's": {
			`{"url":"https://registry.lab.example/"}`, "https://registry.lab.example/",
			[]ExtraHost{{Pattern: "lab.example", SendCredentials: true}, {Pattern: "cdn.lab.example"}},
		},
		"a preset's URL written another way is the preset once normalized": {
			`{"url":"HTTPS://Registry.Lab.Example:443"}`, "https://registry.lab.example/",
			[]ExtraHost{{Pattern: "lab.example", SendCredentials: true}, {Pattern: "cdn.lab.example"}},
		},
		"any other URL takes none": {
			`{"url":"https://registry.example.com/"}`, "https://registry.example.com/", []ExtraHost{},
		},
		"an explicit list is kept as given": {
			`{"url":"https://registry.lab.example/","extraHosts":[{"pattern":"mirror.example.com"}]}`, "https://registry.lab.example/",
			[]ExtraHost{{Pattern: "mirror.example.com"}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			d, v := validatePresetDoc(t, "", c.remote)
			if err := v.err(); err != nil {
				t.Fatal(err)
			}
			s := settingsOf(t, d)
			if s.URL != c.url || !reflect.DeepEqual(s.ExtraHosts, c.want) {
				t.Errorf("url %q, extraHosts %+v; want %q, %+v", s.URL, s.ExtraHosts, c.url, c.want)
			}
		})
	}
}

// TestR_REPO_052_EveryPresetNeedsABound: a mirror of any preset of the format, the default or another, without bounding include
// patterns and without maxBytes is refused with mirror_unbounded; a bound lets it through; another upstream gets the warning
// (Q-REPO-323).
func TestR_REPO_052_EveryPresetNeedsABound(t *testing.T) {
	for _, url := range []string{"https://registry.hub.example/", "https://registry.lab.example/", "https://registry.lab.example"} {
		t.Run("unbounded "+url, func(t *testing.T) {
			_, v := validatePresetDoc(t, "", `{"url":"`+url+`","mode":"mirror"}`)
			if got := fieldCodes(v)["/remote/mode"]; got != errs.CodeMirrorUnbounded {
				t.Fatalf("code at /remote/mode = %q, want %s", got, errs.CodeMirrorUnbounded)
			}
		})
	}
	t.Run("a bounded mirror of the second preset", func(t *testing.T) {
		_, v := validatePresetDoc(t, `["library/*"]`, `{"url":"https://registry.lab.example/","mode":"mirror"}`)
		if err := v.err(); err != nil {
			t.Fatal(err)
		}
		_, v = validatePresetDoc(t, "", `{"url":"https://registry.lab.example/","mode":"mirror","mirror":{"maxBytes":10737418240}}`)
		if err := v.err(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("another upstream is saved with the warning", func(t *testing.T) {
		_, v := validatePresetDoc(t, "", `{"url":"https://registry.example.com/","mode":"mirror"}`)
		if err := v.err(); err != nil {
			t.Fatal(err)
		}
		if got := warningCodes(v)["/remote/mode"]; got != WarnMirrorUnbounded {
			t.Errorf("warning = %q", got)
		}
	})
}
