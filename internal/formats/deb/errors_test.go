package deb

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
)

// The table of R-DEB-005: every code of the format answers the status the table gives it.
func TestErrorCodesAnswerTheStatusOfTheTable(t *testing.T) {
	table := map[int][]string{
		400: {"deb_distribution_required", "deb_invalid_coordinate", "deb_invalid_file_name", "invalid_path"},
		403: {"deb_members_unreadable"},
		404: {"deb_not_in_release", "deb_no_upstream_keys"},
		405: {"no_deploy_target"},
		409: {"deb_version_exists", "deb_filename_conflict", "deb_withheld", "generated_item", "deb_no_next_key", "deb_key_in_use", "deb_key_state", "reindex_running"},
		413: {"payload_too_large"},
		422: {"deb_invalid_archive", "deb_invalid_control", "deb_unsupported_compression", "deb_key_unsupported", "deb_upstream_keys_required", "deb_member_unverified", "deb_mirror_suites_required"},
		501: {"deb_not_implemented"},
		502: {"deb_upstream_signature_invalid", "deb_release_invalid", "deb_index_mismatch", "upstream_integrity_mismatch", "upstream_digest_missing"},
		503: {"shadow_check_unavailable"},
	}
	for status, codes := range table {
		for _, code := range codes {
			info, ok := errs.Lookup(code)
			if !ok {
				t.Errorf("code %q is not registered", code)
				continue
			}
			if got := (&errs.Error{Kind: info.Kind}).Status(); got != status {
				t.Errorf("code %q answers %d, the table says %d", code, got, status)
			}
		}
	}
}

func TestWriteErrorIsTheProblemDetailsShape(t *testing.T) {
	rec := httptest.NewRecorder()
	New().WriteError(rec, httptest.NewRequest(http.MethodPut, "/upload/x.deb", nil),
		errs.E(errs.Unprocessable, errs.CodeDebInvalidArchive, "The upload is not an ar archive."))
	if rec.Code != http.StatusUnprocessableEntity || rec.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("status %d, type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Code != "deb_invalid_archive" {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
}
