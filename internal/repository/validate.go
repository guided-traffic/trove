package repository

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/errs"
)

// validation collects the violations of one document, so that a request reports
// every one of them at once (R-REPO-008).
type validation struct {
	fields   []errs.FieldError
	warnings []Warning
}

func (v *validation) add(pointer, code, detail string) {
	v.fields = append(v.fields, errs.FieldError{Pointer: pointer, Code: code, Detail: detail})
}

// validate checks a normalized document against the rules of R-REPO-008 in one
// pass. create adds the key rules, which do not apply to an update because the
// key cannot change. It returns the format entry (zero when the format is
// unknown) and sets d.Layout to the format's layout.
func validate(cat FormatCatalog, d *Document, create bool) (FormatInfo, *validation) {
	v := &validation{}
	if create {
		if fe := checkKey(d.Key); fe != nil {
			v.fields = append(v.fields, *fe)
		}
	}
	info, known := cat.Format(d.Format)
	if !known {
		v.add("/format", errs.CodeUnknownFormat, fmt.Sprintf("format %q is not in the format catalog", d.Format))
	}
	classOK := validClass(d.Class)
	if !classOK {
		v.add("/class", errs.CodeUnsupportedClass, fmt.Sprintf("class %q is not local, remote or virtual", d.Class))
	}
	if known && classOK && !info.Supports(d.Class) {
		v.add("/class", errs.CodeUnsupportedClass,
			fmt.Sprintf("the %s format does not support %s repositories", info.Name, d.Class))
		classOK = false
	}
	if known {
		switch d.Layout {
		case "", info.Layout:
			d.Layout = info.Layout
		default:
			v.add("/layout", errs.CodeUnsupportedLayout,
				fmt.Sprintf("the %s format has the layout %q and no other", info.Name, info.Layout))
			d.Layout = info.Layout
		}
	}
	validateText(v, d)
	if known {
		validatePatterns(v, info.PatternSubject, "/includePatterns", d.IncludePatterns)
		validatePatterns(v, info.PatternSubject, "/excludePatterns", d.ExcludePatterns)
	}
	if known && classOK {
		validateSections(v, info, d)
	}
	return info, v
}

func validateText(v *validation, d *Document) {
	if utf8.RuneCountInString(d.Description) > MaxDescription {
		v.add("/description", codeOutOfRange, "at most "+strconv.Itoa(MaxDescription)+" characters")
	}
	if utf8.RuneCountInString(d.AdminNotes) > MaxAdminNotes {
		v.add("/adminNotes", codeOutOfRange, "at most "+strconv.Itoa(MaxAdminNotes)+" characters")
	}
	switch d.HideExistence {
	case HideInherit, HideTrue, HideFalse:
	default:
		v.add("/hideExistence", codeInvalidValue, `must be "inherit", "true" or "false"`)
	}
}

func validatePatterns(v *validation, subject PatternSubject, pointer string, list []string) {
	if len(list) > MaxPatterns {
		v.add(pointer, codeInvalidPattern, "at most "+strconv.Itoa(MaxPatterns)+" patterns per list")
		return
	}
	for i, p := range list {
		if _, err := CompilePattern(subject, p); err != nil {
			v.add(pointer+"/"+strconv.Itoa(i), codeInvalidPattern, err.Error())
		}
	}
}

// validateSections checks the members that depend on the class and the format:
// a class section that does not match the class, and the members that the
// format does not support, answer not_applicable (R-REPO-004, R-GENERIC-008).
func validateSections(v *validation, info FormatInfo, d *Document) {
	na := func(pointer, why string) { v.add(pointer, errs.CodeNotApplicable, why) }
	if d.Class != Remote && len(d.Remote) > 0 {
		na("/remote", "the remote section applies to remote repositories")
	}
	if d.Class != Virtual && len(d.Virtual) > 0 {
		na("/virtual", "the virtual section applies to virtual repositories")
	}
	if d.PriorityMember {
		switch {
		case d.Class == Virtual:
			na("/priorityMember", "priority members are local and remote repositories")
		case !info.PriorityMembers:
			na("/priorityMember", fmt.Sprintf("the %s format has no priority members", info.Name))
		}
	}
	if d.Local != nil {
		validateLocal(v, info, d, na)
	}
	if len(d.Storage) > 0 {
		trimmed := bytes.TrimSpace(d.Storage)
		var probe map[string]json.RawMessage
		if len(trimmed) == 0 || trimmed[0] != '{' || json.Unmarshal(trimmed, &probe) != nil {
			v.add("/storage", codeInvalidValue, "must be a JSON object")
		} else if raw, ok := probe["maxUploadSize"]; ok && !validByteLimit(raw) {
			v.add("/storage/maxUploadSize", codeInvalidValue, "must be a non-negative integer number of bytes")
		}
	}
}

// validByteLimit reports whether a JSON value is a non-negative integer that
// fits an int64, written without fraction or exponent: the form the upload
// admission decodes (R-STOR-004). Anything else would decode to "no limit" there.
func validByteLimit(raw json.RawMessage) bool {
	_, err := strconv.ParseUint(string(bytes.TrimSpace(raw)), 10, 63)
	return err == nil
}

func validateLocal(v *validation, info FormatInfo, d *Document, na func(pointer, why string)) {
	if d.Class != Local {
		na("/local", "the local section applies to local repositories")
		return
	}
	l := d.Local
	own := fmt.Sprintf("the %s format has no package names, so ownership and claims have nothing to act on", info.Name)
	if l.PackageOwnership != nil {
		switch {
		case !info.PackageNames:
			na("/local/packageOwnership", own)
		case *l.PackageOwnership != "enforce" && *l.PackageOwnership != "off":
			v.add("/local/packageOwnership", codeInvalidValue, `must be "enforce" or "off"`)
		}
	}
	if l.OwnershipDomain != nil && !info.PackageNames {
		na("/local/ownershipDomain", own)
	}
	if l.AutoClaim != nil && (!info.PackageNames || !info.PinsAndClaims) {
		na("/local/autoClaim", own)
	}
	if len(l.Caps) > 0 && !info.Caps {
		na("/local/caps", fmt.Sprintf("the %s format has no version groups, so caps have nothing to count", info.Name))
	}
}

// err turns the violations into the error of a 422 response (R-API-009, Q-REPO-84):
// the top-level code is validation_failed whatever the number of violations, and
// every violation, with its own code, is in errors[] (R-REPO-008). A reserved key
// is the one exception, because R-API-004 defines reserved_name as the code of
// that refusal.
func (v *validation) err() error {
	if len(v.fields) == 0 {
		return nil
	}
	code, detail := errs.CodeValidation, strconv.Itoa(len(v.fields))+" fields are invalid."
	if len(v.fields) == 1 {
		detail = "1 field is invalid."
	}
	for _, f := range v.fields {
		if f.Code == codeReservedName {
			code, detail = codeReservedName, f.Detail
			break
		}
	}
	return errs.E(errs.Invalid, code, detail, errs.WithFields(v.fields...))
}
