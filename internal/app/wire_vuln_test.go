package app

import (
	"testing"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/vuln"
	vmetrics "github.com/guided-traffic/trove/internal/vuln/metrics"
)

// R-VULN-025 (contract test): the task a scanning part registers takes the specification's schedule, replaced by
// maintenance.tasks.<name>, and its enabled switch.
func TestVulnTaskSpecTakesTheConfiguration(t *testing.T) {
	a := &App{cfg: &config.Config{}}
	td := vuln.TaskDefaults()[0]
	if got := a.vulnTaskSpec(td); got.Schedule != "*/15 * * * *" || got.Disabled || got.DedupKey != vuln.DedupSyncOSV {
		t.Fatalf("default spec = %+v", got)
	}
	a.cfg.Maintenance.Tasks = map[string]config.MaintenanceTask{td.Name: {Enabled: false, Schedule: "0 * * * *"}}
	if got := a.vulnTaskSpec(td); got.Schedule != "0 * * * *" || !got.Disabled || got.DedupKey != vuln.DedupSyncOSV {
		t.Fatalf("configured spec = %+v", got)
	}
}

// A vulnArea without ports wires nothing: the hooks are the inner ones, a nil port stays nil and the audit count reaches the
// collector.
func TestVulnAreaWithoutPortsWiresNothing(t *testing.T) {
	var inner items.Hooks = items.NoHooks{}
	var v vulnArea
	if got := v.hooks(inner); got != inner {
		t.Error("the area wrapped the hooks without an evaluation")
	}
	v.countAudit("npm", "local", "ok") // no metrics: counts nothing, does not panic
	m, err := vmetrics.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	v.metrics = m
	v.countAudit("npm", "local", "ok")
	if v.states != nil || v.evaluator != nil || v.advisories != nil {
		t.Error("a port is wired")
	}
}
