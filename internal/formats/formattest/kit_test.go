package formattest_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/internal/formats/formattest/crafted"
	"github.com/guided-traffic/trove/internal/formats/formattest/testformat"
)

// bare is a format with no routes, no clients and no package names: the shape of
// generic.
type bare struct{ formats.Base }

func (bare) Descriptor() formats.Descriptor {
	return formats.Descriptor{
		Facts: catalogFacts(), ContractVersion: formats.ContractVersion,
	}
}
func (bare) Routes() []formats.Route { return nil }
func (bare) Match(m, p string) (*formats.Route, formats.Params, error) {
	return formats.MatchRoutes(nil, m, p)
}

func (bare) Classify(formats.Path) formats.PathInfo {
	return formats.PathInfo{Kind: formats.Content}
}
func (bare) PackageDocumentPath(string) (formats.Path, bool) { return formats.Path{}, false }

// guided is bare with a guide: a guide without clients.
type guided struct{ bare }

func (guided) Guide() []formats.GuideSection {
	return []formats.GuideSection{{ID: "g.what", Topic: formats.GuideWhat, Title: "What", Markdown: "x"}}
}

// override replaces parts of the test format.
type override struct {
	testformat.Format
	templates []formats.SetupTemplate
	guide     []formats.GuideSection
	remote    formats.RemoteAdapter
	match     func(method, p string) (*formats.Route, formats.Params, error)
	inspect   func(context.Context, formats.InspectInput) (*formats.Inspection, error)
}

func (o override) SetupTemplates() []formats.SetupTemplate {
	if o.templates != nil {
		return o.templates
	}
	return o.Format.SetupTemplates()
}

func (o override) Guide() []formats.GuideSection {
	if o.guide != nil {
		return o.guide
	}
	return o.Format.Guide()
}

func (o override) Remote() formats.RemoteAdapter {
	if o.remote != nil {
		return o.remote
	}
	return o.Format.Remote()
}

func (o override) Match(m, p string) (*formats.Route, formats.Params, error) {
	if o.match != nil {
		return o.match(m, p)
	}
	return o.Format.Match(m, p)
}

func (o override) Inspect(ctx context.Context, in formats.InspectInput) (*formats.Inspection, error) {
	if o.inspect != nil {
		return o.inspect(ctx, in)
	}
	return o.Format.Inspect(ctx, in)
}

func messages(r formattest.Report, c formattest.Check) string {
	return strings.Join(r.Result(c).Failures, "\n")
}

