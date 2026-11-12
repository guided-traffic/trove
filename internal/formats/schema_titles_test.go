package formats_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// settingSchema is the part of a format's settings schema the forms label: a member's title and its own members.
type settingSchema struct {
	Title      string                   `json:"title"`
	Properties map[string]settingSchema `json:"properties"`
}

// TestR_UI_088_EverySettingOfAFormatHasATitle: the settings forms label a member with its schema's title (Q-UI-79), so every
// member of every format's settings schema has one; a member without it would be shown under its raw key.
func TestR_UI_088_EverySettingOfAFormatHasATitle(t *testing.T) {
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
		checkTitles(t, file, "", schema.Properties)
	}
}

func checkTitles(t *testing.T, file, prefix string, properties map[string]settingSchema) {
	t.Helper()
	for name, member := range properties {
		if member.Title == "" {
			t.Errorf("%s: the member %s%s has no title", file, prefix, name)
		}
		checkTitles(t, file, prefix+name+".", member.Properties)
	}
}
