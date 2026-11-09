//go:build chart

package chart_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// profileRow is one row of the sizing table of R-HA-050 in docs/spec/ha-and-helm.md, cell by cell.
func profileRow(t *testing.T, name string) []string {
	t.Helper()
	spec, err := os.ReadFile(filepath.Join(repoRoot, "docs", "spec", "ha-and-helm.md"))
	if err != nil {
		t.Fatal(err)
	}
	inSection := false
	for _, l := range strings.Split(string(spec), "\n") {
		switch {
		case strings.HasPrefix(l, "### R-HA-050 "):
			inSection = true
		case strings.HasPrefix(l, "### R-HA-051 "):
			inSection = false
		case inSection && strings.HasPrefix(l, "| "+name+" |"):
			var cells []string
			for _, c := range strings.Split(strings.Trim(l, "| "), "|") {
				cells = append(cells, strings.TrimSpace(c))
			}
			return cells
		}
	}
	t.Fatalf("the sizing table of R-HA-050 has no row %q", name)
	return nil
}

// replicasCell reads "3 (no HPA)" and "4 (4–8)": the replica count and, with an HPA, its bounds.
func replicasCell(t *testing.T, cell string) (replicas int, hpa bool, min, max int) {
	t.Helper()
	m := regexp.MustCompile(`^(\d+) \((?:no HPA|(\d+)–(\d+))\)$`).FindStringSubmatch(cell)
	if m == nil {
		t.Fatalf("replica cell %q is neither 'N (no HPA)' nor 'N (min–max)'", cell)
	}
	replicas, _ = strconv.Atoi(m[1])
	if m[2] != "" {
		min, _ = strconv.Atoi(m[2])
		max, _ = strconv.Atoi(m[3])
		hpa = true
	}
	return
}

func quantity(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return ""
}

