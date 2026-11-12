package catalog

import (
	"slices"
	"strings"
	"sync"
	"testing"
)

// lower is a name rule that compares case-insensitively, as NuGet does.
type lower struct{}

func (lower) Normalize(name string) (string, bool) {
	if name == "" {
		return "", false
	}
	return strings.ToLower(name), true
}

type rangeless struct{}

func (rangeless) ValidateRange(string) error        { return nil }
func (rangeless) Prerelease(string) (bool, bool)    { return false, true }
func (rangeless) InRange(version, expr string) bool { return version == expr }

func valid(id string) Entry {
	return Entry{Facts: Facts{ID: ID(id), Classes: []Class{Local}, Layout: id, PatternSubject: SubjectPath}}
}

func TestR_REPO_003_RegisterRefusesAMalformedEntry(t *testing.T) {
	bad := map[string]Entry{
		"upper case":                valid("Generic"),
		"one character":             valid("x"),
		"an API version":            valid("v1"),
		"no class":                  {Facts: Facts{ID: "nocls", Layout: "x", PatternSubject: SubjectPath}},
		"no layout":                 {Facts: Facts{ID: "nolay", Classes: []Class{Local}, PatternSubject: SubjectPath}},
		"no subject":                {Facts: Facts{ID: "nosub", Classes: []Class{Local}, Layout: "x"}},
		"a class twice":             {Facts: Facts{ID: "twice", Classes: []Class{Local, Local}, Layout: "x", PatternSubject: SubjectPath}},
		"a class out of the set":    {Facts: Facts{ID: "oos", Classes: []Class{"mirror"}, Layout: "x", PatternSubject: SubjectPath}},
		"package names, no rule":    {Facts: Facts{ID: "norule", Classes: []Class{Local}, Layout: "x", PatternSubject: SubjectName, PackageNames: true}},
		"a rule, no package names":  {Facts: Facts{ID: "norule2", Classes: []Class{Local}, Layout: "x", PatternSubject: SubjectPath}, Names: lower{}},
		"versions, no versioning":   {Facts: Facts{ID: "nover", Classes: []Class{Local}, Layout: "x", PatternSubject: SubjectPath}, Exposure: Exposure{Exposable: true, Versions: true}},
		"dimensions, not exposable": {Facts: Facts{ID: "nodim", Classes: []Class{Local}, Layout: "x", PatternSubject: SubjectPath}, Exposure: Exposure{Tags: true}},
		// R-AUTHZ-018: an ownership default needs package names and is enforce or off.
		"an ownership default without package names": {Facts: Facts{ID: "noown", Classes: []Class{Local}, Layout: "x", PatternSubject: SubjectPath, OwnershipDefault: OwnershipEnforce}},
		"an ownership default out of the set":        {Facts: Facts{ID: "badown", Classes: []Class{Local}, Layout: "x", PatternSubject: SubjectName, PackageNames: true, OwnershipDefault: "sometimes"}, Names: lower{}},
	}
	for name, e := range bad {
		t.Run("R-REPO-003 Register panics on "+name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("no panic")
				}
			}()
			NewRegistry().Register(e)
		})
	}
}

func TestR_REPO_003_TheRegistryServesTheFactsOnce(t *testing.T) {
	r := NewRegistry()
	r.Register(valid("probe-b"))
	r.Register(valid("probe-a"))
	t.Run("a duplicate ID panics", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatal("no panic")
			}
		}()
		r.Register(valid("probe-a"))
	})
	t.Run("Lookup ignores blanks and finds the entry", func(t *testing.T) {
		if e, ok := r.Lookup(" probe-a "); !ok || e.ID != "probe-a" {
			t.Fatalf("Lookup = %+v, %v", e, ok)
		}
		if _, ok := r.Lookup("missing"); ok {
			t.Fatal("found a format that is not registered")
		}
	})
	t.Run("List is sorted by ID", func(t *testing.T) {
		got := r.List()
		if len(got) != 2 || got[0].ID != "probe-a" || got[1].ID != "probe-b" {
			t.Fatalf("List = %v", got)
		}
	})
	t.Run("Supports follows the class list", func(t *testing.T) {
		f := valid("x1").Facts
		if !f.Supports(Local) || f.Supports(Remote) || f.Supports(Virtual) {
			t.Fatal("Supports")
		}
	})
	t.Run("the registry keeps its own copy of the class list", func(t *testing.T) {
		classes := []Class{Local, Remote}
		e := valid("copied")
		e.Classes = classes
		r2 := NewRegistry()
		r2.Register(e)
		classes[0] = Virtual
		if got, _ := r2.Lookup("copied"); !slices.Equal(got.Classes, []Class{Local, Remote}) {
			t.Fatalf("classes = %v", got.Classes)
		}
	})
	t.Run("Or turns nil into the process registry", func(t *testing.T) {
		if Or(nil) != Default() || Or(r) != r {
			t.Fatal("Or")
		}
	})
}

