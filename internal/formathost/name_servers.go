package formathost

import (
	"context"

	"github.com/guided-traffic/trove/internal/virtual"
)

// nameServers is the virtual engine's NameServers (Q-REPO-270): it asks the format's NameServer of the remote member for the
// name in the scope of the virtual repository of the pair.
//
// This is the seam the work package of the Debian remote class fills: until a format registers a remote adapter with a NameServer
// the answer is undetermined, which no repository can reach (a format that has a NameServer has a remote class).
type nameServers struct{}

// NewNameServers returns the virtual engine's name servers.
func NewNameServers() virtual.NameServers { return nameServers{} }

// ServesName implements [virtual.NameServers].
func (nameServers) ServesName(context.Context, virtual.Caller, virtual.Exposure, string) (virtual.ProbeResult, error) {
	return virtual.ProbeResult{Outcome: virtual.Undetermined}, nil
}