func TestR_ARCH_010_KitSkips(t *testing.T) {
	allSkipped := map[formattest.Check]string{
		formattest.CheckAmbiguity: "no routes: the protocol is /files/", formattest.CheckRewrite: "no remote class",
		formattest.CheckMerge: "no virtual class", formattest.CheckInspect: "Inspect reads no archive",
		formattest.CheckHeaders: "no routes", formattest.CheckSetup: "no clients",
		formattest.CheckRevision: "no package rows", formattest.CheckNames: "no package names",
		formattest.CheckView: "no package screens", formattest.CheckShared: "no routes", formattest.CheckAuthzPaths: "no routes",
	}
	fx := formattest.Fixture{
		Paths: []formattest.PathCase{{Path: "a/b.bin", Want: formats.PathInfo{Kind: formats.Content}}},
		Skip:  allSkipped,
	}
	t.Run("R-ARCH-010 a format without routes passes with reasoned skips, checks 2 and 3 still run", func(t *testing.T) {
		rep := formattest.Evaluate(bare{}, fx)
		if f := rep.Failed(); len(f) != 0 {
			t.Fatalf("failed: %v", rep)
		}
		if !rep.Result(formattest.CheckTraversal).Passed() || !rep.Result(formattest.CheckClassify).Passed() {
			t.Fatalf("checks 2 and 3 did not run: %v", rep)
		}
		if rep.Result(formattest.CheckHeaders).Skipped != "no routes" || rep.Result(formattest.CheckHeaders).Passed() {
			t.Fatal("a skipped check reports as passed")
		}
	})
	t.Run("R-ARCH-010 a skip without a reason fails the check", func(t *testing.T) {
		skips := map[formattest.Check]string{}
		for c, r := range allSkipped {
			skips[c] = r
		}
		skips[formattest.CheckSetup] = ""
		rep := formattest.Evaluate(bare{}, formattest.Fixture{Paths: fx.Paths, Skip: skips})
		if got := rep.Failed(); len(got) != 1 || got[0] != formattest.CheckSetup || !strings.Contains(messages(rep, formattest.CheckSetup), "without a reason") {
			t.Fatalf("failed = %v: %v", got, rep)
		}
	})
	t.Run("R-ARCH-010 a check that is not skipped and has nothing to run over fails", func(t *testing.T) {
		rep := formattest.Evaluate(bare{}, formattest.Fixture{})
		want := []formattest.Check{
			formattest.CheckAmbiguity, formattest.CheckClassify, formattest.CheckRewrite, formattest.CheckMerge, formattest.CheckInspect,
			formattest.CheckHeaders, formattest.CheckSetup, formattest.CheckRevision, formattest.CheckNames, formattest.CheckView,
			formattest.CheckShared, formattest.CheckAuthzPaths,
		}
		got := rep.Failed()
		if len(got) != len(want) {
			t.Fatalf("failed = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("failed = %v, want %v", got, want)
			}
		}
	})
	t.Run("R-ARCH-010 a skip the format contradicts fails: routes, an upload route, a write route, a remote adapter, a virtual adapter, clients, package names", func(t *testing.T) {
		for _, c := range []formattest.Check{
			formattest.CheckAmbiguity, formattest.CheckClassify, formattest.CheckRewrite, formattest.CheckMerge, formattest.CheckHeaders,
			formattest.CheckSetup, formattest.CheckNames, formattest.CheckTraversal, formattest.CheckInspect, formattest.CheckRevision, formattest.CheckShared,
			formattest.CheckAuthzPaths,
		} {
			rep := formattest.Evaluate(testformat.New(), formattest.Fixture{Skip: map[formattest.Check]string{c: "not applicable"}})
			if got := messages(rep, c); !strings.Contains(got, "skipped") && !strings.Contains(got, "always applies") {
				t.Errorf("%v: skip accepted: %q", c, got)
			}
		}
	})
	t.Run("R-ARCH-010 a later class cannot pass a check vacuously: generic with a remote adapter may not keep its skip", func(t *testing.T) {
		rep := formattest.Evaluate(testformat.New(), formattest.Fixture{Skip: map[formattest.Check]string{formattest.CheckRewrite: "local only"}})
		if got := messages(rep, formattest.CheckRewrite); !strings.Contains(got, "has a remote adapter") {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("R-ARCH-010 a fixture that lacks its cases fails every check that needs them", func(t *testing.T) {
		rep := formattest.Evaluate(testformat.New(), formattest.Fixture{Host: testformat.Fixture().Host})
		for c, want := range map[formattest.Check]string{
			formattest.CheckRewrite: "no Remote documents", formattest.CheckMerge: "no Merge cases", formattest.CheckInspect: "no crafted Inspect case",
			formattest.CheckHeaders: "has no content case", formattest.CheckRevision: "has no change case", formattest.CheckNames: "no sample package names",
		} {
			if got := messages(rep, c); !strings.Contains(got, want) {
				t.Errorf("%v: %q does not say %q", c, got, want)
			}
		}
	})
	t.Run("R-ARCH-010 the subtest names carry the check number", func(t *testing.T) {
		if got := formattest.CheckHeaders.String(); got != "R-ARCH-010 check 7 content headers" {
			t.Fatalf("name = %q", got)
		}
	})
}

func TestR_ARCH_010_KitChecksCatchTheirDefects(t *testing.T) {
	fx := testformat.Fixture()
	t.Run("R-ARCH-010 check 2: a Match that panics on hostile input names the input", func(t *testing.T) {
		o := override{Format: testformat.New(), match: func(m, p string) (*formats.Route, formats.Params, error) {
			if strings.Contains(p, "\x00") {
				panic("boom")
			}
			return testformat.New().Match(m, p)
		}}
		got := messages(formattest.Evaluate(o, fx), formattest.CheckTraversal)
		if !strings.Contains(got, "panicked") || !strings.Contains(got, "boom") {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("R-NUGET-001 check 3: the canonical path of an example is the one Classify is asked about", func(t *testing.T) {
		identity := fx
		identity.Canonical = func(ex string) (string, bool) { return ex, true }
		if got := messages(formattest.Evaluate(testformat.New(), identity), formattest.CheckClassify); got != "" {
			t.Fatalf("the identity mapping is refused: %q", got)
		}
		// A route that addresses no canonical path is held to its kind by checks 7 and 9; a
		// download serves a stored item, so only the downloads are refused.
		skipped := fx
		skipped.Canonical = func(string) (string, bool) { return "", false }
		got := messages(formattest.Evaluate(testformat.New(), skipped), formattest.CheckClassify)
		if got == "" {
			t.Fatal("a download whose example addresses no canonical path passes")
		}
		for _, line := range strings.Split(got, "\n") {
			if !strings.Contains(line, "is a download and its example") {
				t.Fatalf("a route without a canonical path that is no download is refused: %q", line)
			}
		}
		wrong := fx
		wrong.Canonical = func(string) (string, bool) { return "", true }
		if got := messages(formattest.Evaluate(testformat.New(), wrong), formattest.CheckClassify); !strings.Contains(got, "not content") {
			t.Fatalf("a canonical path of another kind passes: %q", got)
		}
	})
	t.Run("R-NUGET-001 check 10: the protocol path of a package document is the one Match is asked about", func(t *testing.T) {
		identity := fx
		identity.Protocol = func(c string) string { return c }
		if got := messages(formattest.Evaluate(testformat.New(), identity), formattest.CheckNames); got != "" {
			t.Fatalf("the identity mapping is refused: %q", got)
		}
		wrong := fx
		wrong.Protocol = func(string) string { return "no/route/here" }
		if got := messages(formattest.Evaluate(testformat.New(), wrong), formattest.CheckNames); !strings.Contains(got, "does not match a route") {
			t.Fatalf("a protocol path that matches no route passes: %q", got)
		}
	})
	t.Run("R-ARCH-010 check 8: a template that does not parse, does not execute, or belongs to no client fails", func(t *testing.T) {
		base := testformat.New().SetupTemplates()
		tpl := func(id, client, text string) formats.SetupTemplate {
			return formats.SetupTemplate{ID: id, Client: client, Section: "install", Lang: "shell", Template: text}
		}
		cases := map[string][]formats.SetupTemplate{
			"does not parse":          append(append([]formats.SetupTemplate(nil), base...), tpl("x1", "tf", "{{.Registry")),
			"does not execute":        append(append([]formats.SetupTemplate(nil), base...), tpl("x2", "tf", "{{.Token}}")),
			"which the descriptor":    append(append([]formats.SetupTemplate(nil), base...), tpl("x3", "other", "x")),
			"is used twice":           append(append([]formats.SetupTemplate(nil), base...), tpl("tf.install", "tf", "x")),
			"has language":            append(append([]formats.SetupTemplate(nil), base...), formats.SetupTemplate{ID: "x4", Client: "tf", Section: "install", Lang: "perl", Template: "x"}),
			"has section":             append(append([]formats.SetupTemplate(nil), base...), formats.SetupTemplate{ID: "x5", Client: "tf", Section: "other", Lang: "shell", Template: "x"}),
			"a client of the format ": append(append([]formats.SetupTemplate(nil), base...), tpl("", "tf", "x")),
		}
		for want, ts := range cases {
			got := messages(formattest.Evaluate(override{Format: testformat.New(), templates: ts}, fx), formattest.CheckSetup)
			if got == "" {
				t.Errorf("%s: nothing failed", want)
			}
			if want != "a client of the format " && !strings.Contains(got, want) {
				t.Errorf("%q does not say %q", got, want)
			}
		}
	})
	t.Run("R-ARCH-010 check 8: a literal option name the format whitelists is not a credential", func(t *testing.T) {
		ts := []formats.SetupTemplate{
			{ID: "tf.rc.configure", Client: "tf", Section: "configure", Lang: "ini", Template: "legacy_auth_mode=1\n"},
			{ID: "tf.install", Client: "tf", Section: "install", Lang: "shell", Template: "tf install\n"},
			// The guide of the test format attaches the publish snippets.
			{ID: "tf.publish", Client: "tf", Section: "publish", Lang: "shell", Template: "tf publish\n"},
		}
		o := override{Format: testformat.New(), templates: ts}
		if got := messages(formattest.Evaluate(o, fx), formattest.CheckSetup); !strings.Contains(got, "spells a credential") {
			t.Fatalf("not refused: %q", got)
		}
		fx2 := fx
		fx2.OptionNames = []string{"legacy_auth_mode"}
		if got := messages(formattest.Evaluate(o, fx2), formattest.CheckSetup); got != "" {
			t.Fatalf("refused despite the whitelist: %q", got)
		}
	})
	t.Run("R-ARCH-010 check 8: every spelling of the token reference is no credential, with or without the whitelist", func(t *testing.T) {
		ts := []formats.SetupTemplate{
			{ID: "tf.win.configure", Client: "tf", Section: "configure", Lang: "xml", Template: `<add key="p" value="` + formats.SetupTokenRefWin + `" />` + "\n"},
			{ID: "tf.ps.configure", Client: "tf", Section: "configure", Lang: "powershell", Template: "$h = " + formats.SetupTokenRefPS + "\n"},
			{ID: "tf.cmd.configure", Client: "tf", Section: "configure", Lang: "cmd", Template: "set X=" + formats.SetupTokenRefWin + "\n"},
			{ID: "tf.install", Client: "tf", Section: "install", Lang: "shell", Template: "tf install " + formats.SetupPackageRef + "\n", VersionSpec: " --version " + formats.SetupVersionRef},
			{ID: "tf.publish", Client: "tf", Section: "publish", Lang: "shell", Template: "tf publish\n"},
		}
		o := override{Format: testformat.New(), templates: ts}
		fx2 := fx
		fx2.OptionNames = nil
		if got := messages(formattest.Evaluate(o, fx2), formattest.CheckSetup); got != "" {
			t.Fatalf("refused: %q", got)
		}
	})
	t.Run("R-ARCH-010 check 8: a VersionSpec that is misplaced fails", func(t *testing.T) {
		install := formats.SetupTemplate{ID: "tf.install", Client: "tf", Section: "install", Lang: "shell", Template: "tf install " + formats.SetupPackageRef + "\n"}
		cases := map[string][]formats.SetupTemplate{
			"is not an install template": {
				{ID: "tf.rc.configure", Client: "tf", Section: "configure", Lang: "ini", Template: "x\n", VersionSpec: " --version " + formats.SetupVersionRef}, install,
			},
			"does not place " + formats.SetupPackageRef: {
				{ID: "tf.rc.configure", Client: "tf", Section: "configure", Lang: "ini", Template: "x\n"},
				{ID: "tf.install", Client: "tf", Section: "install", Lang: "shell", Template: "tf install\n", VersionSpec: " --version " + formats.SetupVersionRef},
			},
			"does not place " + formats.SetupVersionRef: {
				{ID: "tf.rc.configure", Client: "tf", Section: "configure", Lang: "ini", Template: "x\n"},
				{ID: "tf.install", Client: "tf", Section: "install", Lang: "shell", Template: install.Template, VersionSpec: " --version"},
			},
		}
		for want, ts := range cases {
			got := messages(formattest.Evaluate(override{Format: testformat.New(), templates: ts}, fx), formattest.CheckSetup)
			if !strings.Contains(got, want) {
				t.Errorf("%q does not say %q", got, want)
			}
		}
	})
	t.Run("R-ARCH-010 R-UI-049 check 8: a guide section that breaks a rule of the guide fails", func(t *testing.T) {
		with := func(change func(*formats.GuideSection)) []formats.GuideSection {
			out := append([]formats.GuideSection(nil), testformat.New().Guide()...)
			change(&out[0])
			return out
		}
		extra := func(s formats.GuideSection) []formats.GuideSection {
			return append(append([]formats.GuideSection(nil), testformat.New().Guide()...), s)
		}
		cases := map[string][]formats.GuideSection{
			"does not execute over GuideData": with(func(s *formats.GuideSection) { s.Markdown = "{{.Token}}" }),
			`has topic "faq"`:                 with(func(s *formats.GuideSection) { s.Topic = "faq" }),
			"has no title":                    with(func(s *formats.GuideSection) { s.Title = " " }),
			"is used twice":                   extra(formats.GuideSection{ID: "tf.what", Topic: formats.GuideWhat, Title: "x", Markdown: "x"}),
			"which the descriptor does not list": extra(formats.GuideSection{
				ID: "x1", Topic: formats.GuideWhat, Title: "x", Clients: []string{"other"}, Markdown: "x",
			}),
			"which the format does not support": extra(formats.GuideSection{
				ID: "x2", Topic: formats.GuideWhat, Title: "x", Classes: []formats.Class{"federated"}, Markdown: "x",
			}),
			`attaches the snippets of section "deploy"`: with(func(s *formats.GuideSection) { s.Snippets = "deploy" }),
			"holds the shape of a token":                with(func(s *formats.GuideSection) { s.Markdown = "Use trv_pat_x." }),
			`no guide section of topic "configure" attaches the configure snippets`: func() []formats.GuideSection {
				out := append([]formats.GuideSection(nil), testformat.New().Guide()...)
				out[1].Snippets = ""
				return out
			}(),
			`of the required topic "authenticate" says nothing`: func() []formats.GuideSection {
				out := append([]formats.GuideSection(nil), testformat.New().Guide()...)
				out[2].Markdown = "{{if .Anonymous}}{{else}}Keep it.{{end}}"
				return out
			}(),
		}
		for want, guide := range cases {
			got := messages(formattest.Evaluate(override{Format: testformat.New(), guide: guide}, fx), formattest.CheckSetup)
			if !strings.Contains(got, want) {
				t.Errorf("%q does not say %q", got, want)
			}
		}
	})
	t.Run("R-ARCH-010 R-UI-049 check 8: a format with a guide and no clients fails, and its skip is refused", func(t *testing.T) {
		if got := messages(formattest.Evaluate(guided{}, formattest.Fixture{}), formattest.CheckSetup); !strings.Contains(got, "a guide and no clients") {
			t.Fatalf("not refused: %q", got)
		}
		rep := formattest.Evaluate(guided{}, formattest.Fixture{Skip: map[formattest.Check]string{formattest.CheckSetup: "no clients"}})
		if got := messages(rep, formattest.CheckSetup); !strings.Contains(got, "or a guide") {
			t.Fatalf("skip accepted: %q", got)
		}
	})
	t.Run("R-ARCH-010 check 4: a rewrite that changes its input or is not idempotent fails", func(t *testing.T) {
		good := testformat.New().Remote()
		cases := map[string]rewriteFunc{
			"changed its input": func(doc []byte, ln formats.LinkBuilder, ctx context.Context, rc formats.RemoteContext, p formats.Path) ([]byte, error) {
				out, err := good.Rewrite(ctx, rc, p, doc, ln)
				if i := bytes.Index(doc, []byte("left")); i >= 0 {
					doc[i] = 'L'
				}
				return out, err
			},
			"not idempotent": func(doc []byte, ln formats.LinkBuilder, ctx context.Context, rc formats.RemoteContext, p formats.Path) ([]byte, error) {
				out, err := good.Rewrite(ctx, rc, p, doc, ln)
				return append(out, bytes.Repeat([]byte(" "), len(doc))...), err
			},
		}
		for want, fn := range cases {
			o := override{Format: testformat.New(), remote: rewriter{good, fn}}
			if got := messages(formattest.Evaluate(o, fx), formattest.CheckRewrite); !strings.Contains(got, want) {
				t.Errorf("%q does not say %q", got, want)
			}
		}
	})
	t.Run("R-ARCH-010 check 4: a fixture without an upstream link proves nothing", func(t *testing.T) {
		fx2 := fx
		r := *fx.Remote
		r.DownloadLinks = func([]byte) []string { return nil }
		fx2.Remote = &r
		if got := messages(formattest.Evaluate(testformat.New(), fx2), formattest.CheckRewrite); !strings.Contains(got, "proves nothing") {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("R-ARCH-010 check 7: an unexpected status and a failing seed are reported", func(t *testing.T) {
		fx2 := fx
		fx2.Content = append([]formattest.ContentCase(nil), fx.Content...)
		fx2.Content[0] = formattest.ContentCase{Name: "missing document", Path: "left-pad", Status: 200}
		fx2.Content[1].Seed = func(*formattest.Host) error { return context.Canceled }
		got := messages(formattest.Evaluate(testformat.New(), fx2), formattest.CheckHeaders)
		if !strings.Contains(got, "status 404, want 200") || !strings.Contains(got, "the seed fails") {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("R-ARCH-010 check 7: a route that matches no case path is reported with the format's error", func(t *testing.T) {
		fx2 := fx
		fx2.Content = append([]formattest.ContentCase{{Name: "no route", Path: "a/b/c/d"}}, fx.Content...)
		if got := messages(formattest.Evaluate(testformat.New(), fx2), formattest.CheckHeaders); !strings.Contains(got, `content case "no route"`) {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("R-ARCH-009 check 9: a change that fails, a case without a package, and a NoRevision that names no route", func(t *testing.T) {
		fx2 := fx
		fx2.Changes = []formattest.ChangeCase{
			{Name: "fails", Package: "left-pad", Do: func(context.Context, *formattest.Host) error { return context.Canceled }},
			{Name: "no package", Do: func(context.Context, *formattest.Host) error { return nil }},
		}
		fx2.NoRevision = map[string]string{"tf.nope": "reason", "tf.tag.put": ""}
		got := messages(formattest.Evaluate(testformat.New(), fx2), formattest.CheckRevision)
		for _, want := range []string{`change "fails" fails`, "needs a Package and a Do", "which the format does not have", "without a reason"} {
			if !strings.Contains(got, want) {
				t.Errorf("%q does not say %q", got, want)
			}
		}
	})
	t.Run("R-ARCH-009 check 9: a route that changes no package row is listed in NoRevision with a reason", func(t *testing.T) {
		fx2 := fx
		var kept []formattest.ChangeCase
		for _, c := range fx.Changes {
			if c.Name != "tag change (Admin route)" {
				kept = append(kept, c)
			}
		}
		fx2.Changes = kept
		if got := messages(formattest.Evaluate(testformat.New(), fx2), formattest.CheckRevision); !strings.Contains(got, `route "tf.tag.put" (admin) has no change case`) {
			t.Fatalf("got %q", got)
		}
		fx2.NoRevision = map[string]string{"tf.tag.put": "the tag is a row of the format's own table"}
		if got := messages(formattest.Evaluate(testformat.New(), fx2), formattest.CheckRevision); got != "" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("R-ARCH-010 check 10: a name the format rejects and a spelling that normalizes differently fail", func(t *testing.T) {
		fx2 := fx
		fx2.Names = []formattest.NameCase{{Spellings: []string{"Not A Name"}}, {Spellings: []string{"left-pad", "right-pad"}}, {}}
		got := messages(formattest.Evaluate(testformat.New(), fx2), formattest.CheckNames)
		for _, want := range []string{"is no package name", "normalize to", "has no spellings"} {
			if !strings.Contains(got, want) {
				t.Errorf("%q does not say %q", got, want)
			}
		}
	})
	t.Run("R-ARCH-010 check 3: a path case that is wrong, and a path that is no path, fail", func(t *testing.T) {
		fx2 := fx
		fx2.Paths = []formattest.PathCase{{Path: "left-pad", Want: formats.PathInfo{Kind: formats.Content}}, {Path: "../x"}}
		got := messages(formattest.Evaluate(testformat.New(), fx2), formattest.CheckClassify)
		if !strings.Contains(got, `Classify("left-pad")`) || !strings.Contains(got, "is no clean path") {
			t.Fatalf("got %q", got)
		}
	})
}

func TestR_ARCH_028_Check6Bounds(t *testing.T) {
	fx := testformat.Fixture()
	fx.Inspect = fx.Inspect[:1]
	t.Run("R-ARCH-028 an Inspect that does not return within five seconds fails, and the kit cancels it", func(t *testing.T) {
		o := override{Format: testformat.New(), inspect: func(ctx context.Context, _ formats.InspectInput) (*formats.Inspection, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}}
		got := messages(formattest.Evaluate(o, fx), formattest.CheckInspect)
		// The bound is five seconds, stretched in a build under the race detector (formattest.TimeBound).
		secs := int(formattest.TimeBound() / time.Second)
		if !strings.Contains(got, fmt.Sprintf("took %ds", secs)) && !strings.Contains(got, fmt.Sprintf("took %d.", secs)) {
			t.Fatalf("got %q, want an Inspect that took about %v", got, formattest.TimeBound())
		}
	})
	t.Run("R-ARCH-028 an Inspect that panics is a failure of the check, not of the run", func(t *testing.T) {
		o := override{Format: testformat.New(), inspect: func(context.Context, formats.InspectInput) (*formats.Inspection, error) { panic("boom") }}
		if got := messages(formattest.Evaluate(o, fx), formattest.CheckInspect); !strings.Contains(got, "panicked: boom") {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("R-ARCH-028 a crafted input is a stream and a spool: size, digests, random access only where the archive has it", func(t *testing.T) {
		sp := formattest.SpoolOf(crafted.TarGzEntry("x", 1<<30))
		if sp.Size() == 0 || len(sp.Digests().SHA256) != 0 {
			t.Fatal("size or digests")
		}
		if _, err := sp.ReaderAt(); err == nil {
			t.Fatal("a stream has random access")
		}
		rc, err := sp.Open()
		if err != nil {
			t.Fatal(err)
		}
		_ = rc.Close()
		if _, err := formattest.SpoolOf(crafted.ZipEntries(3)).ReaderAt(); err != nil {
			t.Fatal(err)
		}
	})
}

// rewriteFunc replaces Rewrite.
type rewriteFunc func(doc []byte, ln formats.LinkBuilder, ctx context.Context, rc formats.RemoteContext, p formats.Path) ([]byte, error)

type rewriter struct {
	formats.RemoteAdapter
	fn rewriteFunc
}

func (r rewriter) Rewrite(ctx context.Context, rc formats.RemoteContext, p formats.Path, doc []byte, ln formats.LinkBuilder) ([]byte, error) {
	return r.fn(doc, ln, ctx, rc, p)
}

func TestR_ARCH_010_CleanMethodCoverage(t *testing.T) {
	// the HEAD request of the content cases is served by the GET route, like the
	// dispatcher does
	h := formattest.NewHost(testformat.New(), formattest.HostOptions{Data: &testformat.MemStore{}})
	if err := testformat.Publish(context.Background(), h, "left-pad", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	resp, err := h.Serve(context.Background(), http.MethodHead, "left-pad", nil, false)
	if err != nil || resp.Code != http.StatusOK {
		t.Fatalf("%v %v", resp, err)
	}
	if got := h.RoutesServed(); len(got) != 2 || got[1] != "tf.doc.get" {
		t.Fatalf("routes served = %v", got)
	}
}

// TestQ_ARCH_281_AContentRouteThatOnlyRefusesIsListed: check 7 checks the content headers on what a
// route serves, so a route whose content cases all expect a refusal is listed in NoContent with the
// reason, and an entry the fixture contradicts fails.
func TestQ_ARCH_281_AContentRouteThatOnlyRefusesIsListed(t *testing.T) {
	fx := testformat.Fixture()
	var cases []formattest.ContentCase
	for _, c := range fx.Content {
		if c.Path != "left-pad" && c.Path != "@scope%2Fname" {
			cases = append(cases, c)
		}
	}
	cases = append(cases, formattest.ContentCase{Name: "an unknown package", Path: "no-such-package", Status: http.StatusNotFound})
	fx.Content = cases
	check7 := func(noContent map[string]string) string {
		f := fx
		f.NoContent = noContent
		return messages(formattest.Evaluate(testformat.New(), f), formattest.CheckHeaders)
	}
	for name, c := range map[string]struct {
		noContent map[string]string
		want      string
	}{
		"R-ARCH-010 a route whose cases only refuse fails":             {nil, "only content cases that refuse"},
		"R-ARCH-010 a listed route without a reason fails":             {map[string]string{"tf.doc.get": ""}, "without a reason"},
		"R-ARCH-010 a listed route that a case serves fails":           {map[string]string{"tf.doc.get": "x", "tf.tarball.get": "x"}, "a content case serves it"},
		"R-ARCH-010 an entry for a route that serves no content fails": {map[string]string{"tf.doc.get": "x", "tf.tag.put": "x"}, "NoContent names"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := check7(c.noContent); !strings.Contains(got, c.want) {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
	t.Run("R-ARCH-010 a listed route with its reason passes", func(t *testing.T) {
		if got := check7(map[string]string{"tf.doc.get": "the format answers 404 for it"}); got != "" {
			t.Fatalf("got %q", got)
		}
	})
}

// TestQ_ARCH_281_EveryStorageHookIsDrivenByAChange: check 9 asks a change case of every storage hook
// the format has, the restore hook of formats.ItemsRestorer included, or a reasoned NoRevision entry.
func TestQ_ARCH_281_EveryStorageHookIsDrivenByAChange(t *testing.T) {
	fx := testformat.Fixture()
	var changes []formattest.ChangeCase
	for _, c := range fx.Changes {
		if !strings.Contains(c.Name, "OnItemsRestored") {
			changes = append(changes, c)
		}
	}
	fx.Changes = changes
	check9 := func() string { return messages(formattest.Evaluate(testformat.New(), fx), formattest.CheckRevision) }
	if got := check9(); !strings.Contains(got, "storage hook OnItemsRestored ran in no change case") {
		t.Fatalf("got %q", got)
	}
	fx.NoRevision = map[string]string{"OnItemsRestored": "the trash can's restore is not built"}
	if got := check9(); got != "" {
		t.Fatalf("a reasoned NoRevision entry for the hook is refused: %q", got)
	}
}

// fillInspecting gives a remote adapter the fill hook of formats.FillInspector.
type fillInspecting struct {
	formats.RemoteAdapter
	fill func(ctx context.Context) (*formats.Inspection, error)
}

func (fillInspecting) InspectsFill(formats.Path) bool { return true }

func (f fillInspecting) InspectFill(ctx context.Context, _ formats.RemoteContext, _ formats.Path, _ formats.Spooled) (*formats.Inspection, error) {
	return f.fill(ctx)
}

// TestQ_ARCH_281_TheFillHookMeetsTheBoundsOfCheck6: a remote adapter that inspects fills runs the
// crafted inputs of check 6 through its hook; what it answers is its own, and a panic fails the check.
func TestQ_ARCH_281_TheFillHookMeetsTheBoundsOfCheck6(t *testing.T) {
	fx := testformat.Fixture()
	fx.Inspect = fx.Inspect[:1]
	withFill := func(fill func(context.Context) (*formats.Inspection, error)) override {
		return override{Format: testformat.New(), remote: fillInspecting{RemoteAdapter: testformat.New().Remote(), fill: fill}}
	}
	t.Run("R-ARCH-028 a fill hook that answers in time passes, whatever it answers", func(t *testing.T) {
		o := withFill(func(context.Context) (*formats.Inspection, error) { return nil, nil })
		if got := messages(formattest.Evaluate(o, fx), formattest.CheckInspect); got != "" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("R-ARCH-028 a fill hook that panics fails check 6", func(t *testing.T) {
		o := withFill(func(context.Context) (*formats.Inspection, error) { panic("boom") })
		if got := messages(formattest.Evaluate(o, fx), formattest.CheckInspect); !strings.Contains(got, "InspectFill panicked: boom") {
			t.Fatalf("got %q", got)
		}
	})
}
