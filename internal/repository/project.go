package repository

import (
	"context"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The project side of the repository writes (R-AUTHZ-026, Q-REPO-212). A repository belongs to the
// project of its path from its creation; a move is the project service's.

// lockProject locks the project a create adds the repository to and returns its id. The project is
// held FOR SHARE: a concurrent delete of the project (FOR UPDATE, which counts the repositories under
// that lock) waits for this transaction, so the new repository is counted or the project is gone. A
// project that does not exist answers 404 like a missing repository.
func lockProject(ctx context.Context, qs *q.Queries, project string) (string, error) {
	rows, err := qs.RepositoryProjectShare(ctx, project)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", errs.E(errs.NotFound, errs.CodeNotFound, "The project does not exist.")
	}
	return rows[0].ID, nil
}

// projectKeys maps the id of every project to its key.
func projectKeys(ctx context.Context, qs *q.Queries) (map[string]string, error) {
	rows, err := qs.RepositoryProjectKeys(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.ID] = r.Key
	}
	return out, nil
}

// projectKeyOf resolves the project of a row to its key.
func projectKeyOf(ctx context.Context, qs *q.Queries, row q.Repository) (string, error) {
	keys, err := projectKeys(ctx, qs)
	if err != nil {
		return "", err
	}
	return keys[row.ProjectID.String()], nil
}
