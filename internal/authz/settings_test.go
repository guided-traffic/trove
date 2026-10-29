package authz

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/repository"
)

// txRecorder keeps the events written in a transaction.
type txRecorder struct{ events []audit.Event }

func (r *txRecorder) RecordTx(_ context.Context, _ meta.Tx, ev audit.Event) error {
	r.events = append(r.events, ev)
	return nil
}
func (r *txRecorder) Record(context.Context, audit.Event) {}

func settingsRepo(class repository.Class, requireAuth bool, hide, config string) repository.Repository {
	return repository.Repository{ID: "12", Key: "r", Class: class, Format: "generic", RequireAuthentication: requireAuth, HideExistence: hide, Config: []byte(config)}
}

func TestR_AUTHZ_031_SettingsChangedIsRecordedByTheRepositoryUpdate(t *testing.T) {
	caller := repository.Caller{Actor: audit.Actor{Kind: "user", ID: "u1", Name: "sec-alice"}, Source: audit.Source{ClientAddr: "10.0.0.1"}}
	cases := []struct {
		name          string
		before, after repository.Repository
		want          string // the changed members as JSON, "" for no event
	}{
		{"nothing changed", settingsRepo(repository.Local, false, "inherit", `{}`), settingsRepo(repository.Local, false, "inherit", `{"description":"x"}`), ""},
		{
			"requireAuthentication", settingsRepo(repository.Local, false, "inherit", `{}`), settingsRepo(repository.Local, true, "inherit", `{}`),
			`{"before":{"requireAuthentication":false},"after":{"requireAuthentication":true}}`,
		},
		{
			"hideExistence", settingsRepo(repository.Local, false, "", `{}`), settingsRepo(repository.Local, false, "false", `{}`),
			`{"before":{"hideExistence":"inherit"},"after":{"hideExistence":"false"}}`,
		},
		{
			"packageOwnership set", settingsRepo(repository.Local, false, "inherit", `{"local":{}}`), settingsRepo(repository.Local, false, "inherit", `{"local":{"packageOwnership":"off"}}`),
			`{"before":{"packageOwnership":null},"after":{"packageOwnership":"off"}}`,
		},
		{
			"cacheFillAction of a remote repository, default read", settingsRepo(repository.Remote, false, "inherit", `{}`), settingsRepo(repository.Remote, false, "inherit", `{"remote":{"cacheFillAction":"deploy"}}`),
			`{"before":{"cacheFillAction":"read"},"after":{"cacheFillAction":"deploy"}}`,
		},
		{"the cache fill of a local repository is not a setting", settingsRepo(repository.Local, false, "inherit", `{}`), settingsRepo(repository.Local, false, "inherit", `{"remote":{"cacheFillAction":"deploy"}}`), ""},
		{
			"two settings at once", settingsRepo(repository.Local, false, "inherit", `{}`), settingsRepo(repository.Local, true, "true", `{}`),
			`{"before":{"hideExistence":"inherit","requireAuthentication":false},"after":{"hideExistence":"true","requireAuthentication":true}}`,
		},
	}
	for _, tc := range cases {
		t.Run("R-AUTHZ-031 "+tc.name, func(t *testing.T) {
			rec := &txRecorder{}
			if err := NewSettingsHook(rec).OnUpdated(t.Context(), nil, caller, tc.before, tc.after); err != nil {
				t.Fatal(err)
			}
			evs := rec.events
			if tc.want == "" {
				if len(evs) != 0 {
					t.Fatalf("%+v", evs)
				}
				return
			}
			if len(evs) != 1 {
				t.Fatalf("%d events", len(evs))
			}
			ev := evs[0]
			if ev.Type != EventSettingsChanged || ev.Repository != "r" || ev.Actor.Name != "sec-alice" || ev.Source.ClientAddr != "10.0.0.1" || ev.Outcome != audit.Success {
				t.Fatalf("%+v", ev)
			}
			// The journal entry names the repository by ID and carries only the changed settings.
			var j struct {
				RepositoryID string         `json:"repositoryId"`
				Before       map[string]any `json:"before"`
				After        map[string]any `json:"after"`
			}
			if err := json.Unmarshal(ev.Journal, &j); err != nil || j.RepositoryID != "12" {
				t.Fatalf("journal %s: %v", ev.Journal, err)
			}
			got, _ := json.Marshal(struct {
				Before map[string]any `json:"before"`
				After  map[string]any `json:"after"`
			}{j.Before, j.After})
			if string(got) != tc.want {
				t.Fatalf("journal changes %s, want %s", got, tc.want)
			}
		})
	}
}
