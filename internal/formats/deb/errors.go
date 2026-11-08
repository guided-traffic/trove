package deb

import (
	"log/slog"
	"net/http"

	"github.com/guided-traffic/trove/internal/errs"
)

// WriteError renders a core error in the shape of R-DEB-005: the standard status of the error with the problem details of R-ARCH-023.
// APT shows the status line of a failed request and does not read bodies; curl and CI scripts read the body, whose `code` is one of
//
//	400  deb_distribution_required, deb_invalid_coordinate, deb_invalid_file_name, invalid_path
//	403  deb_members_unreadable
//	404  deb_not_in_release, deb_no_upstream_keys
//	405  no_deploy_target
//	409  deb_version_exists, deb_filename_conflict, deb_withheld, generated_item, deb_no_next_key, deb_key_in_use, deb_key_state,
//	     reindex_running
//	413  payload_too_large (the bound of R-DEB-034; Q-DEB-44)
//	422  deb_invalid_archive, deb_invalid_control, deb_unsupported_compression, deb_key_unsupported, deb_upstream_keys_required,
//	     deb_member_unverified, deb_mirror_suites_required
//	501  deb_not_implemented
//	502  deb_upstream_signature_invalid, deb_release_invalid, deb_index_mismatch, upstream_integrity_mismatch, upstream_digest_missing
//	503  shadow_check_unavailable
//
// The kind of each code fixes its status in internal/errs, so the format adds nothing to the shape: the codes are the format's, the
// table is held by a test (errors_test.go), and the chain's refusals on this format's paths are rendered here too (Q-ARCH-214).
func (f *Format) WriteError(w http.ResponseWriter, r *http.Request, err error) {
	errs.Write(w, r, err, w.Header().Get("X-Request-Id"), slog.Default())
}

// notImplemented is the answer of a route whose work package is not built yet (R-DEB-005, the skeleton of WP0).
func notImplemented() error {
	return errs.E(errs.NotImplemented, errs.CodeDebNotImplemented, "This operation of the Debian repository is not built yet.")
}
