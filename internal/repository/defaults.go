package repository

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// maxSuggestion is the last number of a suggested key: <format>-<class>, then <format>-<class>-2 to -9 (R-REPO-063).
const maxSuggestion = 9

// Defaults is the answer of getRepositoryDefaults (R-REPO-063, Q-REPO-324): a key that no repository of the project holds, or
// nil when the ten suggestions are taken, and the document a create of only key, class and format in the project would store.
// Document.Key is empty: the document is the defaults, not a repository.
type Defaults struct {
	SuggestedKey *string
	Document     Document
}

// Defaults resolves what a create of only key, class and format in project would store, by the rules of that create (the
// defaults of every member, the class's section in its canonical form with the format's default preset for a remote, the
// visibility of R-AUTHZ-033 from the installation's default and the project's level), without writing a row, an audit event or a
// change. An unknown format or a class the format lacks is the 422 of a create; an unknown project is 404. The caller's
// permission is the API's (that of a create).
func (s *Service) Defaults(ctx context.Context, c Caller, project, format string, class Class) (Defaults, error) {
	key := suggestionBase(format, class)
	body, err := json.Marshal(map[string]string{"key": "defaults", "class": string(class), "format": format})
	if err != nil {
		return Defaults{}, err
	}
	d, err := DecodeDocument(body)
	if err != nil {
		return Defaults{}, err
	}
	_, v := validate(s.cat, &d, true)
	if err := v.err(); err != nil {
		return Defaults{}, err
	}
	var out Defaults
	err = s.run.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		rows, err := qs.RepositoryProjectLevel(ctx, project)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return errs.E(errs.NotFound, errs.CodeNotFound, "The project does not exist.")
		}
		levels, err := s.resolveLevels(ctx, qs, v, c, rows[0].Visibility, nil, &d)
		if err != nil {
			return err
		}
		if err := v.err(); err != nil {
			return err
		}
		cfg, err := d.marshalConfig()
		if err != nil {
			return err
		}
		doc, err := toDocument(q.Repository{
			Format: d.Format, Class: string(d.Class), Config: cfg,
			VisibilityArtifacts: levels.Artifacts, VisibilityWiki: levels.Wiki,
		}, s.cat)
		if err != nil {
			return err
		}
		doc.Project = project
		doc.renderVirtual(nil)
		out.Document = doc
		out.SuggestedKey, err = freeKey(ctx, qs, project, key)
		return err
	})
	if err != nil {
		return Defaults{}, err
	}
	return out, nil
}

// suggestionBase is the first suggested key of a format and a class, <format>-<class> (R-REPO-063).
func suggestionBase(format string, class Class) string { return format + "-" + string(class) }

// freeKey returns the first of base and base-2 to base-9 that no repository of the project holds, nil when all are taken.
func freeKey(ctx context.Context, qs *q.Queries, project, base string) (*string, error) {
	candidates := []string{base}
	for n := 2; n <= maxSuggestion; n++ {
		candidates = append(candidates, base+"-"+strconv.Itoa(n))
	}
	taken, err := qs.TakenRepositoryKeys(ctx, q.TakenRepositoryKeysParams{Project: project, Keys: candidates})
	if err != nil {
		return nil, err
	}
	for _, k := range candidates {
		if !slices.Contains(taken, k) {
			return &k, nil
		}
	}
	return nil, nil
}
