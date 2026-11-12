package authz

import (
	"encoding/json"
	"testing"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/repository"
)

// TestR_AUTHZ_036_TheLevelsAreSettingsOfTheJournal: the settings comparator orders the two levels private, internal, public, so a
// replay applies a lowering and never a raise (R-AUTHZ-036).
func TestR_AUTHZ_036_TheLevelsAreSettingsOfTheJournal(t *testing.T) {
	cur := repoSettings{settingArtifactsLevel: "internal", settingWikiLevel: "internal"}
	cases := []struct {
		name  string
		after repoSettings
		want  bool
	}{
		{"a lowering narrows", repoSettings{settingArtifactsLevel: "private"}, true},
		{"the same level narrows", repoSettings{settingArtifactsLevel: "internal"}, true},
		{"a raise does not", repoSettings{settingArtifactsLevel: "public"}, false},
		{"one part lower and one higher does not", repoSettings{settingArtifactsLevel: "private", settingWikiLevel: "public"}, false},
		{"a value that is no level does not", repoSettings{settingWikiLevel: "open"}, false},
	}
	for _, c := range cases {
		t.Run("R-AUTHZ-036 "+c.name, func(t *testing.T) {
			if got := (settingsOrder{}).Narrows(cur, c.after); got != c.want {
				t.Errorf("Narrows = %v, want %v", got, c.want)
			}
		})
	}
}

// TestR_AUTHZ_036_TheSettingsHookNamesTheLevelsThatChanged: authz.settings.changed of a repository update names the members that
// changed, before and after, and the cause; the journal entry carries the same (R-AUTHZ-036, R-AUTHZ-031).
func TestR_AUTHZ_036_TheSettingsHookNamesTheLevelsThatChanged(t *testing.T) {
	before := repository.Repository{HideExistence: "inherit", Visibility: repository.Visibility{Artifacts: "public", Wiki: "internal"}}
	after := before
	after.Visibility.Artifacts = "internal"
	b, a := changedSettings(settingsOf(before), settingsOf(after))
	if len(b) != 1 || b[settingArtifactsLevel] != "public" || a[settingArtifactsLevel] != "internal" {
		t.Fatalf("before %v after %v: only the level that changed is named", b, a)
	}
	if len(a) != 1 {
		t.Errorf("after names %v", a)
	}
}

// TestR_AUTHZ_036_AnEventOfTheCascadeCarriesTheCause: the lowered repository of a project or a move is recorded with its cause and
// a journal entry that names the repository by ID.
func TestR_AUTHZ_036_AnEventOfTheCascadeCarriesTheCause(t *testing.T) {
	ev := LevelsChangedEvent(audit.Actor{Kind: "user", Name: "root"}, "web/npm-public", "17",
		RepositoryLevels{Artifacts: "public", Wiki: "internal"}, RepositoryLevels{Artifacts: "private", Wiki: "private"}, CauseProject, 42)
	if ev.Type != EventSettingsChanged || ev.Repository != "web/npm-public" {
		t.Fatalf("%+v", ev)
	}
	var details struct {
		Cause  string            `json:"cause"`
		Epoch  int64             `json:"epoch"`
		Before map[string]string `json:"before"`
		After  map[string]string `json:"after"`
	}
	if err := json.Unmarshal(ev.Details, &details); err != nil || details.Cause != "project" || details.Epoch != 42 ||
		details.Before[settingArtifactsLevel] != "public" || details.After[settingWikiLevel] != "private" {
		t.Errorf("details %s: %v", ev.Details, err)
	}
	var entry struct {
		RepositoryID string            `json:"repositoryId"`
		Before       map[string]string `json:"before"`
	}
	if err := json.Unmarshal(ev.Journal, &entry); err != nil || entry.RepositoryID != "17" || entry.Before[settingWikiLevel] != "internal" {
		t.Errorf("journal %s: %v", ev.Journal, err)
	}
	same := LevelsChangedEvent(audit.Actor{}, "web/x", "1", RepositoryLevels{"public", "public"}, RepositoryLevels{"public", "internal"}, CauseMove, 1)
	var d2 struct {
		After map[string]string `json:"after"`
	}
	_ = json.Unmarshal(same.Details, &d2)
	if len(d2.After) != 1 || d2.After[settingWikiLevel] != "internal" {
		t.Errorf("only the part that changed is named: %s", same.Details)
	}
}
