package formats_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// settingSchema is the part of a format's settings schema the forms show: a member's title, its description, the label of each
// value of its enum and its own members.
type settingSchema struct {
	Title       string                   `json:"title"`
	Description string                   `json:"description"`
	Enum        []any                    `json:"enum"`
	EnumTitles  map[string]string        `json:"x-trove-enum-titles"`
	Properties  map[string]settingSchema `json:"properties"`
}

// specID is a requirement or decision ID of the specification (R-NPM-026, Q-DEB-17): a text for users names none.
var specID = regexp.MustCompile(`\b[RQ]-[A-Z]+-[0-9]+[a-z]?\b`)

// What a text for users does not hold (R-UI-088): the wording of the specification, which the OpenAPI document and the format's
// page of the specification keep for the developer.
var (
	// A three-digit number from 100 to 599 that no unit follows is an HTTP status ("answers 404"); "300 seconds" is a limit.
	statusCode = regexp.MustCompile(`\b([1-5][0-9]{2})\b(\s+[A-Za-z]+)?`)
	units      = []string{
		"second", "seconds", "minute", "minutes", "hour", "hours", "day", "days", "character", "characters",
		"byte", "bytes", "KiB", "MiB", "GiB", "keys", "entries", "patterns", "tags", "versions", "names",
	}
	// A dotted name is a configuration or document key (vulnerabilities.enabled, server.shutdown_timeout, npm.latestGuard).
	dottedKey = regexp.MustCompile(`\b[a-z][A-Za-z0-9_-]*\.[a-z][A-Za-z0-9_]*\b`)
	// A word joined by an underscore is an error code or a key (npm_audit_unavailable, shutdown_timeout).
	snakeCase = regexp.MustCompile(`\b[A-Za-z0-9]+_[A-Za-z0-9_]+\b`)
	// A word with an upper-case letter after its first lower-case ones is a member name (extraHosts, providerURL).
	camelCase = regexp.MustCompile(`\b[a-z]+[A-Z][A-Za-z0-9]*\b`)
	// null is JSON's, not the user's: the form shows an empty field.
	jsonNull = regexp.MustCompile(`(?i)\bnull\b`)
	// The end of a sentence: a description says what the setting does and when to change it, in one or two.
	sentenceEnd = regexp.MustCompile(`[.!?](\s|$)`)
)

// TestR_UI_088_EverySettingOfAFormatHasATitleAndPlainTexts: the settings forms label a member with its schema's title, show its
// description under it (Q-UI-79) and each value of an enum by its label (x-trove-enum-titles), so every member of every format's
// settings schema has a title, every value of a member's enum a label, and no title, description or label names a requirement or
// decision ID, which means nothing to the user who reads it. A member's description is one or two sentences in plain words: no
// HTTP status, no dotted configuration key, no error code, no member name in camelCase and no null.
func TestR_UI_088_EverySettingOfAFormatHasATitleAndPlainTexts(t *testing.T) {
	files, err := filepath.Glob("*/settings.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no settings schema found under internal/formats")
	}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var schema settingSchema
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		checkNoSpecID(t, file, "the root", schema.Title, schema.Description)
		checkMembers(t, file, "", schema.Properties)
	}
}

func checkMembers(t *testing.T, file, prefix string, properties map[string]settingSchema) {
	t.Helper()
	for name, member := range properties {
		where := prefix + name
		if member.Title == "" {
			t.Errorf("%s: the member %s has no title", file, where)
		}
		texts := []string{member.Title, member.Description}
		for _, v := range member.Enum {
			label := member.EnumTitles[fmt.Sprint(v)]
			if label == "" {
				t.Errorf("%s: the value %v of %s has no label in x-trove-enum-titles", file, v, where)
			}
			texts = append(texts, label)
		}
		for value := range member.EnumTitles {
			if !slices.ContainsFunc(member.Enum, func(v any) bool { return fmt.Sprint(v) == value }) {
				t.Errorf("%s: x-trove-enum-titles of %s labels %q, which its enum does not hold", file, where, value)
			}
		}
		checkNoSpecID(t, file, where, texts...)
		for _, text := range texts {
			for _, problem := range unplain(text) {
				t.Errorf("%s: %s %s in a text for users: %q", file, where, problem, text)
			}
		}
		if n := len(sentenceEnd.FindAllString(member.Description, -1)); n > 2 {
			t.Errorf("%s: the description of %s has %d sentences, at most two say what the setting does and when to change it: %q",
				file, where, n, member.Description)
		}
		checkMembers(t, file, where+".", member.Properties)
	}
}

func checkNoSpecID(t *testing.T, file, where string, texts ...string) {
	t.Helper()
	for _, text := range texts {
		if id := specID.FindString(text); id != "" {
			t.Errorf("%s: %s names %s in a text for users: %q", file, where, id, text)
		}
	}
}

// unplain names what makes a text for users read like the specification.
func unplain(text string) []string {
	var out []string
	for _, m := range statusCode.FindAllStringSubmatch(text, -1) {
		if !slices.Contains(units, strings.TrimSpace(m[2])) {
			out = append(out, "names the HTTP status "+m[1])
		}
	}
	for _, check := range []struct {
		re   *regexp.Regexp
		what string
	}{
		{dottedKey, "names the configuration key"},
		{snakeCase, "names the code"},
		{camelCase, "names the member"},
		{jsonNull, "names"},
	} {
		for _, m := range check.re.FindAllString(text, -1) {
			out = append(out, check.what+" "+m)
		}
	}
	return out
}

// TestR_UI_088_APlainTextNamesNoStatusKeyCodeMemberOrNull: the checks of the schemas' texts find what the specification's wording
// carried, and let a limit in words through.
func TestR_UI_088_APlainTextNamesNoStatusKeyCodeMemberOrNull(t *testing.T) {
	for _, text := range []string{
		"disabled answers 501 when it is off.",
		"while vulnerabilities.enabled is false",
		"never longer than server.shutdown_timeout minus one second",
		"answers vulnerability_data_unavailable",
		"an extraHosts entry with sendCredentials",
		"null is the remote's upstream URL",
	} {
		if len(unplain(text)) == 0 {
			t.Errorf("unplain(%q) found nothing", text)
		}
	}
	for _, text := range []string{
		"How long to wait for the answer, 1 to 300 seconds, written like 30s.",
		"The largest audit request trove accepts, 1 KiB to 10 MiB, written like 1MiB.",
		"The platforms to keep, such as linux/amd64 or linux/arm64/v8.",
		"The upstream's public signing keys, pasted as one armored OpenPGP block of up to 32 keys.",
	} {
		if got := unplain(text); len(got) != 0 {
			t.Errorf("unplain(%q) = %q, want nothing", text, got)
		}
	}
}
