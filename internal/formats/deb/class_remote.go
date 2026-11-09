package deb

import "github.com/guided-traffic/trove/internal/formats"

// The remote class (R-DEB-001, R-DEB-019 to R-DEB-023): a pull-through cache of an APT archive, Debian, Ubuntu or any other, whose
// release documents are passed through byte for byte and verified against deb.upstreamKeys when the repository has them. The class
// brings the remote adapter (remote.go, remote_gate.go, remote_names.go), the handlers of the routes it serves (handlers_remote.go)
// and the setup and guide of its client (setup_remote.go, guide_remote.go).
func init() { registerClass(formats.Remote, func(f *Format) { f.remote = remoteAdapter{f: f} }) }
