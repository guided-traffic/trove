package config

import "strings"

// Secrets configures the stored-secret key ring (R-ARCH-021). There is no
// default key and no generated key: a missing key ring stops `trove serve`.
type Secrets struct {
	EncryptionKeyFiles []string `yaml:"encryption_key_files" default:"[]" doc:"Key files: 32 random bytes, base64; the file name without .key is the key ID."`
	ActiveKeyID        string   `yaml:"active_key_id" default:"" doc:"Key ID used for encryption; every listed key decrypts."`
}

// ValidateFor requires the key ring where a replica starts.
func (s *Secrets) ValidateFor(cmd Command, r *Report) {
	if !cmd.runsReplica() {
		return
	}
	if len(s.EncryptionKeyFiles) == 0 {
		r.Add("encryption_key_files", "is required: list at least one key file")
	}
	if s.ActiveKeyID == "" {
		r.Add("active_key_id", "is required")
	}
}

// Validate checks that the active key is one of the listed key files.
func (s *Secrets) Validate(r *Report) {
	if s.ActiveKeyID == "" || len(s.EncryptionKeyFiles) == 0 {
		return
	}
	for _, f := range s.EncryptionKeyFiles {
		if KeyIDOf(f) == s.ActiveKeyID {
			return
		}
	}
	r.Add("active_key_id", "%q matches none of encryption_key_files (the key ID is the file name without .key)", s.ActiveKeyID)
}

// KeyIDOf returns the key ID of a key file: its base name without ".key".
func KeyIDOf(file string) string {
	base := file
	for i := len(file) - 1; i >= 0; i-- {
		if file[i] == '/' {
			base = file[i+1:]
			break
		}
	}
	return strings.TrimSuffix(base, ".key")
}
