package blob_test

import (
	"io"
	"testing"

	"github.com/guided-traffic/trove/internal/observability"
)

func readAll(t *testing.T, r io.ReadCloser) []byte {
	t.Helper()
	defer r.Close()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func gauge(t *testing.T, m *observability.Metrics, name string) float64 {
	t.Helper()
	return counter(t, m, name, nil)
}

// counter returns the value of the series of a family whose labels include want.
func counter(t *testing.T, m *observability.Metrics, name string, want map[string]string) float64 {
	t.Helper()
	fams, err := m.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fams {
		if f.GetName() != name {
			continue
		}
		for _, mm := range f.GetMetric() {
			ok := true
			for k, v := range want {
				found := false
				for _, l := range mm.GetLabel() {
					found = found || (l.GetName() == k && l.GetValue() == v)
				}
				ok = ok && found
			}
			if !ok {
				continue
			}
			switch {
			case mm.Counter != nil:
				return mm.Counter.GetValue()
			case mm.Gauge != nil:
				return mm.Gauge.GetValue()
			}
		}
	}
	return 0
}
