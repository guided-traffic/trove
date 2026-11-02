package project

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// payloadVersion is the version of the change payloads of this package, for both
// areas it notifies.
const payloadVersion = 1

// authzChange is the payload of an authz change: the type and ID of the changed
// object, never a grant (R-AUTHZ-028).
type authzChange struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// repoChange is the payload of a repo_config change, the shape the repository
// package writes (internal/repository changePayload): the repository id and the
// config_version the write left (R-REPO-008).
type repoChange struct {
	ID      string `json:"id"`
	Key     string `json:"key"`
	Op      string `json:"op"`
	Version int64  `json:"version"`
}

// opUpdated is the repo_config op of an assignment: the repository's document
// changed (its project member), which is what an "updated" says.
const opUpdated = "updated"

var errNoEpochRow = errors.New("project: the authz epoch row is missing")

func queriesOf(tx meta.Tx) (*q.Queries, error) {
	ptx, ok := postgres.Unwrap(tx)
	if !ok {
		return nil, errors.New("project: the transaction does not come from the PostgreSQL store")
	}
	return q.New(ptx), nil
}

// inTx runs fn in a transaction with the queries bound to it.
func (s *service) inTx(ctx context.Context, fn func(tx meta.Tx, qs *q.Queries) error) error {
	return s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queriesOf(tx)
		if err != nil {
			return err
		}
		return fn(tx, qs)
	})
}

// afterEpoch registers a hook that runs after the change log wrote its note in
// this transaction: it reads the epoch the transaction produced (the row is locked
// until commit, so the value is ours) and lets fn use it. Hooks run in
// registration order, so call it after the Notify.
func afterEpoch(tx meta.Tx, epoch *int64, fn func(ctx context.Context, tx meta.Tx, epoch int64) error) {
	tx.Before(func(ctx context.Context, tx meta.Tx) error {
		qs, err := queriesOf(tx)
		if err != nil {
			return err
		}
		n, err := epochOf(ctx, qs)
		if err != nil {
			return err
		}
		*epoch = n
		if fn == nil {
			return nil
		}
		return fn(ctx, tx, n)
	})
}

func epochOf(ctx context.Context, qs *q.Queries) (int64, error) {
	rows, err := qs.ListClusterEpochs(ctx, []string{authz.AreaAuthz})
	if err != nil {
		return 0, err
	}
	if len(rows) != 1 {
		return 0, errNoEpochRow
	}
	return rows[0].Epoch, nil
}

func (s *service) notifyAuthz(tx meta.Tx, typ, id string) error {
	return s.o.Changes.Notify(tx, authz.AreaAuthz, payloadVersion, authzChange{Type: typ, ID: id})
}

func notFound(key string) error {
	return errs.E(errs.NotFound, errs.CodeNotFound, fmt.Sprintf("The project %q does not exist.", key))
}

func actorID(sub authz.Subject) string {
	if sub.Anonymous {
		return ""
	}
	return sub.ID
}

func actorOf(sub authz.Subject) audit.Actor {
	if sub.Anonymous {
		return audit.Actor{Kind: string(principal.KindAnonymous)}
	}
	kind := string(sub.Kind)
	if kind == "" {
		kind = string(principal.KindUser)
	}
	return audit.Actor{Kind: kind, ID: sub.ID, Name: sub.Name, TokenID: sub.TokenID, AuthMethod: sub.AuthMethod}
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil { // the values are strings, numbers, booleans and slices of them
		panic(err)
	}
	return b
}

// newID returns a UUIDv7 (RFC 9562), as the other authorization objects do
// (docs/developer/database.md "Access-core tables").
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil { // crypto/rand does not fail on supported platforms
		panic(err)
	}
	ms := uint64(time.Now().UnixMilli()) //nolint:gosec // after 1970
	b[0], b[1], b[2], b[3], b[4], b[5] = byte(ms>>40), byte(ms>>32), byte(ms>>24), byte(ms>>16), byte(ms>>8), byte(ms)
	b[6] = b[6]&0x0f | 0x70
	b[8] = b[8]&0x3f | 0x80
	var s [36]byte
	hex.Encode(s[0:8], b[0:4])
	s[8] = '-'
	hex.Encode(s[9:13], b[4:6])
	s[13] = '-'
	hex.Encode(s[14:18], b[6:8])
	s[18] = '-'
	hex.Encode(s[19:23], b[8:10])
	s[23] = '-'
	hex.Encode(s[24:], b[10:])
	return string(s[:])
}

// refOfRow renders the reference of a principal-table row (kind as stored); empty
// for no row.
func refOfRow(kind, name string) string {
	switch kind {
	case "":
		return ""
	case "service_account":
		return "sa:" + name
	case "ci_mapping":
		return "ci:" + name
	}
	return kind + ":" + name
}

// head is the project row as both read statements return it.
type head = q.ProjectGetRow