// TestR_HA_050_SizingProfiles (R-HA-050, R-HA-029): each profile file renders on top of the operator's minimum values, the
// rendered Deployments, autoscalers and database pool carry the numbers of the table in the specification, and the large
// profile's spool is an ephemeral claim.
func TestR_HA_050_SizingProfiles(t *testing.T) {
	operator := filepath.Join(chartDir, "..", "..", "..", "test", "chart", "testdata", "operator-minimum.yaml")
	for _, name := range []string{"small", "medium", "large"} {
		t.Run("R-HA-050 the profile "+name+" renders and carries the numbers of the table", func(t *testing.T) {
			profile := filepath.Join(chartDir, "profiles", name+".yaml")
			row := profileRow(t, name)
			if len(row) != 12 {
				t.Fatalf("the row has %d cells, want 12: %v", len(row), row)
			}
			_, docs := render(t, []string{profile, operator})

			pkgReplicas, pkgHPA, pkgMin, pkgMax := replicasCell(t, row[2])
			workerReplicas, workerHPA, workerMin, workerMax := replicasCell(t, row[3])
			for _, c := range []struct {
				deployment                string
				replicas, min, max        int
				hpa                       bool
				cpu, memory, limit, spool string
			}{
				{"trove-packages", pkgReplicas, pkgMin, pkgMax, pkgHPA, "", "", "", ""},
				{"trove-worker", workerReplicas, workerMin, workerMax, workerHPA, "", "", "", ""},
			} {
				dep := find(docs, "Deployment", c.deployment)
				if dep == nil {
					t.Fatalf("no Deployment %s", c.deployment)
				}
				hpa := find(docs, "HorizontalPodAutoscaler", c.deployment)
				switch {
				case c.hpa && hpa == nil:
					t.Errorf("%s has no HorizontalPodAutoscaler, the table says %d–%d", c.deployment, c.min, c.max)
				case c.hpa:
					if got := at(hpa, "spec", "minReplicas"); got != c.min {
						t.Errorf("%s minReplicas = %v, want %d", c.deployment, got, c.min)
					}
					if got := at(hpa, "spec", "maxReplicas"); got != c.max {
						t.Errorf("%s maxReplicas = %v, want %d", c.deployment, got, c.max)
					}
				case hpa != nil:
					t.Errorf("%s has an autoscaler, the table says no HPA", c.deployment)
				default:
					if got := at(dep, "spec", "replicas"); got != c.replicas {
						t.Errorf("%s replicas = %v, want %d", c.deployment, got, c.replicas)
					}
				}
			}

			// The packages Deployment holds the web columns: requests CPU / memory, the memory limit, the spool.
			pod := at(find(docs, "Deployment", "trove-packages"), "spec", "template", "spec")
			res := at(pod, "containers", "0", "resources")
			wantReq := strings.Split(row[4], " / ")
			if len(wantReq) != 2 {
				t.Fatalf("requests cell %q is not 'CPU / memory'", row[4])
			}
			if got := quantity(at(res, "requests", "cpu")); got != wantReq[0] {
				t.Errorf("packages CPU request = %q, want %q", got, wantReq[0])
			}
			if got := quantity(at(res, "requests", "memory")); got != wantReq[1] {
				t.Errorf("packages memory request = %q, want %q", got, wantReq[1])
			}
			if got := quantity(at(res, "limits", "memory")); got != row[5] {
				t.Errorf("packages memory limit = %q, want %q", got, row[5])
			}
			cfg := configOf(t, docs)
			if got := quantity(at(cfg, "database", "pool", "max_conns")); got != row[6] {
				t.Errorf("config.database.pool.max_conns = %q, want %q", got, row[6])
			}
			wantSpool := strings.Fields(row[7])[0]
			if strings.Contains(row[7], "ephemeral") {
				var claim any
				for _, v := range list(pod, "volumes") {
					if str(v, "name") == "spool" {
						claim = at(v, "ephemeral", "volumeClaimTemplate", "spec", "resources", "requests", "storage")
					}
				}
				if quantity(claim) != wantSpool {
					t.Errorf("the spool claim of a packages pod is %v, want an ephemeral claim of %s", claim, wantSpool)
				}
			} else {
				var limit any
				for _, v := range list(pod, "volumes") {
					if str(v, "name") == "spool" {
						limit = at(v, "emptyDir", "sizeLimit")
					}
				}
				if quantity(limit) != wantSpool {
					t.Errorf("the spool of a packages pod is %v, want an emptyDir of %s", limit, wantSpool)
				}
			}

			// After the cut into roles every profile adds api: 2 replicas (an HPA of 2 to 4 in medium and large), 500m / 512Mi, 1Gi, 5Gi.
			api := find(docs, "Deployment", "trove-api")
			apiRes := at(api, "spec", "template", "spec", "containers", "0", "resources")
			if quantity(at(apiRes, "requests", "cpu")) != "500m" || quantity(at(apiRes, "requests", "memory")) != "512Mi" || quantity(at(apiRes, "limits", "memory")) != "1Gi" {
				t.Errorf("api resources = %v, want 500m / 512Mi and a 1Gi limit", apiRes)
			}
			apiHPA := find(docs, "HorizontalPodAutoscaler", "trove-api")
			if (name == "small") != (apiHPA == nil) {
				t.Errorf("api autoscaler present = %v in the profile %s", apiHPA != nil, name)
			}
			if apiHPA != nil && (at(apiHPA, "spec", "minReplicas") != 2 || at(apiHPA, "spec", "maxReplicas") != 4) {
				t.Errorf("api autoscaler = %v, want 2 to 4", at(apiHPA, "spec"))
			}
			if apiHPA == nil && at(api, "spec", "replicas") != 2 {
				t.Errorf("api replicas = %v, want 2", at(api, "spec", "replicas"))
			}
		})
	}

	t.Run("R-HA-050 a profile layered before the operator's file leaves the operator's values the last word", func(t *testing.T) {
		_, docs := render(t, []string{filepath.Join(chartDir, "profiles", "medium.yaml"), filepath.Join(chartDir, "ci", "required-only.yaml")}, "packages.replicaCount=5", "packages.autoscaling.enabled=false")
		if got := at(find(docs, "Deployment", "trove-packages"), "spec", "replicas"); got != 5 {
			t.Errorf("packages replicas = %v, want the operator's 5", got)
		}
		if find(docs, "HorizontalPodAutoscaler", "trove-packages") != nil {
			t.Error("the autoscaler of the profile survived the operator switching it off")
		}
	})
}
