//go:build !unix

package blob

import "errors"

// freeBytes is not available on this platform; the min_free_bytes check is
// skipped when the probe fails.
func freeBytes(string) (uint64, error) { return 0, errors.New("free space is not available") }
