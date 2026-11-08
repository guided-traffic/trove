package sync

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/vuln/match"
	vmetrics "github.com/guided-traffic/trove/internal/vuln/metrics"
	"github.com/guided-traffic/trove/internal/vuln/store"

	"github.com/guided-traffic/trove/internal/blob"
)

type stubDB struct{}

func (stubDB) InTx(context.Context, func(meta.Tx) error) error { return nil }
func (stubDB) Read(context.Context, func(dbtx) error) error    { return nil }

type stubChanges struct{}

func (stubChanges) Notify(meta.Tx, string, int, any) error { return nil }

type stubSpool struct{}

func (stubSpool) Ingest(context.Context, io.Reader, blob.IngestOptions) (*blob.Spooled, error) {
	return nil, nil
}

func counter(m *vmetrics.Metrics, eco, result string) float64 {
	var out dto.Metric
	if err := m.SyncRecords.WithLabelValues(store.SourceOSV, eco, result).Write(&out); err != nil {
		panic(err)
	}
	return out.GetCounter().GetValue()
}

// seriesOf counts the series of the record counter that a registry gathers.
func seriesOf(t *testing.T, reg *prometheus.Registry) int {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, f := range families {
		if f.GetName() == vmetrics.SyncRecords {
			n += len(f.GetMetric())
		}
	}
	return n
}

// R-VULN-024, Q-VULN-122: the series of every result the alerts read exist at 0 for every configured ecosystem as soon as the
// service is built, so that the first rejection of a process is an increase and not the series' first sample.
func TestR_VULN_024_TheRejectionSeriesExistAtZeroWhenTheServiceIsBuilt(t *testing.T) {
	promReg := prometheus.NewRegistry()
	m, err := vmetrics.New(promReg)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := cache.NewRegistry(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var cfg config.Vulnerabilities
	if _, err := New(Options{
		DB: stubDB{}, Changes: stubChanges{}, Caches: reg, HTTP: http.DefaultClient, Spool: stubSpool{}, Metrics: m, Config: cfg,
		Adapters: func() map[string]match.Adapter {
			return map[string]match.Adapter{"npm": testAdapter{}, "NuGet": testAdapter{}}
		},
		Log: slog.New(slog.DiscardHandler),
	}); err != nil {
		t.Fatal(err)
	}
	if got, want := seriesOf(t, promReg), 2*3; got != want {
		t.Fatalf("%d series, want %d (two ecosystems, three results)", got, want)
	}
	for _, eco := range []string{"npm", "NuGet"} {
		for _, result := range []string{vmetrics.RecordRejected, vmetrics.RecordInvalid, vmetrics.RecordRejectedMalicious} {
			if got := counter(m, eco, result); got != 0 {
				t.Errorf("%s %s = %v", eco, result, got)
			}
		}
	}
}

// R-VULN-024, Q-VULN-122: a rejection by a bound of one entry counts as rejected, a rejection by validation as invalid, and a
// MAL- record of either cause also as rejected_malicious; the counts are written when the log is finished, so a run that ends on an error still
// counts what it rejected.
func TestR_VULN_024_RejectionsAreCountedByCauseWhateverEndedTheRun(t *testing.T) {
	m, err := vmetrics.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{o: Options{Metrics: m}, log: slog.New(slog.DiscardHandler)}
	rl := s.newRejectLog("npm")
	rl.add("GHSA-a.json", "GHSA-a", sizeRejection(10, 5))
	rl.add("MAL-2026-1.json", "MAL-2026-1", sizeRejection(10, 5))
	rl.add("MAL-2026-2.json", "MAL-2026-2", &rejection{reason: "bad", validation: true})
	rl.add("a row", "", &rejection{reason: "bad row", validation: true})
	rl.counted(3)
	rl.finish() // as the deferred call of a run that failed after these rejections
	for result, want := range map[string]float64{
		vmetrics.RecordRejected: 2, vmetrics.RecordInvalid: 5, vmetrics.RecordRejectedMalicious: 2, // a MAL- record of either cause
	} {
		if got := counter(m, "npm", result); got != want {
			t.Errorf("%s = %v, want %v", result, got, want)
		}
	}
}
