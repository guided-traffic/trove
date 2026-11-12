package report

import (
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

// The grade of a version at every boundary of R-VULN-038: 0, 10 and 11 Low, None counted with Low, 1, 5 and 6 Critical, the end
// of life and N/A, with the count that decides each.
func TestR_VULN_038_GradeAtEveryBoundary(t *testing.T) {
	for _, tc := range []struct {
		name            string
		c               Counts
		eosl, evaluated bool
		grade           Grade
		basis           int64
	}{
		{"evaluated, nothing found", Counts{}, false, true, GradeS, 0},
		{"not evaluated", Counts{}, false, false, GradeNA, 0},
		{"not evaluated keeps N/A whatever it counts", Counts{Critical: 9}, true, false, GradeNA, 0},
		{"1 Low", Counts{Low: 1}, false, true, GradeA, 1},
		{"10 Low", Counts{Low: 10}, false, true, GradeA, 10},
		{"11 Low", Counts{Low: 11}, false, true, GradeB, 11},
		{"6 Low and 5 Unknown", Counts{Low: 6, Unknown: 5}, false, true, GradeB, 11},
		{"10 None", Counts{None: 10}, false, true, GradeA, 10},
		{"10 Unknown", Counts{Unknown: 10}, false, true, GradeA, 10},
		{"1 Medium", Counts{Medium: 1, Low: 3}, false, true, GradeB, 1},
		{"1 High", Counts{High: 1, Medium: 40}, false, true, GradeC, 1},
		{"1 Critical", Counts{Critical: 1, High: 2}, false, true, GradeD, 1},
		{"5 Critical", Counts{Critical: 5}, false, true, GradeD, 5},
		{"6 Critical", Counts{Critical: 6}, false, true, GradeE, 6},
		{"13 Critical, the owner's example", Counts{Critical: 13, High: 160, Medium: 113, Low: 16, Unknown: 843}, false, true, GradeE, 13},
		{"end of life without a finding", Counts{}, true, true, GradeC, 0},
		{"end of life with 1 Low", Counts{Low: 1}, true, true, GradeC, 0},
		{"end of life with 1 High counts the High", Counts{High: 1}, true, true, GradeC, 1},
		{"end of life with 6 Critical stays E", Counts{Critical: 6}, true, true, GradeE, 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := GradeOf(tc.c, tc.eosl, tc.evaluated)
			if g != tc.grade {
				t.Fatalf("GradeOf(%+v, eosl %v, evaluated %v) = %s, want %s", tc.c, tc.eosl, tc.evaluated, g, tc.grade)
			}
			if b := basis(g, tc.c, tc.eosl); b != tc.basis {
				t.Errorf("the count that decides %s is %d, want %d", g, b, tc.basis)
			}
		})
	}
}

// The order of R-VULN-038: worse is higher, the grade first and the count that decides it within the grade, N/A below S.
func TestR_VULN_038_GradesOrderByGradeThenTheirCount(t *testing.T) {
	in := []struct {
		name string
		c    Counts
		eosl bool
		eval bool
	}{
		{"E · 13 Critical", Counts{Critical: 13}, false, true},
		{"E · 6 Critical", Counts{Critical: 6}, false, true},
		{"D · 4 Critical", Counts{Critical: 4}, false, true},
		{"D · 2 Critical", Counts{Critical: 2}, false, true},
		{"C · 120 High", Counts{High: 120}, false, true},
		{"C · OS end of life", Counts{}, true, true},
		{"B · 45 Medium", Counts{Medium: 45}, false, true},
		{"A · 3 Low", Counts{Low: 3}, false, true},
		{"S", Counts{}, false, true},
		{"N/A", Counts{}, false, false},
	}
	for i := 1; i < len(in); i++ {
		a, b := in[i-1], in[i]
		ra := Rank(GradeOf(a.c, a.eosl, a.eval), a.c, a.eosl)
		rb := Rank(GradeOf(b.c, b.eosl, b.eval), b.c, b.eosl)
		if ra <= rb {
			t.Errorf("%s (%d) does not rank above %s (%d)", a.name, ra, b.name, rb)
		}
	}
	if r := Rank("X", Counts{}, false); r != 0 {
		t.Errorf("an unknown grade ranks %d, want 0", r)
	}
	if r := Rank(GradeB, Counts{Low: 20_000_000}, false); r != 4*rankStep+rankStep-1 {
		t.Errorf("a count beyond the step is cut there: %d", r)
	}
}

