package match

// The version of each format adapter's normalization of advisory names and versions (R-VULN-004, Q-VULN-63). What the advisory
// sync stores of a record (the normalized names, versions and fixed versions, the evaluable ranges) is a function of the record
// and of the adapter, so a change of an adapter that alters it for the same record raises that ecosystem's constant by one.
const (
	npmAdapterVersion   = 1
	nugetAdapterVersion = 1
)

// AdapterVersion returns the adapter version of an ecosystem; an ecosystem without a constant is at 1. A run that finds
// vuln_source_state.adapter_version of the ecosystem lower than this value imports the ecosystem again in full and
// unconditionally: no 304, no digest skip. A row stored before the column holds 0.
func AdapterVersion(ecosystem string) int {
	switch ecosystem {
	case "npm":
		return npmAdapterVersion
	case "NuGet":
		return nugetAdapterVersion
	}
	return 1
}
