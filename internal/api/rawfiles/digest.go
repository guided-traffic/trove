package rawfiles

import (
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/errs"
)

// parseReprDigest reads the Repr-Digest header of an upload (RFC 9530, R-STOR-007):
// a structured-field dictionary of byte sequences, `sha-256=:<base64>:` and
// `sha-512=:<base64>:`. Other algorithms (md5, sha-1, unixsum) are deprecated by
// the RFC and not verified here. A value that is not a dictionary of byte
// sequences, or a digest of the wrong length, is 400 invalid_header; a digest
// that differs from the content is 409 after the body (the spool).
func parseReprDigest(h http.Header) (blob.Expected, error) {
	var out blob.Expected
	for _, line := range h.Values("Repr-Digest") {
		for _, member := range strings.Split(line, ",") {
			member = strings.TrimSpace(member)
			if member == "" {
				continue
			}
			name, val, ok := strings.Cut(member, "=")
			if !ok {
				return out, badDigest()
			}
			if i := strings.IndexByte(val, ';'); i >= 0 { // parameters are ignored
				val = val[:i]
			}
			if len(val) < 2 || val[0] != ':' || val[len(val)-1] != ':' {
				return out, badDigest()
			}
			raw, err := base64.StdEncoding.DecodeString(val[1 : len(val)-1])
			if err != nil {
				return out, badDigest()
			}
			switch strings.ToLower(strings.TrimSpace(name)) {
			case "sha-256":
				if len(raw) != 32 {
					return out, badDigest()
				}
				out.SHA256 = raw
			case "sha-512":
				if len(raw) != 64 {
					return out, badDigest()
				}
				out.SHA512 = raw
			}
		}
	}
	return out, nil
}

func badDigest() error {
	return errs.E(errs.Invalid, errs.CodeInvalidHeader, "Repr-Digest must be a dictionary of byte sequences, such as sha-256=:<base64>:.")
}

// reprDigest renders the Repr-Digest of a response: the SHA-256 always, the
// SHA-512 when the request asks for it with Want-Repr-Digest (RFC 9530).
func reprDigest(want http.Header, sha256, sha512 []byte) string {
	out := "sha-256=:" + base64.StdEncoding.EncodeToString(sha256) + ":"
	for _, line := range want.Values("Want-Repr-Digest") {
		for _, member := range strings.Split(line, ",") {
			name, weight, _ := strings.Cut(strings.TrimSpace(member), "=")
			if strings.EqualFold(name, "sha-512") && weight != "0" && len(sha512) == 64 {
				return out + ", sha-512=:" + base64.StdEncoding.EncodeToString(sha512) + ":"
			}
		}
	}
	return out
}
