//go:build chart

package chart_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestImageReference pins the default image of the chart (the release pipeline publishes
// guidedtraffic/trove on Docker Hub; docs/developer/releasing.md) and the one line of values.yaml that the release pipeline
// rewrites (build.yml, job release-helm-gh, sed on the image tag).
func TestImageReference(t *testing.T) {
	t.Run("the default image is docker.io/guidedtraffic/trove at the chart's appVersion", func(t *testing.T) {
		raw, err := os.ReadFile(filepath.Join(chartDir, "Chart.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		appVersion := ""
		for _, line := range strings.Split(string(raw), "\n") {
			if v, ok := strings.CutPrefix(line, "appVersion: "); ok {
				appVersion = strings.Trim(v, `"' `)
			}
		}
		if appVersion == "" {
			t.Fatal("Chart.yaml names no appVersion")
		}
		want := "docker.io/guidedtraffic/trove:" + appVersion
		_, docs := render(t, required())
		pods := podSpecs(docs)
		if len(pods) == 0 {
			t.Fatal("the chart renders no Deployment or Job")
		}
		for name, pod := range pods {
			if got := str(pod, "containers", "0", "image"); got != want {
				t.Errorf("%s runs image %q, want %q", name, got, want)
			}
		}
	})

	t.Run("R-HA-028 the release pipeline's sed finds exactly one image tag line in values.yaml", func(t *testing.T) {
		raw, err := os.ReadFile(filepath.Join(chartDir, "values.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(line, "  tag:") {
				n++
			}
		}
		if n != 1 {
			t.Errorf("values.yaml has %d lines starting with two spaces and \"tag:\", want 1 (the image tag, which build.yml rewrites)", n)
		}
	})
}
