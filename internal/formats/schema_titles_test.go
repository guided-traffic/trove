package formats_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// settingSchema is the part of a format's settings schema the forms show: a member's title, its description and its own members.
type settingSchema struct {
	Title       string                   `json:"title"`
	Description string                   `json:"description"`
	Properties  map[string]settingSchema `json:"properties"`
}

// specID is a requirement or decision ID of the specification (R-NPM-026, Q-DEB-17): a text for users names none.
var specID = regexp.MustCompile(`\b[RQ]-[A-Z]+-[0-9]+[a-z]?\b`)

// TestR_UI_088_EverySettingOfAFormatHasATitleAndPlainTexts: the settings forms label a member with its schema's title and show its
// description under it (Q-UI-79), so every member of every format's settings schema has a title, and no title or description
// of the schema, the root's included, names a requirement or decision ID, which means nothing to the user who reads it.
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
		checkTexts(t, file, "the root", schema, false)
		checkMembers(t, file, "", schema.Properties)
	}
}

func checkMembers(t *testing.T, file, prefix string, properties map[string]settingSchema) {
	t.Helper()
	for name, member := range properties {
		checkTexts(t, file, prefix+name, member, true)
		checkMembers(t, file, prefix+name+".", member.Properties)
	}
}

func checkTexts(t *testing.T, file, where string, member settingSchema, titled bool) {
	t.Helper()
	if titled && member.Title == "" {
		t.Errorf("%s: the member %s has no title", file, where)
	}
	for _, text := range []string{member.Title, member.Description} {
		if id := specID.FindString(text); id != "" {
			t.Errorf("%s: %s names %s in a text for users: %q", file, where, id, text)
		}
	}
}