func TestR_ARCH_010_NothingRegistersAfterFreeze(t *testing.T) {
	r := NewRegistry()
	r.Register(valid("early"))
	r.Freeze()
	defer func() {
		if recover() == nil {
			t.Fatal("registered after Freeze")
		}
	}()
	r.Register(valid("late"))
}

func TestR_ARCH_032_TheRegistryIsSafeForConcurrentUse(t *testing.T) {
	r := NewRegistry()
	var wg sync.WaitGroup
	for _, id := range []string{"con-a", "con-b", "con-c", "con-d"} {
		wg.Add(2)
		go func() { defer wg.Done(); r.Register(valid(id)) }()
		go func() { defer wg.Done(); r.List(); r.Lookup(id) }()
	}
	wg.Wait()
	if n := len(r.List()); n != 4 {
		t.Fatalf("%d formats, want 4", n)
	}
}

// TestR_AUTHZ_005_OneNameNormaliser is the rule the decision point, the
// repository model and the exposure rules share: a name and a pattern fold with
// the one function of the format, and a format without a rule folds nothing.
func TestR_AUTHZ_005_OneNameNormaliser(t *testing.T) {
	r := NewRegistry()
	files := valid("files")
	pkg := valid("pkgs")
	pkg.PackageNames, pkg.PatternSubject, pkg.Names = true, SubjectName, lower{}
	r.Register(files)
	r.Register(pkg)
	cases := []struct {
		name, format, in, want string
		ok                     bool
	}{
		{"a format with a rule folds a name", "pkgs", "Acme.Core", "acme.core", true},
		{"and folds a pattern with the same function", "pkgs", "Acme.*", "acme.*", true},
		{"and leaves the pattern characters unchanged", "pkgs", `[A-Z]?{A,B}!\*`, `[a-z]?{a,b}!\*`, true},
		{"it is idempotent", "pkgs", "acme.core", "acme.core", true},
		{"a string that is no name is reported", "pkgs", "", "", false},
		{"a format without a rule folds nothing", "files", "Acme.Core", "Acme.Core", true},
		{"a format the registry does not know folds nothing", "elsewhere", "Acme.Core", "Acme.Core", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := r.NormalizeName(c.format, c.in)
			if got != c.want || ok != c.ok {
				t.Fatalf("NormalizeName(%q, %q) = %q, %v; want %q, %v", c.format, c.in, got, ok, c.want, c.ok)
			}
			if again, _ := r.NormalizeName(c.format, got); c.ok && again != got {
				t.Fatalf("not idempotent: %q then %q", got, again)
			}
		})
	}
}

func TestR_EP_018_ExposureFactsFailClosed(t *testing.T) {
	r := NewRegistry()
	plain := valid("plain")
	exposed := valid("exposed")
	exposed.Exposure = Exposure{Exposable: true}
	versioned := valid("versioned")
	versioned.PackageNames, versioned.PatternSubject, versioned.Names = true, SubjectName, lower{}
	versioned.Exposure = Exposure{Exposable: true, Packages: true, Versions: true, Versioning: rangeless{}}
	r.Register(plain)
	r.Register(exposed)
	r.Register(versioned)
	if e, _ := r.Lookup("plain"); e.Exposure.Exposable {
		t.Fatal("the zero Exposure must not be exposable")
	}
	if e, _ := r.Lookup("exposed"); !e.Exposure.Exposable || e.Exposure.Versions {
		t.Fatalf("exposure = %+v", e.Exposure)
	}
	if e, _ := r.Lookup("versioned"); e.Exposure.Versioning == nil || !e.Exposure.Packages {
		t.Fatalf("exposure = %+v", e.Exposure)
	}
}

