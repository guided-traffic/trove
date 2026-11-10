//go:build chart

package chart_test

import (
	"os"
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

// TestR_HA_047_BuildRunsOnReleasesAndNoCommandTakesEventText: build.yml publishes on release events only, and no command of
// a job takes the event's text, which a branch or a tag name could turn into a command.
func TestR_HA_047_BuildRunsOnReleasesAndNoCommandTakesEventText(t *testing.T) {
	w := loadBuildWorkflow(t)
	if _, ok := w.On["release"]; !ok || len(w.On) != 1 {
		t.Errorf("build.yml runs on %v, want release events only", w.On)
	}
	injection := regexp.MustCompile(`\$\{\{\s*(inputs|github\.event|github\.head_ref)\b`)
	for name, job := range w.Jobs {
		for i, s := range job.Steps {
			if m := injection.FindString(s.Run); m != "" {
				t.Errorf("job %s step %d (%s) puts %s into a command: pass it through env and quote it", name, i, s.Name, m)
			}
		}
	}
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
