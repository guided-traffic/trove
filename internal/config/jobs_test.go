package config

import (
	"strings"
	"testing"
	"time"
)

func TestJobsWorkersQueues(t *testing.T) {
	w := JobsWorkers{Default: 4, Maintenance: 1, Index: 2, Delivery: 8, Mirror: 2, Vuln: 1}
	t.Run("R-ARCH-016 every queue has its worker slots", func(t *testing.T) {
		want := map[string]int{"default": 4, "maintenance": 1, "index": 2, "delivery": 8, "mirror": 2, "vuln": 1}
		for _, q := range w.Queues() {
			n, ok := w.Count(q)
			if !ok || n != want[q] {
				t.Errorf("Count(%s) = %d, %v", q, n, ok)
			}
		}
		if len(w.Queues()) != len(want) {
			t.Errorf("Queues = %v", w.Queues())
		}
		if _, ok := w.Count("elsewhere"); ok {
			t.Error("an unknown queue was accepted")
		}
	})
}

func TestWatchedLoopIntervals(t *testing.T) {
	check := func(mut func(*Config)) []string {
		var c Config
		c.Health.WatchdogTimeout = 2 * time.Minute
		c.Jobs.PollInterval, c.Jobs.ScheduleTick = 2*time.Second, 10*time.Second
		c.Cluster.EpochPollInterval = time.Second
		c.Database.LeaseCheckInterval = 5 * time.Second
		c.System.HeartbeatInterval = 10 * time.Second
		mut(&c)
		var issues []Issue
		c.validateWatchedLoops(&Report{issues: &issues})
		out := make([]string, len(issues))
		for i, is := range issues {
			out[i] = is.Message
		}
		return out
	}
	t.Run("R-ARCH-016 the shipped cadences are accepted", func(t *testing.T) {
		if got := check(func(*Config) {}); len(got) != 0 {
			t.Fatalf("issues: %v", got)
		}
	})
	t.Run("R-ARCH-016 a cadence at or above health.watchdog_timeout is rejected naming both keys", func(t *testing.T) {
		for _, tc := range []struct {
			key string
			mut func(*Config)
		}{
			{"jobs.schedule_tick", func(c *Config) { c.Jobs.ScheduleTick = 2 * time.Minute }},
			{"jobs.poll_interval", func(c *Config) { c.Jobs.PollInterval = 3 * time.Minute }},
			{"cluster.epoch_poll_interval", func(c *Config) { c.Cluster.EpochPollInterval = 2 * time.Minute }},
			{"database.lease_check_interval", func(c *Config) { c.Database.LeaseCheckInterval = 5 * time.Minute }},
			{"system.heartbeat_interval", func(c *Config) { c.System.HeartbeatInterval = 2 * time.Minute }},
		} {
			got := check(tc.mut)
			if len(got) != 1 || !strings.Contains(got[0], tc.key) || !strings.Contains(got[0], "health.watchdog_timeout") {
				t.Errorf("%s: issues = %v", tc.key, got)
			}
		}
	})
}

func TestJobsQueueSettings(t *testing.T) {
	issues := func(j Jobs) []string {
		j.LeaseDuration, j.HeartbeatInterval = time.Minute, 15*time.Second
		var is []Issue
		j.Validate(&Report{issues: &is})
		out := make([]string, len(is))
		for i, x := range is {
			out[i] = x.Message
		}
		return out
	}
	t.Run("R-ARCH-016 the queue vuln exists with a slot of its own", func(t *testing.T) {
		if n, ok := (JobsWorkers{Vuln: 1}).Count("vuln"); !ok || n != 1 {
			t.Fatalf("Count(vuln) = %d, %v", n, ok)
		}
	})
	t.Run("R-ARCH-016 no setting claims every queue", func(t *testing.T) {
		var j Jobs
		if got := issues(j); len(got) != 0 {
			t.Fatalf("issues %v", got)
		}
		if got := j.ClaimedQueues(); len(got) != 6 {
			t.Fatalf("claimed %v", got)
		}
	})
	t.Run("R-ARCH-016 queues and exclude_queues each narrow the claim", func(t *testing.T) {
		if got := (&Jobs{Queues: []string{"index"}}).ClaimedQueues(); len(got) != 1 || got[0] != "index" {
			t.Fatalf("queues: %v", got)
		}
		j := &Jobs{ExcludeQueues: []string{"index", "mirror"}}
		if got := j.ClaimedQueues(); len(got) != 4 || j.Claims("index") || !j.Claims("vuln") {
			t.Fatalf("exclude: %v", got)
		}
	})
	t.Run("R-ARCH-016 a queue that does not exist is refused naming the key", func(t *testing.T) {
		for key, j := range map[string]Jobs{"queues": {Queues: []string{"indexing"}}, "exclude_queues": {ExcludeQueues: []string{"nope"}}} {
			got := issues(j)
			if len(got) != 1 || !strings.Contains(got[0], "does not exist") {
				t.Errorf("%s: %v", key, got)
			}
		}
	})
	t.Run("R-ARCH-016 both settings at once are refused", func(t *testing.T) {
		got := issues(Jobs{Queues: []string{"index"}, ExcludeQueues: []string{"mirror"}})
		if len(got) != 1 || !strings.Contains(got[0], "not both") {
			t.Fatalf("issues %v", got)
		}
	})
	t.Run("R-ARCH-016 a process left with no queue is refused", func(t *testing.T) {
		got := issues(Jobs{ExcludeQueues: JobsWorkers{}.Queues()})
		if len(got) != 1 || !strings.Contains(got[0], "claim nothing") {
			t.Fatalf("issues %v", got)
		}
	})
}

func TestJobsQueuesFromTheEnvironment(t *testing.T) {
	load := func(env ...string) (*Loaded, error) {
		return Load(Options{Environ: servableEnv(env...), Command: CommandServe, ReadFile: fakeFS(nil)})
	}
	t.Run("R-HA-062 a list-valued key is read from its environment variable as a JSON or YAML flow list", func(t *testing.T) {
		for _, v := range []string{`["index","vuln"]`, `[index, vuln]`} {
			l, err := load("TROVE_JOBS_QUEUES=" + v)
			if err != nil {
				t.Fatalf("%s: %v", v, err)
			}
			if got := l.Config.Jobs.Queues; len(got) != 2 || got[0] != "index" || got[1] != "vuln" {
				t.Fatalf("%s: %v", v, got)
			}
		}
		l, err := load(`TROVE_JOBS_EXCLUDE_QUEUES=["index"]`)
		if err != nil || len(l.Config.Jobs.ExcludeQueues) != 1 || l.Config.Jobs.Claims("index") {
			t.Fatalf("exclude: %v %+v", err, l)
		}
	})
	t.Run("R-HA-062 a bare name or a comma-separated list is refused", func(t *testing.T) {
		for _, v := range []string{"index", "index,vuln"} {
			if _, err := load("TROVE_JOBS_QUEUES=" + v); err == nil {
				t.Fatalf("%q was accepted", v)
			} else {
				t.Logf("%q: %v", v, err)
			}
		}
	})
	t.Run("R-ARCH-016 both settings from the environment stop start-up and name the key", func(t *testing.T) {
		_, err := load(`TROVE_JOBS_QUEUES=["index"]`, `TROVE_JOBS_EXCLUDE_QUEUES=["mirror"]`)
		if err == nil || !strings.Contains(err.Error(), "exclude_queues") {
			t.Fatalf("%v", err)
		}
	})
}
