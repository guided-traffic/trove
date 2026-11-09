package repository

import (
	"encoding/json"

	"github.com/guided-traffic/trove/internal/errs"
)

// The mirror rules of the Debian format's member that the document alone decides (R-DEB-024, Q-DEB-91): an APT archive has no list of
// its suites, so a mirror names them. The keys a mirror needs are validateDeb's (deb_upstream_keys_required); the ranges of the members
// are the schema's; that an Owner without repositories:write-config keeps deb.mirror as stored is the generic rule of the format's
// mirror member (checkDelegatedFormatMirror).

const debSuitesPointer = "/deb/mirror/suites"

// validateDebMirror refuses a remote Debian repository in mode mirror that names no suite with 422 deb_mirror_suites_required.
func validateDebMirror(v *validation, d *Document) {
	if remoteMode(d) != ModeMirror {
		return
	}
	var m struct {
		Mirror struct {
			Suites []string `json:"suites"`
		} `json:"mirror"`
	}
	_ = json.Unmarshal(d.Deb, &m) //nolint:errcheck // the schema checked the member; one that does not read names no suite
	if len(m.Mirror.Suites) == 0 {
		v.add(debSuitesPointer, errs.CodeDebMirrorSuitesRequired, "a mirror of an APT archive keeps the suites it is told to: name at least one in deb.mirror.suites")
	}
}
