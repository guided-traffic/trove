// Package config is the one configuration schema of trove (R-ARCH-020): a
// single struct tree whose tags give every key its YAML name, default,
// validation and secret flag. The loader, the environment variable names, the
// JSON Schema and the redacted effective-configuration view are all derived
// from the tree by reflection; no key exists outside it.
//
// Each section lives in its own file (process.go, server.go, database.go, …) so
// the owner of a subsystem edits only its section. See docs/developer/configuration.md.
package config

// Config is the root of the schema. A new section is a new field here and a
// new file next to this one.
type Config struct {
	Process     Process     `yaml:"process"`
	Server      Server      `yaml:"server"`
	Database    Database    `yaml:"database"`
	Migrate     Migrate     `yaml:"migrate"`
	Runtime     Runtime     `yaml:"runtime"`
	Valkey      Valkey      `yaml:"valkey"`
	Storage     Storage     `yaml:"storage"`
	Jobs        Jobs        `yaml:"jobs"`
	Cluster     Cluster     `yaml:"cluster"`
	Cache       Cache       `yaml:"cache"`
	Secrets     Secrets     `yaml:"secrets"`
	System      System      `yaml:"system"`
	Health      Health      `yaml:"health"`
	Log         Log         `yaml:"log"`
	Metrics     Metrics     `yaml:"metrics"`
	Tracing     Tracing     `yaml:"tracing"`
	Audit       Audit       `yaml:"audit"`
	Maintenance Maintenance `yaml:"maintenance"`
	Proxy       Proxy       `yaml:"proxy"`

	// The M1 sections are declared in m1.go; their owners fill them there.
	Auth         Auth         `yaml:"auth"`
	Authz        Authz        `yaml:"authz"`
	API          API          `yaml:"api"`
	Repositories Repositories `yaml:"repositories"`

	// The M2 sections. proxy (above) gained its engine keys in outbound.go.
	NPM NPM `yaml:"npm"`
	UI  UI  `yaml:"ui"`

	// The M3 sections.
	NuGet NuGet `yaml:"nuget"`

	// The M4 section: the GitLab CI OIDC federation (ci.go).
	CI CI `yaml:"ci"`

	// The Bastion interface of the main installation (R-BAS-004 to R-BAS-017): the registrations, the check, the
	// feed and the download intake (bastions.go). The Bastion's own configuration is a tree of its own
	// (BastionConfig in bastion_role.go); the section named bastion there has nothing to do with this one.
	Bastions Bastions `yaml:"bastions"`

	// ConfigAsCode holds secrets_dir, which the secret references of M2 use.
	ConfigAsCode AsCode `yaml:"config_as_code"`
}

//go:generate go run ./schemagen -out schema.json -bastion-out bastion.schema.json -bastion-chart ../../deploy/helm/trove-bastion
