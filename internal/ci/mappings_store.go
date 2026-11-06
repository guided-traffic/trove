package ci

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/ci/claims"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The rows of the identity mappings: the mapping between the queries of ci_mappings.sql and [Mapping], the
// stored JSON forms and the record that the audit events carry.

// principalNameOf is the name of a mapping's principal (R-CI-009): "<issuer>/<name>", or
// "<issuer>/<project>/<name>" for a mapping of a project's tier (Q-AUTHZ-214).
func principalNameOf(issuer, project, name string) string {
	return strings.TrimPrefix(claims.MappingRef(issuer, project, name), "ci:")
}

// toMapping maps the columns every read statement of a mapping returns. The stored match, grants and
// options are the wire forms of internal/ci/claims; a value that does not decode is a storage fault.
// The policies of the mapping are not in the row: the binding lives in the policy's principal list
// (R-CI-013) and the caller fills Grants.Policies from it.
func toMapping(r q.CiMappingListRow) (Mapping, error) {
	m := Mapping{
		ID: r.ID, IssuerID: r.IssuerID, Issuer: r.Issuer, PrincipalID: r.PrincipalID, Name: r.Name, Tier: r.Tier,
		ProjectID: r.ProjectID, Priority: int(r.Priority), Enabled: r.Enabled, SubjectKinds: r.SubjectKinds,
		Description: r.Description, Version: r.Version, CreatedBy: r.CreatedBy,
		CreatedAt: time.UnixMicro(r.CreatedUs).UTC(), UpdatedAt: time.UnixMicro(r.UpdatedUs).UTC(),
	}
	if key, ok := strings.CutPrefix(r.Tier, "project:"); ok {
		m.Project = key
	}
	m.Ref = claims.MappingRef(r.Issuer, m.Project, r.Name)
	if r.MaxTtlSecs > 0 {
		v := r.MaxTtlSecs
		m.MaxTTLSeconds = &v
	}
	if err := json.Unmarshal(r.Match, &m.Match); err != nil {
		return Mapping{}, fmt.Errorf("ci: the stored condition of the mapping %s/%s does not decode: %w", r.Issuer, r.Name, err)
	}
	if err := json.Unmarshal(r.Grants, &m.Grants); err != nil {
		return Mapping{}, fmt.Errorf("ci: the stored grants of the mapping %s/%s do not decode: %w", r.Issuer, r.Name, err)
	}
	if err := json.Unmarshal(r.Options, &m.Options); err != nil {
		return Mapping{}, fmt.Errorf("ci: the stored options of the mapping %s/%s do not decode: %w", r.Issuer, r.Name, err)
	}
	if m.Options.CrossProjectSource == "" {
		m.Options.CrossProjectSource = claims.CrossProjectDeny
	}
	if len(m.SubjectKinds) == 0 {
		m.SubjectKinds = []string{SubjectIDToken}
	}
	return m, nil
}

// storedGrants is the stored form of the grants: the binding of the policies is the policy's, so it is not
// kept here (R-CI-013); a read fills it from the policies' principal lists.
func storedGrants(g claims.Spec) string {
	g.Policies = nil
	return string(mustJSON(g))
}

// optionsJSON is the stored form of the options.
func optionsJSON(o claims.Options) string { return string(mustJSON(o)) }

func conditionJSON(n claims.Node) string { return string(mustJSON(n)) }

// mappingRecord is the wire form of a mapping as the audit events carry it (R-CI-020): the members of
// the API, the resolved defaults, the version; it holds no secret.
type mappingRecord struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Issuer        string         `json:"issuer"`
	Tier          string         `json:"tier"`
	Project       string         `json:"project,omitempty"`
	Description   string         `json:"description"`
	Priority      int            `json:"priority"`
	Enabled       bool           `json:"enabled"`
	SubjectKinds  []string       `json:"subjectKinds"`
	Match         claims.Node    `json:"match"`
	Grants        claims.Spec    `json:"grants"`
	MaxTTLSeconds *int64         `json:"maxTtlSeconds"`
	Options       claims.Options `json:"options"`
	Version       int64          `json:"version"`
}

func recordOfMapping(m *Mapping) *mappingRecord {
	if m == nil {
		return nil
	}
	return &mappingRecord{
		ID: m.ID, Name: m.Name, Issuer: m.Issuer, Tier: m.Tier, Project: m.Project, Description: m.Description,
		Priority: m.Priority, Enabled: m.Enabled, SubjectKinds: m.SubjectKinds, Match: m.Match, Grants: m.Grants,
		MaxTTLSeconds: m.MaxTTLSeconds, Options: m.Options, Version: m.Version,
	}
}

// mappingEvent builds the event of a change of a mapping: the full record before and after (R-CI-020).
// The journal entry of .updated, .disabled and .deleted carries what replay needs (R-HA-054): the ID, the
// issuer, the name, the tier and the priority, and the record before (and, for .updated, after) the
// change.
func mappingEvent(name string, actor audit.Actor, before, after *Mapping, cascade bool) audit.Event {
	subj := after
	if subj == nil {
		subj = before
	}
	details := map[string]any{
		"id": subj.ID, "name": subj.Name, "issuer": subj.Issuer, "tier": subj.Tier, "priority": subj.Priority,
		"version": subj.Version, "before": recordOfMapping(before), "after": recordOfMapping(after),
	}
	if subj.Project != "" {
		details["project"] = subj.Project
	}
	if name == audit.EventCIMappingDeleted {
		details["cascade"] = cascade
	}
	ev := audit.Event{
		Type: name, Actor: actor, Outcome: audit.Success,
		Subject: audit.Subject{Kind: "ci_mapping", ID: subj.ID, Name: subj.PrincipalName()}, Details: mustJSON(details),
	}
	entry := map[string]any{
		"mappingId": subj.ID, "issuerId": subj.IssuerID, "issuer": subj.Issuer, "name": subj.Name, "tier": subj.Tier,
		"priority": subj.Priority, "before": recordOfMapping(before),
	}
	switch name {
	case audit.EventCIMappingUpdated:
		entry["after"] = recordOfMapping(after)
		ev.Journal = mustJSON(entry)
	case audit.EventCIMappingDisabled, audit.EventCIMappingDeleted:
		ev.Journal = mustJSON(entry)
	}
	return ev
}

// mappingReplaceEvent is the one event of a PUT, chosen by enabled (R-CI-020): .disabled when it changes
// true to false, .enabled when false to true, .updated otherwise.
func mappingReplaceEvent(before, after Mapping) string {
	switch {
	case before.Enabled && !after.Enabled:
		return audit.EventCIMappingDisabled
	case !before.Enabled && after.Enabled:
		return audit.EventCIMappingEnabled
	}
	return audit.EventCIMappingUpdated
}

// mappingLabel is what created_by shows: the caller as a reference, never a token.
func mappingLabel(sub Actor) string {
	switch {
	case sub.Anonymous:
		return "anonymous"
	case sub.Name != "":
		kind := string(sub.Kind)
		if kind == "" {
			kind = string(principal.KindUser)
		}
		return kind + ":" + sub.Name
	case sub.ID != "":
		return sub.ID
	}
	return "system"
}
