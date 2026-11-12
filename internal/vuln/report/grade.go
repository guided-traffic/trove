package report

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

// The grade of a version (R-VULN-038), as the owner's cogpit grades a container image: a letter from the open findings by
// severity and, for an image, the end of life of its OS; N/A while the version is not evaluated. The API answers it with the facts
// a screen shows beside it (R-VULN-039); the words of the grade, its text and its rule, are the clients' (Q-VULN-157).

// Grade is a version's grade: S, A, B, C, D, E or N/A.
type Grade string

// The grades, from not evaluated through the best to the worst.
const (
	GradeNA Grade = "N/A"
	GradeS  Grade = "S"
	GradeA  Grade = "A"
	GradeB  Grade = "B"
	GradeC  Grade = "C"
	GradeD  Grade = "D"
	GradeE  Grade = "E"
)

// The thresholds of the grades, cogpit's (Q-VULN-159).
const (
	// GradeAMaxLow is the most Low, None and Unknown findings of an A; more is B.
	GradeAMaxLow = 10
	// GradeDMaxCritical is the most Critical findings of a D; more is E.
	GradeDMaxCritical = 5
)

// position orders the grades for the rank: N/A lowest, then from the best to the worst; 0 for no grade.
func position(g Grade) int64 {
	switch g {
	case GradeNA:
		return 1
	case GradeS:
		return 2
	case GradeA:
		return 3
	case GradeB:
		return 4
	case GradeC:
		return 5
	case GradeD:
		return 6
	case GradeE:
		return 7
	}
	return 0
}

// rankStep separates the grades in the rank; a count beyond it is cut there.
const rankStep = 10_000_000

// lowOrUnknown is the number of findings the grades count together: Low, None and Unknown (Q-VULN-153).
func (c Counts) lowOrUnknown() int64 { return c.Low + c.None + c.Unknown }

// counted is the grade by the counts alone.
func (c Counts) counted() Grade {
	switch low := c.lowOrUnknown(); {
	case c.Critical > GradeDMaxCritical:
		return GradeE
	case c.Critical > 0:
		return GradeD
	case c.High > 0:
		return GradeC
	case c.Medium > 0, low > GradeAMaxLow:
		return GradeB
	case low > 0:
		return GradeA
	}
	return GradeS
}

// GradeOf is the grade of a version with these open findings, whose image's OS is (eosl) or is not at its end of life, and which
// is or is not evaluated (R-VULN-001): N/A while it is not, C at best at the end of life.
func GradeOf(c Counts, eosl, evaluated bool) Grade {
	if !evaluated {
		return GradeNA
	}
	g := c.counted()
	if eosl && position(g) < position(GradeC) {
		return GradeC
	}
	return g
}

// basis is the count that decides the grade: the Critical of D and E, the High of C (nothing when the end of life made it C), the
// Medium of B or else its Low, None and Unknown, those of A; nothing for S and N/A.
func basis(g Grade, c Counts, eosl bool) int64 {
	switch g {
	case GradeE, GradeD:
		return c.Critical
	case GradeC:
		if eosl && c.counted() != GradeC {
			return 0
		}
		return c.High
	case GradeB:
		if c.Medium > 0 {
			return c.Medium
		}
		return c.lowOrUnknown()
	case GradeA:
		return c.lowOrUnknown()
	}
	return 0
}

// Rank is a grade's order, worse higher (R-VULN-038): the grade, N/A lowest, then the count that decides it, so that "D · 4
// Critical" ranks above "D · 2 Critical", which ranks above "C · 120 High". An unknown grade ranks 0.
func Rank(g Grade, c Counts, eosl bool) int64 {
	pos := position(g)
	if pos == 0 {
		return 0
	}
	return pos*rankStep + min(basis(g, c, eosl), rankStep-1)
}

// VersionGrade is a version's grade in one repository, with what the screens show beside it (R-VULN-039).
type VersionGrade struct {
	Grade Grade
	// Open are the open findings of the version in the repository; zero for N/A.
	Open Counts
	// EOSL says that the OS of the image is at its end of life; OS names it ("alpine 3.24.1"), empty for every other format.
	EOSL bool
	OS   string
	// CheckedAt is, for an image, the oldest scan of its manifests, for every other format when the advisory data the evaluation
	// used was synchronised; nil for N/A.
	CheckedAt  *time.Time
	Evaluation vuln.Evaluation
	// Repository is the qualified name of the repository.
	Repository string
}

// Rank is the grade's order (Rank).
func (g VersionGrade) Rank() int64 { return Rank(g.Grade, g.Open, g.EOSL) }

// worst returns the worse of two grades by their rank; the first on a tie, so the repositories' order decides it.
func worst(a, b VersionGrade) VersionGrade {
	if b.Rank() > a.Rank() {
		return b
	}
	return a
}

// gradeIn is the grade of a version in a repository from its evaluation, its open findings, the manifests of an image (nil for a
// package) and the time its advisory data was synchronised.
func gradeIn(repository string, evaluation vuln.Evaluation, open Counts, images []ImageState, syncedAt *time.Time) VersionGrade {
	g := VersionGrade{Evaluation: evaluation, Repository: repository}
	if images != nil {
		g.OS = osOf(images)
		for _, m := range images {
			g.EOSL = g.EOSL || m.EOSL
		}
	}
	evaluated := evaluation == vuln.EvaluationEvaluated
	g.Grade = GradeOf(open, g.EOSL, evaluated)
	if !evaluated {
		return g
	}
	g.Open = open
	if images == nil {
		g.CheckedAt = syncedAt
		return g
	}
	for _, m := range images {
		if m.ScannedAt != nil && (g.CheckedAt == nil || m.ScannedAt.Before(*g.CheckedAt)) {
			at := *m.ScannedAt
			g.CheckedAt = &at
		}
	}
	return g
}

