//go:build integration

package vulnblock

import (
	"context"
	"sync"

	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/vuln"
)

// evaluatorPort is the fake of the vuln.Evaluator port: the verdict of a remote miss is named by the test, and every call is
// kept. The commit hook of the evaluation is another package's; here it does nothing.
type evaluatorPort struct {
	mu     sync.Mutex
	blocks map[string]vuln.Block
	calls  []vuln.Miss
}

var _ vuln.Evaluator = (*evaluatorPort)(nil)

func newEvaluatorPort() *evaluatorPort { return &evaluatorPort{blocks: map[string]vuln.Block{}} }

func key(repo, name, version string) string { return repo + "\x00" + name + "\x00" + version }

// block names the verdict of the miss of name@version in a repository.
func (e *evaluatorPort) block(repo, name, version string, b vuln.Block) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.blocks[key(repo, name, version)] = b
}

// asked returns the number of EvaluateMiss calls.
func (e *evaluatorPort) asked() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.calls)
}

func (e *evaluatorPort) OnVersionCommitted(context.Context, meta.Tx, vuln.CommittedVersion) error {
	return nil
}

func (e *evaluatorPort) OnVersionsRemoved(context.Context, meta.Tx, []int64) error { return nil }

func (e *evaluatorPort) EvaluateMiss(_ context.Context, m vuln.Miss) (vuln.Block, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, m)
	return e.blocks[key(m.RepoKey, m.Name, m.Version)], nil
}
