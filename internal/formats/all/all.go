// Package all imports and registers every format (R-ARCH-010). The composition
// root imports it for its effect.
package all

import (
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/generic"
)

func init() {
	formats.Register(generic.Format{})
}
