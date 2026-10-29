package observability

import (
	"fmt"
	"sort"
	"strings"

	dto "github.com/prometheus/client_model/go"
)

// allowedLabels is the label list of R-OPS-007. Each label has a bounded value
// set, defined by code or by configuration.
func allowedLabels() []string {
	return []string{
		"listener", "handler", "method", "code", "code_class", "format", "class", "repo", "subscription", "issuer",
		"operation", "kind", "result", "outcome", "task", "cache", "state", "status", "direction", "auth_method",
		"query", "name", "reason", "mode", "family", "label", "source", "check", "action", "trigger", "via", "limit",
		"area", "db_pool", "command", "mechanism", "limiter", "key_id",
	}
}

// buildInfoOnlyLabels may appear only on trove_build_info.
func buildInfoOnlyLabels() []string { return []string{"version", "revision", "goversion", "role"} }

// AllowedLabel reports whether name may be a label of the given family.
func AllowedLabel(family, name string) bool {
	for _, l := range allowedLabels() {
		if l == name {
			return true
		}
	}
	if family == "trove_build_info" {
		for _, l := range buildInfoOnlyLabels() {
			if l == name {
				return true
			}
		}
	}
	return false
}

// checkName validates the naming rules of R-OPS-007 for a family.
func checkName(name string, counter bool) error {
	if !strings.HasPrefix(name, "trove_") {
		return fmt.Errorf("metric %q: name must start with trove_", name)
	}
	if counter && !strings.HasSuffix(name, "_total") {
		return fmt.Errorf("metric %q: a counter name ends in _total", name)
	}
	if !counter && strings.HasSuffix(name, "_total") && !strings.HasSuffix(name, "_seconds_total") && !strings.HasSuffix(name, "_bytes_total") {
		return fmt.Errorf("metric %q: only counters end in _total", name)
	}
	return nil
}

func checkLabels(family string, labels []string) error {
	for _, l := range labels {
		if !AllowedLabel(family, l) {
			return fmt.Errorf("metric %q: label %q is not in the allowed list of R-OPS-007", family, l)
		}
	}
	return nil
}

// CheckFamilies returns one error per family of g that breaks the naming or
// label rules of R-OPS-007: a missing trove_ prefix (the go_ and process_
// collectors are exempt), a label outside the allowed list, a role-label on a
// family other than trove_build_info. Tests of any package call it over the
// registry after exercising their code.
func CheckFamilies(families []*dto.MetricFamily) []error {
	var errs []error
	for _, f := range families {
		name := f.GetName()
		if strings.HasPrefix(name, "go_") || strings.HasPrefix(name, "process_") {
			continue
		}
		if !strings.HasPrefix(name, "trove_") {
			errs = append(errs, fmt.Errorf("family %q: name must start with trove_", name))
			continue
		}
		seen := map[string]bool{}
		for _, m := range f.GetMetric() {
			for _, lp := range m.GetLabel() {
				seen[lp.GetName()] = true
			}
		}
		var names []string
		for l := range seen {
			names = append(names, l)
		}
		sort.Strings(names)
		if err := checkLabels(name, names); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}