// osOf names the OS of an image's manifests: "alpine 3.24.1", the different ones of several platforms joined with ", ".
func osOf(images []ImageState) string {
	var names []string
	for _, m := range images {
		if n := strings.TrimSpace(m.OSFamily + " " + m.OSName); n != "" && !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	return strings.Join(names, ", ")
}

// gradeKey names a version of a package.
type gradeKey = store.NameVersion

// holderKey names a version in a repository.
type holderKey struct {
	repo int64
	gradeKey
}

// grades grades versions of one format over the repositories that hold them (holders), the worst per version (Q-VULN-155), from
// the open findings in scope and, for an image, the scans of its manifests: the core of the package report and of the list's grades.
func (s *Service) grades(ctx context.Context, sc Scope, format string, holders []store.Candidate, f store.Filter) (map[gradeKey]VersionGrade, error) {
	out := map[gradeKey]VersionGrade{}
	if len(holders) == 0 {
		return out, nil
	}
	var pairs []store.NameVersion
	var repos []int64
	seen := map[gradeKey]bool{}
	for _, h := range holders {
		k := gradeKey{Name: h.Package, Version: h.Version}
		if !seen[k] {
			seen[k] = true
			pairs = append(pairs, k)
		}
		if !slices.Contains(repos, h.RepoID) {
			repos = append(repos, h.RepoID)
		}
	}
	data, _, err := s.dataStates(ctx)
	if err != nil {
		return nil, err
	}
	image := format == imageFormat
	var counts []store.GradeCount
	if image {
		counts, err = s.st.ImageGradeCounts(ctx, sc.Scope, pairs, f)
	} else {
		counts, err = s.st.GradeCounts(ctx, sc.Scope, format, pairs, f)
	}
	if err != nil {
		return nil, err
	}
	open := map[holderKey]Counts{}
	for _, c := range counts {
		k := holderKey{c.RepoID, gradeKey{Name: c.Name, Version: c.Version}}
		o := open[k]
		o.Add(c.Severity, c.N)
		open[k] = o
	}
	var scans map[holderKey][]ImageState
	if image {
		if scans, err = s.tagScans(ctx, repos, pairs); err != nil {
			return nil, err
		}
	}
	// The evaluation of a repository depends on the name, not the version (R-VULN-010); an image's is refined by its scans.
	evals := map[holderKey]vuln.Evaluation{}
	for _, h := range holders {
		r, ok := sc.snap.Repository(h.RepoID)
		if !ok {
			continue
		}
		ek := holderKey{h.RepoID, gradeKey{Name: h.Package}}
		base, done := evals[ek]
		if !done {
			base = s.evaluation(ctx, r, h.Package, data)
			evals[ek] = base
		}
		k := holderKey{h.RepoID, gradeKey{Name: h.Package, Version: h.Version}}
		var images []ImageState
		eval := base
		if image {
			images = scans[k]
			if images == nil {
				images = []ImageState{}
			}
			eval = imageEvaluation(base, images)
		}
		p := s.policy(sc.snap, h.RepoID)
		g := gradeIn(r.Name.String(), eval, open[k], images, data[p.Ecosystem].SyncedAt)
		if prev, ok := out[k.gradeKey]; ok {
			g = worst(prev, g)
		}
		out[k.gradeKey] = g
	}
	return out, nil
}

// tagScans reads the manifests of image tags in the repositories, by repository and tag; nil while the image scanning is not wired.
func (s *Service) tagScans(ctx context.Context, repos []int64, pairs []store.NameVersion) (map[holderKey][]ImageState, error) {
	out := map[holderKey][]ImageState{}
	if s.images == nil {
		return out, nil
	}
	rows, err := s.images.GradeScans(ctx, repos, pairs)
	if err != nil {
		return nil, err
	}
	for _, m := range rows {
		k := holderKey{m.RepoID, gradeKey{Name: m.Name, Version: m.Tag}}
		state := m.State
		if state == "" {
			state = store.ImageScanPending
		}
		out[k] = append(out[k], ImageState{Digest: m.Digest, Platform: m.Platform, State: state, ScannedAt: m.ScannedAt, OSFamily: m.OSFamily, OSName: m.OSName, EOSL: m.EOSL})
	}
	return out, nil
}

// LatestEntry is an entry of a page of the packages list: its format, its stored name and its latest version.
type LatestEntry struct {
	Format, Name, Version string
}

// LatestGrades grades the latest version of each entry of a page of the packages list over the repositories of f that hold it
// (R-VULN-039, Q-VULN-154), by PackageListKey; a fixed number of statements per format of the page. An entry whose version no
// readable repository holds has no grade.
func (s *Service) LatestGrades(ctx context.Context, sc Scope, entries []LatestEntry, f store.Filter) (map[string]VersionGrade, error) {
	byFormat := map[string][]store.NameVersion{}
	var formats []string
	for _, e := range entries {
		if _, ok := byFormat[e.Format]; !ok {
			formats = append(formats, e.Format)
		}
		byFormat[e.Format] = append(byFormat[e.Format], store.NameVersion{Name: e.Name, Version: e.Version})
	}
	out := make(map[string]VersionGrade, len(entries))
	for _, format := range formats {
		holders, err := s.st.GradeHolders(ctx, sc.Scope, format, byFormat[format], f)
		if err != nil {
			return nil, err
		}
		gs, err := s.grades(ctx, sc, format, holders, f)
		if err != nil {
			return nil, err
		}
		for k, g := range gs {
			out[PackageListKey(format, k.Name)] = g
		}
	}
	return out, nil
}
