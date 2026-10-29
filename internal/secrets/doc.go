// Package secrets seals the secrets that trove stores in PostgreSQL to reach
// other systems (R-ARCH-021): AES-256-GCM under a key ring that the operator
// provides as files, with the key ID in the envelope so keys rotate without a
// decryption gap (R-HA-027).
//
// There is no default key and no generated key: Load fails without a ring, and
// `trove serve` stops. Key material is never logged, never part of Status and
// never part of an error text.
//
// Every sealed column is registered once with Register, at start-up, from the
// package that owns the table. Seal and Open take the registered Column, so a
// column that is not in the registry cannot be sealed, and the per-key counts of
// Status and the secrets-reencrypt job walk the one registry.
package secrets
