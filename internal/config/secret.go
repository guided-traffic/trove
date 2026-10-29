package config

import "log/slog"

// Secret is a secret-typed key with a companion `<name>_file` (R-OPS-028).
//
// A field of this type expands to two keys: `<name>` (a value that may come from
// the environment only; inline in the YAML file it is an error, because the file
// is typically a ConfigMap) and `<name>_file` (a path whose content is the
// value). Setting both is an error. The loader reads the file once; a secret
// that must follow a rotated Secret without a restart is declared file-only
// instead (a plain string field tagged `secret:"file"`).
type Secret struct {
	Value string // the secret itself; empty when unset
	File  string // path of the file that holds it; empty when unset
}

// IsSet reports whether the secret has a value.
func (s Secret) IsSet() bool { return s.Value != "" }

// String implements fmt.Stringer so a Secret never prints its value.
func (s Secret) String() string {
	if s.Value == "" {
		return ""
	}
	return Redacted
}

// GoString implements fmt.GoStringer for the same reason.
func (s Secret) GoString() string { return "config.Secret{" + s.String() + "}" }

// MarshalJSON renders the redacted form: the value never serializes.
func (s Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + s.String() + `"`), nil }

// MarshalText renders the redacted form for every text encoder.
func (s Secret) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// MarshalYAML renders the redacted form.
func (s Secret) MarshalYAML() (any, error) { return s.String(), nil }

// LogValue keeps the value out of slog records.
func (s Secret) LogValue() slog.Value { return slog.StringValue(s.String()) }

// Redacted replaces secret values in every rendering of the configuration.
const Redacted = "[REDACTED]"
