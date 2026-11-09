package formathost

import (
	"context"
	"encoding/json"
	"time"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/virtual"
)

// nameServersRetry is how long a caller is told to wait when the scope of a remote member is not settled: the format enqueued the fetch
// and the parse through the background path, and a few seconds decide a small archive (R-DEB-023, Q-DEB-41).
const nameServersRetry = 10 * time.Second

// nameServers is the virtual engine's NameServers (Q-REPO-270): it asks the format's NameServer of the remote member for the name in
// the scope of the virtual repository of the pair. A format without one (every format whose upstream has a document per package) is
// asked by the engine's probe and never reaches this.
type nameServers struct {
	h formats.Host
}

// bindingHost is the Host a NameServer is handed when the shadowing check runs: the data ports of the formats and nothing else. The
// check runs inside the virtual engine, which is built before the format host (the host's Resolver is the engine); a NameServer reads
// the rows of its format's data port through FormatData, and every other method of the contract is absent here and panics if called.
type bindingHost struct {
	formats.Host
	b *Bindings
}

func (h bindingHost) FormatData(id formats.ID) any { return h.b.Get(id) }

// NewNameServers returns the virtual engine's name servers over the data ports of the formats.
func NewNameServers(b *Bindings) virtual.NameServers { return nameServers{h: bindingHost{b: b}} }

// ServesName implements [virtual.NameServers]. A remote whose format has no NameServer, or whose answer fails, is undetermined: the
// caller without manage gets 503 shadow_check_unavailable (R-REPO-048 step 5).
func (n nameServers) ServesName(ctx context.Context, _ virtual.Caller, x virtual.Exposure, name string) (virtual.ProbeResult, error) {
	caps := formats.RemoteCapabilities(formats.ID(x.Remote.Format))
	if caps.Names == nil {
		return virtual.ProbeResult{Outcome: virtual.Undetermined}, nil
	}
	served, known, err := caps.Names.ServesName(ctx, n.h, formats.NameQuery{
		Remote: repoRef(x.Remote), Via: repoRef(x.Virtual), ViaSettings: memberOf(x.Virtual.Config, x.Virtual.Format), Name: name,
	})
	switch {
	case err != nil:
		return virtual.ProbeResult{Outcome: virtual.Undetermined, RetryAfter: nameServersRetry}, err
	case served:
		return virtual.ProbeResult{Outcome: virtual.Served}, nil
	case known:
		return virtual.ProbeResult{Outcome: virtual.NotServed}, nil
	}
	return virtual.ProbeResult{Outcome: virtual.Undetermined, RetryAfter: nameServersRetry}, nil
}

// memberOf is the member of a repository's stored document that holds the settings of a format ("deb" for the Debian format).
func memberOf(config []byte, format string) json.RawMessage {
	var doc map[string]json.RawMessage
	if json.Unmarshal(config, &doc) != nil {
		return nil
	}
	return doc[format]
}
