package authz

import (
	"context"
	"fmt"
	"strconv"

	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/repository"
)

// CauseRepositoryDeleted is the cause of an endpoint.exposure.deleted event that
// the removal of the rule's repository produced (R-EP-020).
const CauseRepositoryDeleted = "repository_deleted"

// ExposureHook implements [repository.Hooks] for the exposure rules: when a
// repository is deleted, its rules go with the row by the foreign key
// endpoint_exposure_rule.repo_id, and each one is recorded as
// endpoint.exposure.deleted with the cause repository_deleted, with its journal
// entry, in the transaction of the deletion (H-164). The rules stopped exposing
// when the repository was marked deleting, so the entry only completes the trail
// and the revocation journal; the change in the area authz makes every replica
// rebuild its grant index.
type ExposureHook struct{ svc *ExposureService }

var _ repository.Hooks = (*ExposureHook)(nil)

// NewExposureHook creates the hook over the service whose recorder and change
// notifier it uses.
func NewExposureHook(svc *ExposureService) *ExposureHook { return &ExposureHook{svc: svc} }

// OnUpdated implements [repository.Hooks]: an update leaves the rules alone (they
// refer to the repository by its ID).
func (h *ExposureHook) OnUpdated(context.Context, meta.Tx, repository.Caller, repository.Repository, repository.Repository) error {
	return nil
}

// OnDelete implements [repository.Hooks].
func (h *ExposureHook) OnDelete(ctx context.Context, tx meta.Tx, c repository.Caller, r repository.Repository) error {
	id, err := strconv.ParseInt(r.ID, 10, 64)
	if err != nil {
		return fmt.Errorf("authz: the repository id %q is no number: %w", r.ID, err)
	}
	qs, err := queriesOf(tx)
	if err != nil {
		return err
	}
	rows, err := qs.EndpointsListRulesOfRepository(ctx, id)
	if err != nil {
		return err
	}
	for _, row := range rows {
		rule, err := h.svc.ruleOf(listRow(q.EndpointsListRulesRow(row)))
		if err != nil {
			return err
		}
		if err := h.svc.o.Changes.Notify(tx, AreaAuthz, payloadVersion, changePayload{Type: "exposure_rule", ID: rule.ID}); err != nil {
			return err
		}
		var epoch int64
		before := stateOfRule(rule.ExposureRuleInput)
		afterEpoch(tx, &epoch, func(ctx context.Context, tx meta.Tx, epoch int64) error {
			ev := exposureEvent(EventExposureDeleted, Subject{}, rule, before, nil, nil, epoch, CauseRepositoryDeleted)
			ev.Actor, ev.Source, ev.Endpoint = c.Actor, c.Source, c.Endpoint
			return h.svc.o.Recorder.RecordTx(ctx, tx, ev)
		})
	}
	return nil
}
