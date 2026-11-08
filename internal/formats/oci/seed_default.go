//go:build !integration

package oci

// admitSeeded is false in every build without the integration tag: nothing but the protocol's own payloads and the fills of remote
// repositories stores a manifest or a tag item (seed_integration.go).
func admitSeeded(any) bool { return false }
