package host

import (
	"fmt"

	"example.test/fix/internal/formats"
)

// Interfaces of the standard library, anonymous interfaces and structs of the contract are fine.
func clean(a formats.Adapter, v any) {
	_, _ = v.(fmt.Stringer)
	_, _ = v.(interface{ Close() error })
	_, _ = v.(formats.Plain)
	_ = a
}
