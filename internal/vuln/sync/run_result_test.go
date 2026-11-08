package sync

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/vuln/match"
)

// resultRun records what a run sets as its result.
type resultRun struct{ result any }

func (*resultRun) LoadCheckpoint(any) (bool, error)                         { return false, nil }
func (*resultRun) SaveCheckpoint(context.Context, any) error                { return nil }
func (*resultRun) SaveProgress(context.Context, int64, int64, string) error { return nil }
func (r *resultRun) SetResult(v any) error                                  { r.result = v; return nil }

// R-OPS-019, Q-OPS-187: the result of vuln-sync-osv is a JSON object, as that of every job kind, so that the jobs API answers it.
func TestR_OPS_019_TheResultOfTheOSVSyncIsAnObject(t *testing.T) {
	cfg := config.Vulnerabilities{Enabled: true}
	s := &Service{o: Options{Config: cfg, Adapters: func() map[string]match.Adapter { return nil }}}
	run := &resultRun{}
	if err := s.RunOSV(context.Background(), run, Params{}, false); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(run.result)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || string(obj["ecosystems"]) != "[]" {
		t.Fatalf("%s: %v", raw, err)
	}
}
