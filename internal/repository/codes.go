package repository

// Codes of other areas that the validation reports. They are registered by the
// areas that own them (invalid_pattern by authz, reserved_name by api,
// out_of_range by api); the repository package names them as strings so that it
// does not depend on which area lands first. The codes of this area are the
// errs.Code constants.
const (
	codeInvalidPattern = "invalid_pattern"
	codeReservedName   = "reserved_name"
	codeOutOfRange     = "out_of_range"
	codeInvalidValue   = "invalid_value"
	// codeImmutable is the code of R-API-013 for a change to a member that is fixed
	// at creation; it is the field code, the answer is validation_failed.
	codeImmutable = "immutable"
)
