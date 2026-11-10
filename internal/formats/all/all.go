// Package all imports and registers every format (R-ARCH-010). The composition
// root imports it for its effect.
package all

import (
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/ansible"
	"github.com/guided-traffic/trove/internal/formats/deb"
	"github.com/guided-traffic/trove/internal/formats/generic"
	"github.com/guided-traffic/trove/internal/formats/npm"
	"github.com/guided-traffic/trove/internal/formats/nuget"
	"github.com/guided-traffic/trove/internal/formats/oci"
)

func init() {
	formats.Register(generic.Format{})
	formats.Register(npm.New())
	formats.Register(nuget.New())
	formats.Register(oci.New())
	formats.Register(deb.New())
	formats.Register(ansible.New())
}