// The registry root is a fact of the registration (Q-OCI-4): at most one format is the root of a host, and its base path is
// the registry's.
func TestRegistryRootIsChecked(t *testing.T) {
	root := func(id string) Entry {
		e := valid(id)
		e.RegistryRoot, e.ProtocolBasePath = true, RegistryRootBasePath
		return e
	}
	t.Run("a registry root is accepted once", func(t *testing.T) {
		r := NewRegistry()
		r.Register(root("probe-root"))
		if e, ok := r.Lookup("probe-root"); !ok || !e.RegistryRoot {
			t.Fatalf("entry = %+v", e)
		}
		defer func() {
			if msg, _ := recover().(string); !strings.Contains(msg, "registry root") {
				t.Fatalf("panic = %q", msg)
			}
		}()
		r.Register(root("probe-root2"))
	})
	t.Run("the root has the base path of the registry", func(t *testing.T) {
		e := root("probe-badbase")
		e.ProtocolBasePath = "/api/probe-badbase/{project}/{key}/"
		defer func() {
			if recover() == nil {
				t.Fatal("no panic")
			}
		}()
		NewRegistry().Register(e)
	})
	t.Run("a format that is no root may have the base path of an API format", func(t *testing.T) {
		e := valid("probe-api")
		e.ProtocolBasePath = "/api/probe-api/{project}/{key}/"
		NewRegistry().Register(e)
	})
}

// remoteWith is a format with the remote class and the given presets.
func remoteWith(id string, presets ...UpstreamPreset) Entry {
	e := valid(id)
	e.Classes = []Class{Local, Remote}
	e.UpstreamPresets = presets
	return e
}

func preset(id, url string, def bool, hosts ...PresetHost) UpstreamPreset {
	return UpstreamPreset{ID: id, DisplayName: "Registry " + id, URL: url, Default: def, ExtraHosts: hosts}
}

// TestR_REPO_062_RegisterRefusesPresetsThatBreakTheRules: a format's upstream presets are refused at registration on a format
// without the remote class, without a default or with two, with an id or a URL twice, with an id out of its shape, without a
// name or a URL, and with a host pattern twice in one preset.
func TestR_REPO_062_RegisterRefusesPresetsThatBreakTheRules(t *testing.T) {
	a := preset("alpha", "https://a.example.com/", true, PresetHost{Pattern: "auth.example.com", SendCredentials: true})
	b := preset("beta", "https://b.example.com/", false)
	local := valid("probe-local")
	local.UpstreamPresets = []UpstreamPreset{a}
	bad := map[string]Entry{
		"presets without the remote class": local,
		"no default":                       remoteWith("probe-nodef", preset("alpha", "https://a.example.com/", false), b),
		"two defaults":                     remoteWith("probe-twodef", a, preset("beta", "https://b.example.com/", true)),
		"an id twice":                      remoteWith("probe-idtwice", a, preset("alpha", "https://b.example.com/", false)),
		"a URL twice":                      remoteWith("probe-urltwice", a, preset("beta", "https://a.example.com/", false)),
		"an id out of its shape":           remoteWith("probe-badid", a, preset("Beta_1", "https://b.example.com/", false)),
		"no display name":                  remoteWith("probe-noname", a, UpstreamPreset{ID: "beta", URL: "https://b.example.com/"}),
		"no URL":                           remoteWith("probe-nourl", a, UpstreamPreset{ID: "beta", DisplayName: "Beta"}),
		"a host pattern twice": remoteWith("probe-hosttwice", preset("alpha", "https://a.example.com/", true,
			PresetHost{Pattern: "auth.example.com", SendCredentials: true}, PresetHost{Pattern: "auth.example.com"})),
	}
	for name, e := range bad {
		t.Run("R-REPO-062 Register panics on "+name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("no panic")
				}
			}()
			NewRegistry().Register(e)
		})
	}
	t.Run("R-REPO-062 the presets of a remote format are accepted and served in their order", func(t *testing.T) {
		r := NewRegistry()
		r.Register(remoteWith("probe-ok", b, a))
		e, _ := r.Lookup("probe-ok")
		if len(e.UpstreamPresets) != 2 || e.UpstreamPresets[0].ID != "beta" || e.UpstreamPresets[1].ID != "alpha" {
			t.Fatalf("presets = %+v", e.UpstreamPresets)
		}
		if d, ok := e.DefaultPreset(); !ok || d.ID != "alpha" {
			t.Errorf("default = %+v, %v", d, ok)
		}
		if p, ok := e.PresetFor("https://b.example.com/"); !ok || p.ID != "beta" {
			t.Errorf("preset for b = %+v, %v", p, ok)
		}
		if _, ok := e.PresetFor("https://c.example.com/"); ok {
			t.Error("a URL of no preset names one")
		}
	})
	t.Run("R-REPO-062 a format without presets has no default", func(t *testing.T) {
		if _, ok := valid("probe-none").DefaultPreset(); ok {
			t.Error("a default without presets")
		}
	})
}
