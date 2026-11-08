//go:build chart

package chart_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// buildWorkflow is .github/workflows/build.yml, the release pipeline of R-HA-047.
type buildWorkflow struct {
	On          map[string]any    `yaml:"on"`
	Permissions map[string]string `yaml:"permissions"`
	Jobs        map[string]struct {
		If          string            `yaml:"if"`
		RunsOn      string            `yaml:"runs-on"`
		Needs       yaml.Node         `yaml:"needs"`
		Permissions map[string]string `yaml:"permissions"`
		Steps       []struct {
			Name string            `yaml:"name"`
			ID   string            `yaml:"id"`
			If   string            `yaml:"if"`
			Run  string            `yaml:"run"`
			With map[string]string `yaml:"with"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func loadBuildWorkflow(t *testing.T) buildWorkflow {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot, ".github", "workflows", "build.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var w buildWorkflow
	if err := yaml.Unmarshal(raw, &w); err != nil {
		t.Fatal(err)
	}
	return w
}

// TestR_HA_047_ADispatchRunsOnlyThePostReleaseCheck guards the gates of Q-HA-164: the publish jobs run on release events
// only, so that a dispatch (which names a published version) never rebuilds the image, never pushes a tag and never rewrites
// the gh-pages branch, and the check job runs after both of them on a release and alone on a dispatch.
func TestR_HA_047_ADispatchRunsOnlyThePostReleaseCheck(t *testing.T) {
	w := loadBuildWorkflow(t)

	t.Run("a dispatch names the version to check", func(t *testing.T) {
		d, ok := w.On["workflow_dispatch"].(map[string]any)
		if !ok {
			t.Fatal("build.yml has no workflow_dispatch trigger")
		}
		inputs, _ := d["inputs"].(map[string]any)
		v, ok := inputs["version"].(map[string]any)
		if !ok {
			t.Fatal("workflow_dispatch has no input version")
		}
		if v["required"] != true {
			t.Errorf("the input version must be required, got %v", v["required"])
		}
		if _, ok := w.On["release"]; !ok {
			t.Error("build.yml no longer runs on release events")
		}
	})

	t.Run("the publish jobs run on release events only", func(t *testing.T) {
		for _, name := range []string{"build", "release-helm-gh"} {
			job, ok := w.Jobs[name]
			if !ok {
				t.Fatalf("build.yml has no job %s", name)
			}
			if job.If != "github.event_name == 'release'" {
				t.Errorf("job %s has if %q, want \"github.event_name == 'release'\": without it a dispatch would publish", name, job.If)
			}
		}
	})

	t.Run("the check runs after both publish jobs on a release and alone on a dispatch", func(t *testing.T) {
		job, ok := w.Jobs["smoke-published"]
		if !ok {
			t.Fatal("build.yml has no job smoke-published")
		}
		var needs []string
		if err := job.Needs.Decode(&needs); err != nil {
			t.Fatal(err)
		}
		if strings.Join(needs, ",") != "build,release-helm-gh" {
			t.Errorf("smoke-published needs %v, want [build release-helm-gh]", needs)
		}
		// !cancelled(), not always(): a cancelled run stops the check, and the skipped publish jobs of a dispatch still let it run.
		const wantIf = "${{ !cancelled() && (github.event_name == 'workflow_dispatch' || (needs.build.result == 'success' && needs.release-helm-gh.result == 'success')) }}"
		if job.If != wantIf {
			t.Errorf("the condition of smoke-published is\n%s\nwant\n%s", job.If, wantIf)
		}
		if job.RunsOn != "large" {
			t.Errorf("smoke-published runs on %q; the kind legs run on large (docs/developer/releasing.md#ci)", job.RunsOn)
		}
	})

	t.Run("no command of any job takes the dispatch input or the event text", func(t *testing.T) {
		injection := regexp.MustCompile(`\$\{\{\s*(inputs|github\.event|github\.head_ref)\b`)
		for name, job := range w.Jobs {
			for i, s := range job.Steps {
				if m := injection.FindString(s.Run); m != "" {
					t.Errorf("job %s step %d (%s) puts %s into a command: pass it through env and quote it", name, i, s.Name, m)
				}
			}
		}
	})

	t.Run("the guard runs after a successful install, also after red scenarios", func(t *testing.T) {
		found := false
		for _, s := range w.Jobs["smoke-published"].Steps {
			if strings.Contains(s.Run, "make release-smoke-guard") {
				found = true
				if want := "${{ !cancelled() && steps.install.outcome == 'success' }}"; s.If != want {
					t.Errorf("the guard step has if %q, want %q", s.If, want)
				}
			}
		}
		if !found {
			t.Error("smoke-published has no step that runs the guard")
		}
	})
}

// TestR_HA_047_TheTokensHoldWhatTheJobsUse guards the permissions of build.yml (docs/security/data-and-secret-flow.md): the
// workflow reads by default, the image job writes contents only, the chart job writes contents and pages, and the check reads.
func TestR_HA_047_TheTokensHoldWhatTheJobsUse(t *testing.T) {
	w := loadBuildWorkflow(t)
	if len(w.Permissions) != 1 || w.Permissions["contents"] != "read" {
		t.Errorf("the workflow permissions are %v, want only contents: read", w.Permissions)
	}
	for name, want := range map[string]map[string]string{
		"build":           {"contents": "write"},
		"release-helm-gh": {"contents": "write", "pages": "write"}, // pages stays: dropping it gains nothing (Q-HA-164)
		"smoke-published": {"contents": "read"},
	} {
		got := w.Jobs[name].Permissions
		if len(got) != len(want) {
			t.Errorf("job %s permissions are %v, want %v", name, got, want)
			continue
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("job %s permissions are %v, want %v", name, got, want)
			}
		}
	}
	raw, err := os.ReadFile(filepath.Join(repoRoot, ".github", "workflows", "build.yml"))
	if err != nil {
		t.Fatal(err)
	}
	// The release builder takes no network option: host would put every RUN step into the runner's network namespace, and a
	// user-defined network gives it Docker's embedded resolver at 127.0.0.11, which the RUN steps of BuildKit cannot use (release
	// v0.25.1 failed on `go mod download`, Q-HA-166).
	if m := regexp.MustCompile(`(?m)^[^#\n]*network=\S+`).FindString(string(raw)); m != "" {
		t.Errorf("build.yml sets a network for the builder (%s): the builder stays on the default bridge network", strings.TrimSpace(m))
	}
	if !regexp.MustCompile(`(?m)^\s*\[registry\."\$\{\{ steps\.sbom_net\.outputs\.host \}\}"\]\n\s*http = true`).Match(raw) {
		t.Error("build.yml does not mark the address of the generator registry as plain HTTP for the builder")
	}
}

// TestR_HA_047_TheImageAttestationUsesThePinnedSyft guards that the SBOM attestation comes from the generator of
// tools/sbom-generator (the pinned syft, Q-HA-166) and not from BuildKit's bundled scanner.
func TestR_HA_047_TheImageAttestationUsesThePinnedSyft(t *testing.T) {
	w := loadBuildWorkflow(t)
	found := false
	for _, s := range w.Jobs["build"].Steps {
		if s.ID != "build" {
			continue
		}
		found = true
		if s.With["sbom"] != "generator=${{ steps.sbom_generator.outputs.image }}" {
			t.Errorf("the build step sets sbom to %q, want the generator of the step sbom_generator", s.With["sbom"])
		}
		if s.With["provenance"] != "true" {
			t.Errorf("the build step sets provenance to %q, want true", s.With["provenance"])
		}
	}
	if !found {
		t.Fatal("the job build has no step with id build")
	}
}

// TestR_HA_047_TheIndexDigestOfAChartVersion runs deploy/dev/index-digest.awk, which the post-release check uses to read the
// digest that the chart repository lists for the package, on a copy of the published index (two charts, two versions each,
// the digest before the name and the version as helm writes them).
func TestR_HA_047_TheIndexDigestOfAChartVersion(t *testing.T) {
	index := filepath.Join(repoRoot, "test", "chart", "testdata", "release-index.yaml")
	script := filepath.Join(repoRoot, "deploy", "dev", "index-digest.awk")
	digests := func(chart, version string) []string {
		out, err := exec.Command("awk", "-v", "chart="+chart, "-v", "want="+version, "-f", script, index).Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.Fields(string(out))
	}
	for _, c := range []struct {
		chart, version string
		want           []string
	}{
		{"trove", "0.25.0", []string{"e3a1f5c514708ccb6a8f4df29375460511e4d7222cf8a98da77589cfa8a9459e"}},
		{"trove", "0.24.0", []string{"3254dc20e8a668ed0c2176fc7bc8cd14daa8646d54e4dd99e651cc0caa592a9b"}},
		// the Bastion chart has the same versions and other packages: the chart name decides
		{"trove-bastion", "0.25.0", []string{"838c880025c560c246cba46e26b725465a994314cbf01c861ec6530faa9d6aaf"}},
		// a version that is not listed gives nothing, and a prefix of a listed one is not that version
		{"trove", "9.9.9", nil},
		{"trove", "0.25", nil},
		{"trove", "0.25.00", nil},
	} {
		got := digests(c.chart, c.version)
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s %s: digests %v, want %v", c.chart, c.version, got, c.want)
		}
	}
}
