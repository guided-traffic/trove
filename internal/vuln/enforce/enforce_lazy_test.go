package enforce

import (
	"context"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
)

// countingStates counts the reads of the data state.
type countingStates struct {
	reads int
	st    advisory.DataState
}

func (c *countingStates) State(string) advisory.DataState { c.reads++; return c.st }

// R-VULN-015, R-ARCH-032: the data state is read only when it decides: never for a repository that allows missing data, never
// when a finding blocks already, and for onMissingData block only when the lookup found nothing.
func TestR_VULN_015_TheDataStateIsReadOnlyWhenItDecides(t *testing.T) {
	cs := &countingStates{st: advisory.DataState{Complete: true}}
	e := New(Options{Enabled: true, Enforcement: vuln.EnforceBlocks, States: cs})
	if b := e.Decide(scanned, vuln.BlockRow{}, 7); b.Active() || cs.reads != 0 {
		t.Errorf("onMissingData allow: block %+v, %d state reads", b, cs.reads)
	}
	if b := e.Decide(scanned, malRow(), 7); !b.Refused() || cs.reads != 0 {
		t.Errorf("a blocking finding: block %+v, %d state reads", b, cs.reads)
	}
	bp := scanned
	bp.OnMissingData = vuln.MissingBlock
	if b := e.Decide(bp, malRow(), 7); !b.Refused() || b.Reason != vuln.BlockMalicious || cs.reads != 0 {
		t.Errorf("onMissingData block with a blocking finding: %+v, %d state reads", b, cs.reads)
	}
	if b := e.Decide(bp, vuln.BlockRow{}, 7); b.Active() || cs.reads != 1 {
		t.Errorf("onMissingData block, complete data: %+v, %d state reads", b, cs.reads)
	}
	cs.st = advisory.DataState{}
	if b := e.Decide(bp, vuln.BlockRow{}, 7); b.Reason != vuln.BlockMissingData || cs.reads != 2 {
		t.Errorf("onMissingData block, data missing: %+v, %d state reads", b, cs.reads)
	}
}

// R-VULN-016: the refusal of a remote miss names the repository the client addressed, never the member that would have
// fetched it.
func TestR_VULN_016_TheMissRefusalNamesTheAddressedRepository(t *testing.T) {
	f := newFixture(t, states{"npm": {Complete: true}}, vuln.EnforceBlocks)
	f.ev.block = vuln.Block{Reason: vuln.BlockMalicious, AdvisoryID: "MAL-2026-1234"}
	m := Miss{Addressed: "npm-all", Package: "trove-test-mal", Version: "1.0.0", Path: "p"}
	m.Repo.Name = qr("npm-member-remote")
	err := f.e.gate(context.Background(), scanned, m)
	e, ok := errs.As(err)
	if !ok || !strings.Contains(e.Detail, " in npm-all: ") || strings.Contains(e.Detail, "npm-member-remote") {
		t.Fatalf("text = %v", err)
	}
}
