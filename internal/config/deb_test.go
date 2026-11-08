package config

import (
	"testing"
	"time"
)

func TestDebDefaults(t *testing.T) {
	c := mustLoad(t, Options{Environ: servableEnv(), Command: CommandServe}).Config
	d := c.Deb
	if d.IndexDelay != 2*time.Second || d.VirtualIndexInterval != 60*time.Second || d.ByHashKeep != time.Hour || d.IndexWaitMax != 30*time.Second {
		t.Errorf("deb = %+v", d)
	}
}

func TestDebRanges(t *testing.T) {
	cases := []struct {
		name, yaml, key string
		bad             bool
	}{
		{"an index delay below 1s", "deb:\n  index_delay: 999ms\n", "deb.index_delay", true},
		{"an index delay of 1s", "deb:\n  index_delay: 1s\n", "deb.index_delay", false},
		{"an index delay of 60s", "deb:\n  index_delay: 60s\n", "deb.index_delay", false},
		{"an index delay above 60s", "deb:\n  index_delay: 61s\n", "deb.index_delay", true},
		{"a virtual interval below 10s", "deb:\n  virtual_index_interval: 9s\n", "deb.virtual_index_interval", true},
		{"a virtual interval of one hour", "deb:\n  virtual_index_interval: 1h\n", "deb.virtual_index_interval", false},
		{"a virtual interval above one hour", "deb:\n  virtual_index_interval: 61m\n", "deb.virtual_index_interval", true},
		{"a by-hash keep below 10m", "deb:\n  by_hash_keep: 9m\n", "deb.by_hash_keep", true},
		{"a by-hash keep of 7 days", "deb:\n  by_hash_keep: 168h\n", "deb.by_hash_keep", false},
		{"a by-hash keep above 7 days", "deb:\n  by_hash_keep: 169h\n", "deb.by_hash_keep", true},
		{"an index wait below 1s", "deb:\n  index_wait_max: 500ms\n", "deb.index_wait_max", true},
		{"an index wait of 120s", "deb:\n  index_wait_max: 120s\n", "deb.index_wait_max", false},
		{"an index wait above 120s", "deb:\n  index_wait_max: 121s\n", "deb.index_wait_max", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := loadM2(t, c.yaml)
			if got := hasIssue(err, c.key); got != c.bad {
				t.Fatalf("issue on %s = %v, want %v (%v)", c.key, got, c.bad, err)
			}
		})
	}
}

func TestDebEnvironment(t *testing.T) {
	l := mustLoad(t, Options{Environ: servableEnv("TROVE_DEB_INDEX_DELAY=5s"), Command: CommandServe})
	if l.Config.Deb.IndexDelay != 5*time.Second || l.Sources["deb.index_delay"].Kind != "env" {
		t.Errorf("index_delay = %s, source = %+v", l.Config.Deb.IndexDelay, l.Sources["deb.index_delay"])
	}
}
