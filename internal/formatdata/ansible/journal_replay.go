package ansible

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"regexp"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/formats/ansible/collection"
	"github.com/guided-traffic/trove/internal/formats/ansible/version"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The replay of ansible.version.deleted and ansible.tombstone.cleared entries after a restore (R-ANS-029, R-HA-054). The restore
// returned the repository to its snapshot, so a version that was deleted afterwards, possibly because its archive was malicious, is
// stored again. The replay only restricts: a deleted number is WITHHELD again by its tombstone and the restored row is dropped, so the
// documents do not list it and the serve check refuses the archive. Nothing is deleted from the store (R-HA-054): a forged or mistaken
// entry costs the availability of one version until an administrator clears the tombstone, never data or access.
//
// An entry is a claim to be checked against the restored database, not a fact: it acts only on a local Ansible Galaxy repository the
// entry names and on a collection name, a version and a digest of the valid forms.

func init() {
	journal.Register(journal.Handler{Domain: "ansible", Events: []string{audit.EventAnsibleVersionDeleted}, Apply: replayVersionDeleted})
	journal.Register(journal.Handler{Domain: "ansible", Events: []string{audit.EventAnsibleTombstoneCleared}, Apply: replayTombstoneCleared})
}

type entry struct {
	Repository int64  `json:"repository"`
	Name       string `json:"name"`
	Version    string `json:"version"`
	Stored     string `json:"stored"`
	SHA256     string `json:"sha256"`
	ActorID    string `json:"actorId"`
}

var (
	sha256Text = regexp.MustCompile(`^[0-9a-f]{64}$`)
	uuidText   = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

func decode(raw json.RawMessage, needDigest bool) (entry, string) {
	var e entry
	if err := json.Unmarshal(raw, &e); err != nil {
		return e, "the entry is not an Ansible Galaxy entry"
	}
	if _, _, ok := collection.SplitName(e.Name); !ok || e.Repository <= 0 || !version.Valid(e.Version) {
		return e, "the entry is incomplete: no repository, collection or version"
	}
	if needDigest && !sha256Text.MatchString(e.SHA256) {
		return e, "the entry is incomplete: no digest"
	}
	return e, ""
}

func skipped(res journal.Result, why string) journal.Result {
	res.Outcome = journal.Skipped
	res.Detail, _ = json.Marshal(map[string]string{"reason": why}) //nolint:errcheck // a map of strings marshals
	return res
}

// replayVersionDeleted applies one ansible.version.deleted entry.
func replayVersionDeleted(ctx context.Context, tx meta.Tx, _ journal.Env, o journal.Object) (journal.Result, error) {
	res := journal.Result{ID: o.ID, Event: o.Event}
	e, why := decode(o.Entry, true)
	if why != "" {
		return skipped(res, why), nil
	}
	res.Subject = e.Name + " " + e.Version
	qs, err := txQueries(tx)
	if err != nil {
		return res, err
	}
	repos, err := qs.AnsibleReplayRepository(ctx, e.Repository)
	if err != nil {
		return res, err
	}
	if len(repos) == 0 {
		res.Outcome = journal.SubjectUnknown
		return res, nil
	}
	ref := repos[0].Ref
	res.Subject = ref + " " + e.Name + " " + e.Version
	sum, _ := hex.DecodeString(e.SHA256) // sha256Text admits only 64 hexadecimal digits
	by := noActor
	if uuidText.MatchString(e.ActorID) {
		by = e.ActorID
	}
	// The restored row of the number is withheld: dropped, so no document lists it.
	stored := e.Stored
	if stored == "" {
		stored = e.Version
	}
	rowsGone, err := qs.AnsibleDeleteVersion(ctx, q.AnsibleDeleteVersionParams{RepoID: e.Repository, Name: e.Name, Version: stored})
	if err != nil {
		return res, err
	}
	n, err := qs.AnsibleInsertTombstone(ctx, q.AnsibleInsertTombstoneParams{RepoID: e.Repository, Name: e.Name, Version: e.Version, Sha256: sum, DeletedBy: by})
	if err != nil {
		return res, err
	}
	if n == 0 && len(rowsGone) == 0 {
		res.Outcome = journal.AlreadyInEffect
		return res, nil
	}
	if len(rowsGone) > 0 {
		if _, err := qs.AnsibleBumpPackage(ctx, q.AnsibleBumpPackageParams{RepoID: e.Repository, Name: e.Name}); err != nil {
			return res, err
		}
	}
	res.Outcome = journal.Applied
	res.Detail, err = json.Marshal(map[string]string{"withheld": "true", "repository": ref, "name": e.Name, "version": e.Version, "sha256": e.SHA256})
	return res, err
}

// replayTombstoneCleared applies one ansible.tombstone.cleared entry: a restore from a backup that still holds the tombstone clears it
// again, as the administrator did.
func replayTombstoneCleared(ctx context.Context, tx meta.Tx, _ journal.Env, o journal.Object) (journal.Result, error) {
	res := journal.Result{ID: o.ID, Event: o.Event}
	e, why := decode(o.Entry, false)
	if why != "" {
		return skipped(res, why), nil
	}
	res.Subject = e.Name + " " + e.Version
	qs, err := txQueries(tx)
	if err != nil {
		return res, err
	}
	repos, err := qs.AnsibleReplayRepository(ctx, e.Repository)
	if err != nil {
		return res, err
	}
	if len(repos) == 0 {
		res.Outcome = journal.SubjectUnknown
		return res, nil
	}
	ref := repos[0].Ref
	res.Subject = ref + " " + e.Name + " " + e.Version
	n, err := qs.AnsibleDeleteTombstone(ctx, q.AnsibleDeleteTombstoneParams{RepoID: e.Repository, Name: e.Name, Version: e.Version})
	if err != nil {
		return res, err
	}
	res.Outcome = journal.AlreadyInEffect
	if n > 0 {
		res.Outcome = journal.Applied
		res.Detail, err = json.Marshal(map[string]string{"cleared": "true", "repository": ref, "name": e.Name, "version": e.Version})
	}
	return res, err
}
