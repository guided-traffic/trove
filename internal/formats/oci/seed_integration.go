//go:build integration

package oci

// This file is compiled only with the integration build tag, which the integration tests use and no build of the binary does. It is the
// one door around the protocol: the tests that seed the items of a local repository directly through the item store (the registry routes
// are not mounted in their stack, or they need state a push would leave) commit with the payload SeededItem, and the rows of the format
// are then written by the test through the data port. TestSeededItemSurface checks that no other file is tag-gated and that the build
// without the tag exports nothing of it.

// SeededItem is the payload of a commit that stands for a push in a test that seeds a repository's items directly.
type SeededItem struct{}

// admitSeeded reports whether the payload of a commit is the seed of a test: the hook then admits the manifest or tag item without the
// protocol's parse, reference check and rows.
func admitSeeded(payload any) bool {
	_, ok := payload.(SeededItem)
	return ok
}
