package repository

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/guided-traffic/trove/internal/qname"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/vuln"
)

var errForeignTx = errors.New("repository: the transaction is not one of internal/meta/postgres")

// queries returns the sqlc queries on the transaction behind tx. The package
// writes no SQL: its statements live in
// internal/meta/postgres/queries/repository.sql.
func queries(tx meta.Tx) (*q.Queries, error) {
	ptx, ok := postgres.Unwrap(tx)
	if !ok {
		return nil, errForeignTx
	}
	return q.New(ptx), nil
}

// readTx runs fn in a read-only use of a transaction of the runner.
func readRows(ctx context.Context, run meta.Runner, fn func(*q.Queries) ([]q.Repository, error)) ([]q.Repository, error) {
	var rows []q.Repository
	err := run.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		rows, err = fn(qs)
		return err
	})
	return rows, err
}

// toRepository converts a row to the read model and compiles the repository's
// patterns. project is the key of the project the row belongs to. A row whose stored document does not decode, or whose patterns do not
// compile, cannot have been written by this package; it is loaded disabled, so
// that it takes no part in resolution, download or deploy (fail closed), and the
// returned error says why.
func toRepository(r q.Repository, project string, cat *catalog.Registry) (Repository, error) {
	out := Repository{
		ID: strconv.FormatInt(r.ID, 10), Name: qname.Repo{Project: project, Key: r.Key}, Class: Class(r.Class), Format: r.Format,
		HideExistence: HideInherit, Lifecycle: Lifecycle(r.Lifecycle), Version: r.ConfigVersion,
		Visibility: Visibility{Artifacts: r.VisibilityArtifacts, Wiki: r.VisibilityWiki},
		Config:     r.Config, CreatedAt: r.CreatedAt.Time, UpdatedAt: r.UpdatedAt.Time,
	}
	if r.LastActivityAt.Valid {
		out.LastActivityAt = r.LastActivityAt.Time
	}
	if r.ProjectID.Valid {
		out.ProjectID = r.ProjectID.String()
	}
	var s Settings
	if err := json.Unmarshal(r.Config, &s); err != nil {
		out.Disabled = true
		return out, err
	}
	s.normalize()
	out.Disabled, out.HideExistence = s.Disabled, s.HideExistence
	subject := SubjectPath
	info, known := cat.Lookup(r.Format)
	if known {
		subject = info.PatternSubject
	}
	// >>> M2 virtual
	// A name pattern is folded with the format's name rule, like the name it is
	// matched against (R-REPO-005, R-AUTHZ-005).
	include, exclude := s.IncludePatterns, s.ExcludePatterns
	if subject == SubjectName {
		include, exclude = foldAll(cat, r.Format, include), foldAll(cat, r.Format, exclude)
	}
	out.rid, out.PriorityMember = r.ID, s.PriorityMember
	out.autoClaim = effectiveAutoClaim(info, known, &s)
	if eco := cmp.Or(info.Ecosystem, info.ImageEcosystem); out.Class != Virtual && known && eco != "" {
		out.vulnEcosystem, out.vulnerabilities = eco, vuln.DefaultSettings()
		if s.Vulnerabilities != nil {
			out.vulnerabilities = *s.Vulnerabilities
		}
	}
	if out.Class == Virtual {
		out.virt = &virtualState{settings: VirtualSettings{Members: []string{}, MergedMetadataTTLSeconds: ptr(mergedTTLOf(s)), Pinning: Pinning{Rules: []PinRule{}}}}
	}
	// <<< M2 virtual
	adm, err := NewAdmission(subject, include, exclude)
	if err != nil {
		out.Disabled = true
		return out, err
	}
	out.adm = adm
	if Class(r.Class) == Remote && len(s.Remote) > 0 {
		rs, err := parseStoredRemote(s.Remote)
		if err != nil {
			out.Disabled = true
			return out, err
		}
		out.remote = rs
	}
	return out, nil
}

// >>> M2 virtual

// effectiveAutoClaim is the value of local.autoClaim a repository acts on: the format's default (on for a format with package
// names that supports claims, off otherwise) unless the document sets it, and never on where the format cannot claim.
func effectiveAutoClaim(info catalog.Entry, known bool, s *Settings) bool {
	on := known && info.PinsAndClaims && info.PackageNames
	if s.Local != nil && s.Local.AutoClaim != nil {
		on = on && *s.Local.AutoClaim
	}
	return on
}

// foldAll folds name patterns with the name rule of the format; a pattern the
// rule does not take stays as written and fails to compile or never matches.
func foldAll(cat *catalog.Registry, format string, list []string) []string {
	out := make([]string, len(list))
	for i, p := range list {
		if n, ok := cat.NormalizeName(format, p); ok {
			p = n
		}
		out[i] = p
	}
	return out
}

func ptr[T any](v T) *T { return &v }

// <<< M2 virtual

// toDocument renders the stored row as the configuration document.
func toDocument(r q.Repository, cat *catalog.Registry) (Document, error) {
	d := Document{Key: r.Key, Class: Class(r.Class), Format: r.Format, Visibility: Visibility{Artifacts: r.VisibilityArtifacts, Wiki: r.VisibilityWiki}}
	if err := json.Unmarshal(r.Config, &d.Settings); err != nil {
		return Document{}, err
	}
	d.normalize()
	if Class(r.Class) == Remote && len(d.Remote) > 0 {
		// PostgreSQL keeps the section as jsonb, which has its own member order and
		// spacing: render it from the settings, as the validation does, so that a
		// document that is read and sent back is byte-equal to what is stored.
		if rs, err := parseStoredRemote(d.Remote); err == nil {
			if b, err := json.Marshal(rs); err == nil {
				d.Remote = b
			}
		}
	}
	if info, ok := cat.Lookup(r.Format); ok {
		d.Layout = info.Layout
	}
	return d, nil
}

func notFound(name qname.Repo) *errs.Error {
	return errs.E(errs.NotFound, errs.CodeRepositoryNotFound, "no repository is named "+strconv.Quote(name.String()))
}
