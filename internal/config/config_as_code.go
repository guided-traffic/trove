package config

// AsCode is the config_as_code section (R-OPS-029). Only the key that
// secret references need exists so far: a secretRef file of a repository's
// secret field is resolved under SecretsDir (R-REPO-033). The keys of the
// document mechanism itself (files, mode, lock_managed_objects) arrive with it.
type AsCode struct {
	SecretsDir string `yaml:"secrets_dir" default:"/etc/trove/cac-secrets" doc:"Directory of the files that a secretRef {\"file\": name} names; a name is a file directly under it (R-REPO-033, R-OPS-029)."`
}
