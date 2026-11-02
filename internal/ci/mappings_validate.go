package ci

import (
	"errors"
	"fmt"
	"slices"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/ci/claims"
	"github.com/guided-traffic/trove/internal/errs"
)

// The shape rules of a mapping and the answer to a rule violation (R-CI-009, R-CI-019, Q-CI-30). The
// functions here are pure; the rules that need the database are in mappings_bind.go and
// mappings_write.go.

// namedMappingCodes are the rules the specification names by their code (R-CI-010 to R-CI-014, R-CI-019,
// Q-CI-26): such a violation is also the problem's `code`, and the first of them decides it. Any other
// violation (invalid_name, out_of_range, immutable, invalid_pattern, invalid_value, …) answers
// validation_failed (Q-CI-30). A function, not a package variable (R-ARCH-032).
func namedMappingCodes() []string {
	return []string{
		errs.CodePinRequired, errs.CodeCatchAllCondition, errs.CodeUnprotectedWrite, errs.CodeUnpinnedWrite,
		errs.CodeActionNotAllowed, errs.CodeEmptyGrant, errs.CodeTemplateInvalid, errs.CodeTemplateUnanchored,
		errs.CodeConditionInvalid, errs.CodeJobTokenFallbackNone, errs.CodeProjectPrefixRequired,
		errs.CodeGroupsNotAllowedInProject, errs.CodePolicyOutsideProject, errs.CodeRepositoryOutsideProject,
		errs.CodeUnknownPolicy, errs.CodeUnknownGroup, errs.CodeMappingLimitReached,
	}
}

// mappingViolation builds the 422 of a list of field errors in the shape of the rest of the management
// API (R-API-009, Q-CI-30): errors[] lists every violation with its pointer and code, and a named rule is
// also the problem's `code`.
func mappingViolation(fields []errs.FieldError) error {
	if len(fields) == 0 {
		return nil
	}
	code, detail := errs.CodeValidation, fields[0].Detail
	if len(fields) > 1 {
		detail = fmt.Sprintf("%d fields are invalid.", len(fields))
	}
	for _, f := range fields {
		if slices.Contains(namedMappingCodes(), f.Code) {
			code, detail = f.Code, f.Detail
			break
		}
	}
	return errs.E(errs.Invalid, code, detail, errs.WithFields(fields...))
}

// fieldOfCheck turns the refusal of the claim engine into the field error of its pointer.
func fieldOfCheck(err error) []errs.FieldError {
	var ce *claims.Error
	if errors.As(err, &ce) {
		return []errs.FieldError{{Pointer: ce.Pointer, Code: string(ce.Reason), Detail: ce.Detail}}
	}
	return nil
}

// maxMappingPriority is the largest priority (R-CI-009).
const maxMappingPriority = 1000000

// shapeFields are the violations of a mapping input that need neither the issuer nor the database: the
// name, the priority, the description and the lifetime. The rules of the condition and the grants are
// claims.Check's.
func (s *MappingService) shapeFields(in MappingInput) []errs.FieldError {
	var fe []errs.FieldError
	if !issuerNamePattern.MatchString(in.Name) {
		fe = append(fe, field("/name", "invalid_name", "The name matches ^[a-z0-9][a-z0-9._-]{1,63}$."))
	}
	if in.Priority < 1 || in.Priority > maxMappingPriority {
		fe = append(fe, field("/priority", "out_of_range", fmt.Sprintf("The priority is between 1 and %d.", maxMappingPriority)))
	}
	if utf8.RuneCountInString(in.Description) > maxDescription {
		fe = append(fe, field("/description", "invalid_description", "The description has at most 1024 characters."))
	}
	if in.MaxTTLSeconds != nil {
		limit := int64(s.o.Config.Token.MaxTTL.Seconds())
		if *in.MaxTTLSeconds < 1 || *in.MaxTTLSeconds > limit {
			fe = append(fe, field("/maxTtlSeconds", "out_of_range", fmt.Sprintf("maxTtlSeconds is between 1 and ci.token.max_ttl (%d seconds).", limit)))
		}
	}
	return fe
}

// withDefaults fills the defaults of a mapping (R-CI-009): enabled, subjectKinds ["id_token"], options
// deny and false. A PUT is a full replacement, so an omitted member takes its default.
func withDefaults(in MappingInput) (enabled bool, kinds []string, opts claims.Options) {
	enabled = in.Enabled == nil || *in.Enabled
	kinds = in.SubjectKinds
	if len(kinds) == 0 {
		kinds = []string{SubjectIDToken}
	}
	if in.Options != nil {
		opts = *in.Options
	}
	return enabled, kinds, opts
}
