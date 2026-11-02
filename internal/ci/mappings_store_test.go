package ci

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/ci/claims"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
)

// Q-CI-30: every violation is in errors[] with its pointer and code, and a rule the specification names by its
// code is also the problem's code; the others answer validation_failed.
func TestQ_CI_30_AMappingViolationIsShapedLikeTheRestOfTheAPI(t *testing.T) {
	err := mappingViolation([]errs.FieldError{
		field("/name", "invalid_name", "bad name"),
		field("/match", errs.CodePinRequired, "a pin"),
		field("/grants", errs.CodeEmptyGrant, "nothing"),
	})
	var e *errs.Error
	if !errorsAs(err, &e) || e.Code != errs.CodePinRequired || len(e.Fields) != 3 || e.Kind != errs.Invalid {
		t.Fatalf("code %v, fields %d: the first named rule decides the code, every violation stays in errors[]", e, len(e.Fields))
	}
	err = mappingViolation([]errs.FieldError{field("/name", "invalid_name", "bad name")})
	if !errorsAs(err, &e) || e.Code != errs.CodeValidation || e.Detail != "bad name" {
		t.Errorf("an unnamed violation: %v", e)
	}
	if mappingViolation(nil) != nil {
		t.Error("no violation is no error")
	}
}

func errorsAs(err error, target **errs.Error) bool {
	e, ok := errs.As(err)
	if ok {
		*target = e
	}
	return ok
}

// R-CI-009: the shape rules that need no database.
func TestR_CI_009_TheShapeOfAMapping(t *testing.T) {
	s := &MappingService{o: MappingsOptions{Config: config.CI{Token: config.CIToken{MaxTTL: time.Hour}}}}
	over, ok := int64(7200), int64(60)
	for name, tc := range map[string]struct {
		in   MappingInput
		want []string
	}{
		"a valid mapping":              {MappingInput{Name: "release-publish", Priority: 100, MaxTTLSeconds: &ok}, nil},
		"a short name":                 {MappingInput{Name: "a", Priority: 1}, []string{"/name"}},
		"an uppercase name":            {MappingInput{Name: "Release", Priority: 1}, []string{"/name"}},
		"a priority of zero":           {MappingInput{Name: "release", Priority: 0}, []string{"/priority"}},
		"a priority above the max":     {MappingInput{Name: "release", Priority: 1000001}, []string{"/priority"}},
		"a lifetime above the ceiling": {MappingInput{Name: "release", Priority: 1, MaxTTLSeconds: &over}, []string{"/maxTtlSeconds"}},
	} {
		t.Run(name, func(t *testing.T) {
			got := s.shapeFields(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("fields %v, want %v", got, tc.want)
			}
			for i, f := range got {
				if f.Pointer != tc.want[i] {
					t.Errorf("field %d: %s, want %s", i, f.Pointer, tc.want[i])
				}
			}
		})
	}
	enabled, kinds, opts := withDefaults(MappingInput{})
	if !enabled || len(kinds) != 1 || kinds[0] != SubjectIDToken || opts.AllowUnprotectedRefs || opts.CrossProjectSource != "" {
		t.Errorf("defaults: %v %v %+v", enabled, kinds, opts)
	}
}

func sampleMapping(enabled bool) Mapping {
	return Mapping{
		ID: "m1", IssuerID: "i1", Issuer: "gitlab-main", Name: "publish", Tier: ProjectTier("web"), Project: "web", Priority: 10,
		Enabled: enabled, SubjectKinds: []string{SubjectIDToken}, Version: 3,
		Grants: claims.Spec{Policies: []string{"ci-read"}}, Options: claims.Options{CrossProjectSource: claims.CrossProjectDeny},
	}
}

// R-CI-020: a PUT writes the one event its enabled change picks.
func TestR_CI_020_TheEventOfAPut(t *testing.T) {
	on, off := sampleMapping(true), sampleMapping(false)
	for _, tc := range []struct {
		before, after Mapping
		want          string
	}{
		{on, off, audit.EventCIMappingDisabled},
		{off, on, audit.EventCIMappingEnabled},
		{on, on, audit.EventCIMappingUpdated},
		{off, off, audit.EventCIMappingUpdated},
	} {
		if got := mappingReplaceEvent(tc.before, tc.after); got != tc.want {
			t.Errorf("enabled %v to %v: %s, want %s", tc.before.Enabled, tc.after.Enabled, got, tc.want)
		}
	}
}

// R-CI-020: the events carry the record before and after, the project of a project's mapping, and the journal
// entries of the three events that remove or change access.
func TestR_CI_020_TheMappingEventsAndTheirJournalEntries(t *testing.T) {
	before, after := sampleMapping(true), sampleMapping(false)
	after.Version = 4
	actor := audit.Actor{Kind: "user", Name: "ops"}
	for name, tc := range map[string]struct {
		ev      audit.Event
		journal bool
	}{
		audit.EventCIMappingCreated:  {mappingEvent(audit.EventCIMappingCreated, actor, nil, &after, false), false},
		audit.EventCIMappingEnabled:  {mappingEvent(audit.EventCIMappingEnabled, actor, &before, &after, false), false},
		audit.EventCIMappingUpdated:  {mappingEvent(audit.EventCIMappingUpdated, actor, &before, &after, false), true},
		audit.EventCIMappingDisabled: {mappingEvent(audit.EventCIMappingDisabled, actor, &before, &after, false), true},
		audit.EventCIMappingDeleted:  {mappingEvent(audit.EventCIMappingDeleted, actor, &before, nil, true), true},
	} {
		if (tc.ev.Journal != nil) != tc.journal {
			t.Errorf("%s: journal entry present %v, want %v", name, tc.ev.Journal != nil, tc.journal)
		}
		var d map[string]any
		if err := json.Unmarshal(tc.ev.Details, &d); err != nil {
			t.Fatal(err)
		}
		if d["project"] != "web" || d["issuer"] != "gitlab-main" || d["tier"] != "project:web" || d["priority"] != float64(10) {
			t.Errorf("%s: details %v", name, d)
		}
		if tc.journal {
			var j map[string]any
			if err := json.Unmarshal(tc.ev.Journal, &j); err != nil {
				t.Fatal(err)
			}
			for _, k := range []string{"mappingId", "issuerId", "issuer", "name", "tier", "priority", "before"} {
				if _, has := j[k]; !has {
					t.Errorf("%s: the journal entry lacks %s: %v", name, k, j)
				}
			}
			if _, has := j["after"]; has != (name == audit.EventCIMappingUpdated) {
				t.Errorf("%s: after in the journal entry: %v", name, has)
			}
		}
	}
	del := mappingEvent(audit.EventCIMappingDeleted, actor, &before, nil, true)
	var d map[string]any
	_ = json.Unmarshal(del.Details, &d)
	if d["cascade"] != true || d["after"] != nil {
		t.Errorf("a cascaded delete: %v", d)
	}
}

func TestMappingLabelIsNeverATokenAndAnonymousIsNamed(t *testing.T) {
	if got := mappingLabel(authz.Subject{Name: "ops", Kind: "user"}); got != "user:ops" {
		t.Errorf("label = %q", got)
	}
	if got := mappingLabel(authz.Subject{Anonymous: true}); got != "anonymous" {
		t.Errorf("label = %q", got)
	}
	if got := mappingLabel(authz.Subject{}); got != "system" {
		t.Errorf("label = %q", got)
	}
}