func toProject(h head, admins []PrincipalRef) Project {
	return Project{
		ID: h.ID, Key: h.Key, DisplayName: h.DisplayName, Description: h.Description,
		Rights:       Rights{ManagePolicies: h.ManagePolicies, ManageRepositories: h.ManageRepositories},
		Admins:       admins,
		Repositories: int(h.Repositories), ServiceAccounts: int(h.ServiceAccounts), Policies: int(h.Policies), CIMappings: int(h.CiMappings),
		Version:   h.Version,
		CreatedAt: time.UnixMicro(h.CreatedUs).UTC(), CreatedBy: refOfRow(h.CreatedByKind, h.CreatedByName),
		UpdatedAt: time.UnixMicro(h.UpdatedUs).UTC(), UpdatedBy: refOfRow(h.UpdatedByKind, h.UpdatedByName),
	}
}

// load reads one project with its administrators.
func load(ctx context.Context, qs *q.Queries, key string) (Project, error) {
	rows, err := qs.ProjectGet(ctx, key)
	if err != nil {
		return Project{}, err
	}
	if len(rows) == 0 {
		return Project{}, notFound(key)
	}
	adm, err := qs.ProjectAdmins(ctx, key)
	if err != nil {
		return Project{}, err
	}
	admins := make([]PrincipalRef, 0, len(adm))
	for _, a := range adm {
		admins = append(admins, PrincipalRef{Kind: PrincipalKind(a.PrincipalKind), Name: a.PrincipalName, ID: a.PrincipalID})
	}
	return toProject(rows[0], admins), nil
}

// switches is the access-bearing state of a project: what the journal entry of an
// update compares (R-AUTHZ-031).
type switches struct {
	ManagePolicies     bool `json:"managePolicies"`
	ManageRepositories bool `json:"manageRepositories"`
}

// state is the state of a project an event records before and after a change.
type state struct {
	DisplayName string `json:"displayName"`
	Description string `json:"description"`
	switches
}

func stateOf(p Project) *state {
	return &state{
		DisplayName: p.DisplayName, Description: p.Description,
		switches: switches{ManagePolicies: p.ManagePolicies, ManageRepositories: p.ManageRepositories},
	}
}

func subjectOf(p Project) audit.Subject { return audit.Subject{Kind: "project", ID: p.ID, Name: p.Key} }

// projectEvent builds the event of a write to the project itself. The journal
// entry of an update carries the two switches before and after, that of a delete
// the ID and key (R-AUTHZ-031).
func projectEvent(name string, actor authz.Subject, p Project, before, after *state, epoch int64, policies []string) audit.Event {
	details := map[string]any{"epoch": epoch, "project": p.Key, "projectId": p.ID, "version": p.Version, "before": before, "after": after}
	if len(policies) > 0 {
		details["policiesDeleted"] = policies
	}
	ev := audit.Event{
		Type: name, Actor: actorOf(actor), Outcome: audit.Success, Subject: subjectOf(p), Details: mustJSON(details),
	}
	switch name {
	case audit.EventProjectUpdated:
		ev.Journal = mustJSON(map[string]any{
			"projectId": p.ID, "project": p.Key, "before": before.switches, "after": after.switches,
		})
	case audit.EventProjectDeleted:
		ev.Journal = mustJSON(map[string]any{"projectId": p.ID, "project": p.Key})
	}
	return ev
}

func adminEvent(name string, actor authz.Subject, p Project, ref PrincipalRef, epoch int64) audit.Event {
	ev := audit.Event{
		Type: name, Actor: actorOf(actor), Outcome: audit.Success, Subject: subjectOf(p),
		Details: mustJSON(map[string]any{
			"epoch": epoch, "project": p.Key, "projectId": p.ID, "principal": ref.String(), "principalId": ref.ID,
		}),
	}
	if name == audit.EventProjectAdminRemoved {
		ev.Journal = mustJSON(map[string]any{"projectId": p.ID, "project": p.Key, "principalId": ref.ID, "principal": ref.String()})
	}
	return ev
}

// repoMove names the repository of an assignment event and, for a move, the other
// project.
type repoMove struct {
	ID, Key string
	Moved   bool
	Other   string
}

func assignmentEvent(name string, actor authz.Subject, p Project, r repoMove, epoch int64) audit.Event {
	details := map[string]any{
		"epoch": epoch, "project": p.Key, "projectId": p.ID, "repository": r.Key, "repositoryId": r.ID, "moved": r.Moved,
	}
	if r.Other != "" {
		details["otherProject"] = r.Other
	}
	ev := audit.Event{
		Type: name, Actor: actorOf(actor), Outcome: audit.Success, Subject: subjectOf(p), Repository: r.Key,
		Details: mustJSON(details),
	}
	if name == audit.EventProjectRepositoryUnassigned {
		ev.Journal = mustJSON(map[string]any{"projectId": p.ID, "project": p.Key, "repositoryId": r.ID, "repository": r.Key})
	}
	return ev
}