// A version's grade in one repository (R-VULN-039): an image takes the end of life of any of its manifests, its OS and the oldest
// scan; a package the time of its advisory data; N/A has no counts and no time; several repositories give the worst (Q-VULN-155).
func TestR_VULN_039_GradeOfAVersionInARepository(t *testing.T) {
	old, recent := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC), time.Date(2026, 11, 12, 11, 0, 0, 0, time.UTC)
	images := []ImageState{
		{Digest: "sha256:a", Platform: "linux/amd64", State: store.ImageScanScanned, ScannedAt: &recent, OSFamily: "alpine", OSName: "3.24.1"},
		{Digest: "sha256:b", Platform: "linux/arm64", State: store.ImageScanScanned, ScannedAt: &old, OSFamily: "alpine", OSName: "3.24.1", EOSL: true},
	}
	g := gradeIn("web/images", vuln.EvaluationEvaluated, Counts{}, images, nil)
	if g.Grade != GradeC || !g.EOSL || g.OS != "alpine 3.24.1" || g.CheckedAt == nil || !g.CheckedAt.Equal(old) || g.Repository != "web/images" {
		t.Errorf("an image at its end of life = %+v", g)
	}
	two := append([]ImageState{}, images[0], ImageState{Digest: "sha256:c", State: store.ImageScanScanned, ScannedAt: &recent, OSFamily: "debian", OSName: "12.8"})
	if g := gradeIn("web/images", vuln.EvaluationEvaluated, Counts{Low: 2}, two, nil); g.Grade != GradeA || g.EOSL || g.OS != "alpine 3.24.1, debian 12.8" {
		t.Errorf("two platforms of other OSes = %+v", g)
	}
	synced := recent
	p := gradeIn("web/npm", vuln.EvaluationEvaluated, Counts{Critical: 6, High: 1}, nil, &synced)
	if p.Grade != GradeE || p.EOSL || p.OS != "" || p.CheckedAt != &synced || p.Open.Critical != 6 {
		t.Errorf("a package = %+v", p)
	}
	na := gradeIn("web/npm-off", vuln.EvaluationNotCovered, Counts{Low: 3}, nil, &synced)
	if na.Grade != GradeNA || na.Open != (Counts{}) || na.CheckedAt != nil || na.Evaluation != vuln.EvaluationNotCovered {
		t.Errorf("a version that is not covered = %+v", na)
	}
	pending := gradeIn("web/images", imageEvaluation(vuln.EvaluationEvaluated, []ImageState{{State: store.ImageScanPending}}), Counts{}, []ImageState{{State: store.ImageScanPending}}, nil)
	if pending.Grade != GradeNA || pending.Evaluation != vuln.EvaluationPendingScan {
		t.Errorf("a tag still pending = %+v", pending)
	}
	if w := worst(na, p); w.Repository != "web/npm" {
		t.Errorf("an evaluated repository wins over one that is not: %+v", w)
	}
	if w := worst(p, gradeIn("web/npm-2", vuln.EvaluationEvaluated, Counts{Critical: 7}, nil, &synced)); w.Repository != "web/npm-2" {
		t.Errorf("7 Critical is worse than 6: %+v", w)
	}
	if w := worst(p, p); w.Repository != "web/npm" {
		t.Errorf("a tie keeps the first: %+v", w)
	}
}
