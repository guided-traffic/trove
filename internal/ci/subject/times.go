package subject

import "time"

// CheckTimes is step 8 of R-CI-004 for a decoded claim set and a clock: `exp`, `nbf` and `iat` against now
// with the issuer's clock skew, with the reasons Verify answers (token_expired, token_not_yet_valid,
// issued_in_future, required_claim_missing, subject_token_malformed). The dry run of a mapping set calls it
// when the request carries `now` (R-CI-019), so the claim tester and the exchange read one rule. The values of
// the time claims are json.Number, as jws.DecodeObject decodes them.
func CheckTimes(payload map[string]any, skew time.Duration, now time.Time) error {
	_, err := checkTimes(payload, skew, now)
	return err
}
