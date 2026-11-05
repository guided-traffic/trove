package v1

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/api/v1/apitest"
	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/mirror"
	"github.com/guided-traffic/trove/internal/repository"
)

// Tests of the mirror's status and run operations (R-REPO-056, R-REPO-057, R-REPO-050) against doubles of the database and
// the job queue: the callers are narrowed to one right each, so that every row of the permission table is a case. The
// SQL and the transaction are the integration package test/integration/mirrorapi's.

const callerUUID = "0192f0c4-7a7e-7b51-9b1e-2f0d6c9e1a10" // the principal of authed

type mirrorTx struct{ meta.Tx }

// mirrorStore runs a transaction on no database: the doubles below take the place of every statement.
type mirrorStore struct{ commits int }

func (s *mirrorStore) InTx(_ context.Context, fn func(meta.Tx) error) error {
	if err := fn(mirrorTx{}); err != nil {
		return err
	}
	s.commits++
	return nil
}

func (*mirrorStore) Read(context.Context, func(q.DBTX) error) error { return nil }

type mirrorRequest struct {
	repoID int64
	by     string
	full   bool
}

// fakeMirror is the status row, the repositories and the request log.
type fakeMirror struct {
	targets  map[string]MirrorTarget
	row      MirrorRow
	requests []mirrorRequest
}

func (f *fakeMirror) Target(_ context.Context, key string) (MirrorTarget, error) {
	t, ok := f.targets[key]
	if !ok {
		return MirrorTarget{}, errs.E(errs.NotFound, errs.CodeRepositoryNotFound, "no repository has the key "+key)
	}
	return t, nil
}

func (f *fakeMirror) Status(context.Context, int64) (MirrorRow, error) { return f.row, nil }

func (f *fakeMirror) RequestPass(_ context.Context, _ meta.Tx, repoID int64, by string, full bool) error {
	f.requests = append(f.requests, mirrorRequest{repoID, by, full})
	return nil
}

// fakeMirrorQueue creates a job unless one is queued already, as an exclusive enqueue does.
type fakeMirrorQueue struct {
	specs    []jobs.EnqueueSpec
	existing *jobs.Job
}

func (f *fakeMirrorQueue) Enqueue(_ context.Context, _ meta.Tx, e jobs.EnqueueSpec) (jobs.Result, error) {
	f.specs = append(f.specs, e)
	if f.existing != nil {
		return jobs.Result{Job: *f.existing}, nil
	}
	return jobs.Result{Job: jobs.Job{ID: "0193f2c4-7a7e-7b51-9b1e-2f0d6c9e1a99", Kind: e.Kind, State: "queued"}, Created: true}, nil
}

type recordedEvents struct{ events []audit.Event }

func (r *recordedEvents) RecordTx(_ context.Context, _ meta.Tx, ev audit.Event) error {
	r.events = append(r.events, ev)
	return nil
}

func (r *recordedEvents) Record(_ context.Context, ev audit.Event) { r.events = append(r.events, ev) }

func mirrorSettings(mode string, mutate func(*repository.RemoteSettings)) repository.RemoteSettings {
	rs := repository.RemoteSettings{Mode: mode, Mirror: repository.DefaultMirrorSettings()}
	if mutate != nil {
		mutate(&rs)
	}
	return rs
}

type mirrorEnv struct {
	setup
	fm    *fakeMirror
	queue *fakeMirrorQueue
	audit *recordedEvents
	store *mirrorStore
}

func newMirrorEnv(t *testing.T, val *apitest.Validator, access Access, dec authz.Decider, mutate func(*fakeMirror)) mirrorEnv {
	t.Helper()
	fm := &fakeMirror{targets: map[string]MirrorTarget{
		"npm-mirror":  {ID: 7, Key: "npm-mirror", Class: repository.Remote, Settings: mirrorSettings(repository.ModeMirror, nil)},
		"npm-pull":    {ID: 8, Key: "npm-pull", Class: repository.Remote, Settings: mirrorSettings(repository.ModePullThrough, nil)},
		"npm-paused":  {ID: 9, Key: "npm-paused", Class: repository.Remote, Settings: mirrorSettings(repository.ModeMirror, func(r *repository.RemoteSettings) { r.Mirror.Paused = true })},
		"npm-local":   {ID: 10, Key: "npm-local", Class: repository.Local},
		"npm-virtual": {ID: 11, Key: "npm-virtual", Class: repository.Virtual},
	}}
	if mutate != nil {
		mutate(fm)
	}
	env := mirrorEnv{fm: fm, queue: &fakeMirrorQueue{}, audit: &recordedEvents{}, store: &mirrorStore{}}
	env.setup = newSetup(t, func(o *Options) {
		o.Access = access
		o.API.Config = apiConfigForTest()
		o.API.Config.MaxRequestBodyBytes = 1 << 20
		o.API.Store, o.API.Queue, o.API.Audit = env.store, env.queue, env.audit
		o.Repositories = RepositoryServices{Authz: dec}
		o.Mirror = MirrorServices{State: fm, Repositories: fm}
	}, authed)
	env.handler = val.Handler(t, env.handler)
	return env
}

// mirrorDecider answers as the engine does (R-AUTHZ-016): a refused manage is always 403, whatever else the caller holds, and
// only a refused read is hidden. manage and read say what the caller holds; unexposed is a repository that the request's
// endpoint does not expose (R-EP-013).
type mirrorDecider struct {
	authz.Decider
	authz.Authorizer
	manage, read, unexposed bool
}

func (d mirrorDecider) Begin(context.Context, authz.Subject, ...string) (*authz.Index, error) {
	return nil, nil
}

func (d mirrorDecider) Know(_ context.Context, idx *authz.Index, _ authz.Subject, _ string) (*authz.Index, error) {
	return idx, nil
}

func (d mirrorDecider) ExposedOn(_ *authz.Index, _ authz.Subject, keys []string) []string {
	if d.unexposed {
		return nil
	}
	return keys
}

func (d mirrorDecider) DecideOn(_ context.Context, _ *authz.Index, _ authz.Subject, c authz.Coordinate) authz.Decision {
	if len(c.Actions) != 1 {
		return authz.Decision{Outcome: authz.DenyVisible, Reason: "wrong coordinate"}
	}
	switch c.Actions[0] {
	case authz.Manage:
		if d.manage {
			return authz.Decision{Outcome: authz.Allow}
		}
		return authz.Decision{Outcome: authz.DenyVisible, Reason: authz.ReasonNoGrant, Missing: authz.Manage, Target: c.Repository}
	case authz.Read:
		if d.read {
			return authz.Decision{Outcome: authz.Allow}
		}
		return authz.Decision{Outcome: authz.DenyHidden}
	}
	return authz.Decision{Outcome: authz.DenyVisible, Reason: "wrong coordinate"}
}

const (
	mirrorPath = "/api/v1/repositories/npm-mirror/mirror"
	runPath    = "/api/v1/repositories/npm-mirror/mirror/run"
)

// The callers, one right each. cacheDecider answers the decision on manage.
var (
	readOnlyCaller   = projectCaller{}
	readConfigCaller = projectCaller{caps: map[string]bool{capabilityReadConfig: true}}
	writeConfigCall  = projectCaller{caps: map[string]bool{capabilityWriteConfig: true}}
	webAdmin         = projectCaller{
		projects:     []authz.AdministeredProject{{Key: "web", ManagePolicies: true, ManageRepositories: true}},
		repositories: map[string]string{"npm-mirror": "web", "npm-pull": "web"},
	}
	labAdmin = projectCaller{ // administers the project of the repository, with manageRepositories off
		projects:     []authz.AdministeredProject{{Key: "lab", ManagePolicies: true}},
		repositories: map[string]string{"npm-mirror": "lab"},
	}
	shopAdmin = projectCaller{ // administers a project that does not hold the repository
		projects:     []authz.AdministeredProject{{Key: "shop", ManagePolicies: true, ManageRepositories: true}},
		repositories: map[string]string{"npm-mirror": "web"},
	}
)

func TestR_REPO_056_MirrorStatusPermissions(t *testing.T) {
	val := apitest.New(t, EmbeddedYAML())
	get := func(access Access, dec authz.Decider) *mirrorEnv {
		e := newMirrorEnv(t, val, access, dec, nil)
		return &e
	}
	t.Run("R-REPO-056 a caller with read on the repository and nothing else gets 403", func(t *testing.T) {
		e := get(readOnlyCaller, mirrorDecider{read: true})
		problem(t, e.do("GET", mirrorPath, "", nil), 403, "forbidden")
	})
	t.Run("R-REPO-056 R-AUTHZ-016 a repository hidden from the caller answers 404; one that no endpoint exposes also to a configuration reader", func(t *testing.T) {
		problem(t, get(readOnlyCaller, mirrorDecider{}).do("GET", mirrorPath, "", nil), 404, "not_found")
		problem(t, get(shopAdmin, mirrorDecider{}).do("GET", mirrorPath, "", nil), 404, "not_found")
		// An endpoint that does not expose the repository hides it from a configuration reader too (R-EP-013).
		problem(t, get(readConfigCaller, mirrorDecider{read: true, unexposed: true}).do("GET", mirrorPath, "", nil), 404, "not_found")
		problem(t, get(webAdmin, mirrorDecider{read: true, unexposed: true}).do("GET", mirrorPath, "", nil), 404, "not_found")
	})
	t.Run("R-REPO-056 a holder of repositories:read-config gets 200, also without a read on the repository", func(t *testing.T) {
		if w := get(readConfigCaller, mirrorDecider{}).do("GET", mirrorPath, "", nil); w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	})
	t.Run("R-REPO-056 a holder of manage on the repository gets 200, also without a read", func(t *testing.T) {
		if w := get(readOnlyCaller, mirrorDecider{manage: true}).do("GET", mirrorPath, "", nil); w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	})
	t.Run("R-REPO-056 R-AUTHZ-026 an administrator of the repository's project gets 200, whatever the switch", func(t *testing.T) {
		for name, c := range map[string]projectCaller{"manageRepositories on": webAdmin, "manageRepositories off": labAdmin} {
			if w := get(c, mirrorDecider{}).do("GET", mirrorPath, "", nil); w.Code != 200 {
				t.Errorf("%s: %d %s", name, w.Code, w.Body)
			}
		}
	})
	t.Run("R-REPO-056 an administrator of another project is a caller with nothing", func(t *testing.T) {
		problem(t, get(shopAdmin, mirrorDecider{read: true}).do("GET", mirrorPath, "", nil), 403, "forbidden")
	})
	t.Run("R-REPO-054 a run during the upstream's wait answers 409 mirror_rate_limited naming until, and a wait that is over does not", func(t *testing.T) {
		until := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
		e := newMirrorEnv(t, val, writeConfigCall, mirrorDecider{read: true}, func(f *fakeMirror) {
			f.row = MirrorRow{HasState: true, RateLimitedUntil: &until}
		})
		m := problem(t, e.do("POST", runPath, "", nil), 409, "mirror_rate_limited")
		if m["until"] != until.Format(time.RFC3339) {
			t.Errorf("until = %v, want %s", m["until"], until.Format(time.RFC3339))
		}
		if len(e.queue.specs) != 0 || e.store.commits != 0 {
			t.Errorf("the refused run wrote: %+v commits %d", e.queue.specs, e.store.commits)
		}
		past := time.Now().UTC().Add(-time.Minute)
		e2 := newMirrorEnv(t, val, writeConfigCall, mirrorDecider{read: true}, func(f *fakeMirror) {
			f.row = MirrorRow{HasState: true, RateLimitedUntil: &past}
		})
		if w := e2.do("POST", runPath, "", nil); w.Code != 202 {
			t.Fatalf("a wait that is over refused the run: %d %s", w.Code, w.Body)
		}
	})
	t.Run("R-REPO-056 a local and a virtual repository answer 400 not_applicable, an unknown one 404", func(t *testing.T) {
		e := get(readConfigCaller, mirrorDecider{})
		problem(t, e.do("GET", "/api/v1/repositories/npm-local/mirror", "", nil), 400, "not_applicable")
		problem(t, e.do("GET", "/api/v1/repositories/npm-virtual/mirror", "", nil), 400, "not_applicable")
		problem(t, e.do("GET", "/api/v1/repositories/nothing-here/mirror", "", nil), 404, "repository_not_found")
	})
	t.Run("an unwired database is 503", func(t *testing.T) {
		s := newSetup(t, func(o *Options) {
			o.Access, o.API.Config = readConfigCaller, apiConfigForTest()
			o.Repositories = RepositoryServices{Authz: mirrorDecider{}}
		}, authed)
		problem(t, s.do("GET", mirrorPath, "", nil), 503, "unavailable")
	})
}

func at(s string) *time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return &t
}

func TestR_REPO_056_MirrorStatusDocument(t *testing.T) {
	val := apitest.New(t, EmbeddedYAML())
	max := int64(107374182400)
	status := func(t *testing.T, key string, mutate func(*fakeMirror)) map[string]any {
		t.Helper()
		e := newMirrorEnv(t, val, readConfigCaller, mirrorDecider{}, mutate)
		w := e.do("GET", "/api/v1/repositories/"+key+"/mirror", "", nil)
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		var m map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	running := MirrorRow{
		HasState: true, NextPassAt: at("2026-11-04T11:00:00Z"), LastFullPassAt: at("2026-11-03T13:12:09Z"),
		LastPass: []byte(`{"kind":"full","method":"feed","startedAt":"2026-11-03T10:00:00Z","finishedAt":"2026-11-03T13:12:09Z",` +
			`"outcome":"complete","packages":1200,"packagesFailed":0,"itemsStored":5021,"bytesStored":9126805504,` +
			`"discoveryComplete":true,"discoveryNote":null}`),
		PassStartedAt: at("2026-11-04T10:00:00Z"), PassKind: "incremental", PassMethod: "feed",
		PassJobID: "0193f2c4-7a7e-7b51-9b1e-2f0d6c9e1a10", JobKind: "mirror-pass", JobState: "running",
		PassNames: 120, PassPackagesDone: 80, PassItemsStored: 311, PassBytesStored: 52428800, Pending: 40,
		RateLimitWaits: 12, RateLimitWaitSecs: 340, LastRateLimitAt: at("2026-11-04T10:20:31Z"), LastRateLimitStatus: new(429),
		UsedBytes: 9200000000,
	}
	t.Run("R-REPO-056 the document has the members and values of the specified example", func(t *testing.T) {
		got := status(t, "npm-mirror", func(f *fakeMirror) {
			tg := f.targets["npm-mirror"]
			tg.Settings.Mirror.MaxBytes = &max
			f.targets["npm-mirror"] = tg
			f.row = running
		})
		var want map[string]any
		_ = json.Unmarshal([]byte(`{
		  "mode": "mirror", "state": "running",
		  "pass": {"kind": "incremental", "method": "feed", "startedAt": "2026-11-04T10:00:00Z",
		    "namesDiscovered": 120, "packagesDone": 80, "packagesPending": 40, "packagesFailed": 0,
		    "itemsStored": 311, "bytesStored": 52428800, "discoveryDone": false,
		    "job": {"id": "0193f2c4-7a7e-7b51-9b1e-2f0d6c9e1a10", "kind": "mirror-pass", "state": "running", "href": "/api/v1/system/jobs/0193f2c4-7a7e-7b51-9b1e-2f0d6c9e1a10"}},
		  "lastCompletePass": {"kind": "full", "method": "feed", "startedAt": "2026-11-03T10:00:00Z", "finishedAt": "2026-11-03T13:12:09Z",
		    "outcome": "complete", "packages": 1200, "packagesFailed": 0, "itemsStored": 5021, "bytesStored": 9126805504,
		    "discoveryComplete": true, "discoveryNote": null},
		  "nextPassAt": "2026-11-04T11:00:00Z", "lastFullPassAt": "2026-11-03T13:12:09Z",
		  "errors": {"consecutiveFailures": 0, "lastAt": null, "lastCode": null, "lastMessage": null},
		  "rateLimit": {"waits": 12, "waitSeconds": 340, "lastAt": "2026-11-04T10:20:31Z", "lastStatus": 429, "until": null},
		  "bound": {"reached": null, "at": null, "maxBytes": 107374182400, "usedBytes": 9200000000}
		}`), &want)
		if !reflect.DeepEqual(got, want) {
			g, _ := json.MarshalIndent(got, "", " ")
			w, _ := json.MarshalIndent(want, "", " ")
			t.Fatalf("got\n%s\nwant\n%s", g, w)
		}
	})
	t.Run("R-REPO-056 a remote in mode mirror without a state row answers scheduled with nulls", func(t *testing.T) {
		got := status(t, "npm-mirror", nil)
		if got["state"] != "scheduled" || got["mode"] != "mirror" || got["pass"] != nil || got["lastCompletePass"] != nil ||
			got["nextPassAt"] != nil || got["lastFullPassAt"] != nil {
			t.Fatalf("%v", got)
		}
		e := got["errors"].(map[string]any)
		if e["consecutiveFailures"] != float64(0) || e["lastMessage"] != nil {
			t.Errorf("errors = %v", e)
		}
		b := got["bound"].(map[string]any)
		if b["reached"] != nil || b["maxBytes"] != nil || b["usedBytes"] != float64(0) {
			t.Errorf("bound = %v", b)
		}
	})
	t.Run("R-REPO-056 a remote in mode pullThrough is off, and keeps what the engine recorded", func(t *testing.T) {
		got := status(t, "npm-pull", func(f *fakeMirror) { f.row = running })
		if got["state"] != "off" || got["mode"] != "pullThrough" {
			t.Errorf("%v %v", got["state"], got["mode"])
		}
		if got["lastCompletePass"] == nil {
			t.Error("the last complete pass stays after the mode is turned back (Q-REPO-148)")
		}
	})
	t.Run("R-REPO-056 Q-API-196 the state is the first that holds of off, paused, failing, bound_reached, running, waiting, scheduled", func(t *testing.T) {
		mirrorRS := mirrorSettings(repository.ModeMirror, nil)
		pausedRS := mirrorSettings(repository.ModeMirror, func(r *repository.RemoteSettings) { r.Mirror.Paused = true })
		pullRS := mirrorSettings(repository.ModePullThrough, func(r *repository.RemoteSettings) { r.Mirror.Paused = true })
		inPass := func(job string, mutate func(*MirrorRow)) MirrorRow {
			r := MirrorRow{HasState: true, PassStartedAt: at("2026-11-04T10:00:00Z"), PassKind: "full", JobState: job}
			if mutate != nil {
				mutate(&r)
			}
			return r
		}
		for _, c := range []struct {
			name string
			rs   repository.RemoteSettings
			row  MirrorRow
			want string
		}{
			{"pullThrough beats paused", pullRS, MirrorRow{ConsecutiveFailures: 9}, "off"},
			{"paused beats failing", pausedRS, MirrorRow{ConsecutiveFailures: 9, BoundReached: "bytes"}, "paused"},
			{"two failures do not fail", mirrorRS, MirrorRow{ConsecutiveFailures: 2}, "scheduled"},
			{"three failures fail", mirrorRS, MirrorRow{ConsecutiveFailures: 3}, "failing"},
			{"failing beats a bound", mirrorRS, MirrorRow{ConsecutiveFailures: 4, BoundReached: "quota"}, "failing"},
			{"a bound beats a running pass", mirrorRS, inPass("running", func(r *MirrorRow) { r.BoundReached = "capacity" }), "bound_reached"},
			{"no pass in progress", mirrorRS, MirrorRow{HasState: true}, "scheduled"},
			{"a pass whose job runs", mirrorRS, inPass("running", nil), "running"},
			{"a pass whose job is being cancelled still runs", mirrorRS, inPass("cancelling", nil), "running"},
			{"a pass whose job waits in the queue", mirrorRS, inPass("queued", nil), "waiting"},
			{"a pass whose job ended", mirrorRS, inPass("failed", nil), "waiting"},
			{"a pass whose job row is gone", mirrorRS, inPass("", nil), "waiting"},
		} {
			if got := string(mirrorStateOf(c.rs, c.row)); got != c.want {
				t.Errorf("%s: %s, want %s", c.name, got, c.want)
			}
		}
	})
	t.Run("R-REPO-056 a paused repository answers paused through the router", func(t *testing.T) {
		if got := status(t, "npm-paused", nil); got["state"] != "paused" {
			t.Errorf("state = %v", got["state"])
		}
	})
	t.Run("R-REPO-056 the pass names no job once the job row is gone, and the bound shows what stopped it", func(t *testing.T) {
		got := status(t, "npm-mirror", func(f *fakeMirror) {
			r := running
			r.JobKind, r.JobState = "", ""
			r.BoundReached, r.BoundReachedAt = "bytes", at("2026-11-04T10:30:00Z")
			f.row = r
		})
		if got["pass"].(map[string]any)["job"] != nil {
			t.Errorf("job = %v", got["pass"])
		}
		if b := got["bound"].(map[string]any); b["reached"] != "bytes" || b["at"] != "2026-11-04T10:30:00Z" {
			t.Errorf("bound = %v", b)
		}
	})
	t.Run("R-REPO-056 the stored error is shown as stored, with its code and time", func(t *testing.T) {
		got := status(t, "npm-mirror", func(f *fakeMirror) {
			f.row = MirrorRow{
				HasState: true, ConsecutiveFailures: 3, LastErrorAt: at("2026-11-04T09:00:00Z"), LastErrorCode: "discovery_failed",
				LastError: "discovery at registry.example.org answered 500",
			}
		})
		e := got["errors"].(map[string]any)
		if got["state"] != "failing" || e["lastCode"] != "discovery_failed" || e["lastMessage"] != "discovery at registry.example.org answered 500" ||
			e["lastAt"] != "2026-11-04T09:00:00Z" || e["consecutiveFailures"] != float64(3) {
			t.Errorf("%v / %v", got["state"], e)
		}
	})
	t.Run("R-REPO-056 an unreadable summary of the last pass is no last pass, not an error", func(t *testing.T) {
		for _, raw := range []string{``, `null`, `{}`, `[1]`, `{"outcome":7}`} {
			if got := decodeLastPass([]byte(raw)); got != nil {
				t.Errorf("%q: %+v", raw, got)
			}
		}
	})
	t.Run("R-API-002 R-REPO-056 every answer was validated against the document", func(t *testing.T) {
		if missing := val.Uncovered("getRepositoryMirror"); len(missing) > 0 {
			t.Fatalf("not reached: %v", missing)
		}
	})
}

func TestR_REPO_056_MirrorRun(t *testing.T) {
	val := apitest.New(t, EmbeddedYAML())
	run := func(t *testing.T, access Access, dec authz.Decider, path, body string, mutate func(*fakeMirror)) (mirrorEnv, *httptest.ResponseRecorder) {
		t.Helper()
		e := newMirrorEnv(t, val, access, dec, mutate)
		return e, e.do("POST", path, body, nil)
	}
	t.Run("R-REPO-056 write-config, manage and a project administrator with manageRepositories each get 202 with the job", func(t *testing.T) {
		for name, c := range map[string]struct {
			access Access
			dec    authz.Decider
		}{
			"repositories:write-config": {writeConfigCall, mirrorDecider{}},
			"manage":                    {readOnlyCaller, mirrorDecider{manage: true}},
			"project administrator":     {webAdmin, mirrorDecider{}},
		} {
			e, w := run(t, c.access, c.dec, runPath, "", nil)
			if w.Code != 202 {
				t.Errorf("%s: %d %s", name, w.Code, w.Body)
				continue
			}
			var env struct {
				Job struct{ ID, Kind, State, Href string }
			}
			_ = json.Unmarshal(w.Body.Bytes(), &env)
			if env.Job.ID != "0193f2c4-7a7e-7b51-9b1e-2f0d6c9e1a99" || env.Job.Kind != mirror.KindPass || env.Job.State != "queued" ||
				env.Job.Href != "/api/v1/system/jobs/"+env.Job.ID {
				t.Errorf("%s: job = %+v", name, env.Job)
			}
			if loc := w.Header().Get("Location"); !strings.HasSuffix(loc, "/api/v1/system/jobs/"+env.Job.ID) {
				t.Errorf("%s: Location = %q", name, loc)
			}
			if len(e.fm.requests) != 1 {
				t.Errorf("%s: requests = %+v", name, e.fm.requests)
			}
		}
	})
	t.Run("R-REPO-056 a holder of read-config and a reader get 403, a hidden repository 404", func(t *testing.T) {
		for name, c := range map[string]struct {
			access Access
			dec    authz.Decider
			status int
			code   string
		}{
			"read-config only":             {readConfigCaller, mirrorDecider{}, 403, "forbidden"},
			"read only":                    {readOnlyCaller, mirrorDecider{read: true}, 403, "forbidden"},
			"hidden":                       {readOnlyCaller, mirrorDecider{}, 404, "not_found"},
			"unexposed, for write-config":  {writeConfigCall, mirrorDecider{read: true, unexposed: true}, 404, "not_found"},
			"administrator of its project": {shopAdmin, mirrorDecider{read: true}, 403, "forbidden"},
			"administrator, other, hidden": {shopAdmin, mirrorDecider{}, 404, "not_found"},
		} {
			e, w := run(t, c.access, c.dec, runPath, "", nil)
			problem(t, w, c.status, c.code)
			if len(e.queue.specs) != 0 || len(e.fm.requests) != 0 || len(e.audit.events) != 0 {
				t.Errorf("%s: the refused request did something: %+v %+v %+v", name, e.queue.specs, e.fm.requests, e.audit.events)
			}
		}
	})
	t.Run("R-REPO-056 Q-API-199 an administrator whose manageRepositories is off holds manage on the project's repositories and may run", func(t *testing.T) {
		e, w := run(t, labAdmin, mirrorDecider{manage: true}, runPath, "", nil)
		if w.Code != 202 || len(e.queue.specs) != 1 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	})
	t.Run("R-REPO-056 the pass is enqueued exclusive under its key with the arguments of the engine", func(t *testing.T) {
		e, w := run(t, writeConfigCall, mirrorDecider{read: true}, runPath, "", nil)
		if w.Code != 202 || len(e.queue.specs) != 1 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		spec := e.queue.specs[0]
		if spec.Kind != mirror.KindPass || spec.DedupKey != mirror.PassKey(7) || spec.DedupKey != "mirror-pass:7" ||
			spec.TargetKind != "repository" || spec.TargetKey != "npm-mirror" || spec.CreatedBy != callerUUID {
			t.Errorf("spec = %+v", spec)
		}
		if args, ok := spec.Args.(mirror.PassArgs); !ok || args != (mirror.PassArgs{Repository: "npm-mirror", RepositoryID: 7}) {
			t.Errorf("args = %#v", spec.Args)
		}
		if want := []mirrorRequest{{repoID: 7, by: callerUUID}}; !reflect.DeepEqual(e.fm.requests, want) {
			t.Errorf("requests = %+v", e.fm.requests)
		}
	})
	t.Run("R-REPO-056 full true makes the requested pass full, in the request, the job and the audit event", func(t *testing.T) {
		e, w := run(t, writeConfigCall, mirrorDecider{read: true}, runPath, `{"full": true}`, nil)
		if w.Code != 202 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		if got := e.fm.requests; len(got) != 1 || !got[0].full {
			t.Errorf("requests = %+v", got)
		}
		if args := e.queue.specs[0].Args.(mirror.PassArgs); !args.Full {
			t.Errorf("args = %+v", args)
		}
		var d map[string]any
		_ = json.Unmarshal(e.audit.events[0].Details, &d)
		if d["full"] != true {
			t.Errorf("details = %v", d)
		}
	})
	t.Run("R-REPO-057 the request writes mirror.pass_requested in the transaction, with the caller, the repository and the job", func(t *testing.T) {
		e, w := run(t, writeConfigCall, mirrorDecider{read: true}, runPath, "", nil)
		if w.Code != 202 || len(e.audit.events) != 1 {
			t.Fatalf("%d events %+v", w.Code, e.audit.events)
		}
		ev := e.audit.events[0]
		if ev.Type != "mirror.pass_requested" || ev.Outcome != audit.Success ||
			ev.Repository != "npm-mirror" || ev.Subject.Kind != "repository" || ev.Subject.ID != "7" || ev.Subject.Name != "npm-mirror" {
			t.Errorf("event = %+v", ev)
		}
		if ev.Actor.ID != callerUUID {
			t.Errorf("actor = %+v", ev.Actor)
		}
		var d map[string]any
		_ = json.Unmarshal(ev.Details, &d)
		if d["job"] != "0193f2c4-7a7e-7b51-9b1e-2f0d6c9e1a99" || d["full"] != false {
			t.Errorf("details = %v", d)
		}
		if e.store.commits != 1 {
			t.Errorf("commits = %d", e.store.commits)
		}
		if et, ok := audit.Lookup("mirror.pass_requested"); !ok || et.Mode != audit.Transactional || audit.EventMirrorPassRequested != "mirror.pass_requested" {
			t.Errorf("type = %+v %v", et, ok)
		}
	})
	t.Run("R-REPO-056 a queued or running pass answers 409 mirror_pass_running naming its job, and records nothing", func(t *testing.T) {
		existing := &jobs.Job{ID: "0193f2c4-7a7e-7b51-9b1e-2f0d6c9e1a10", Kind: mirror.KindPass, State: "running"}
		e := newMirrorEnv(t, val, writeConfigCall, mirrorDecider{read: true}, nil)
		e.queue.existing = existing
		w := e.do("POST", runPath, "", nil)
		m := problem(t, w, 409, "mirror_pass_running")
		job, _ := m["job"].(map[string]any)
		if job["id"] != existing.ID || job["href"] != "/api/v1/system/jobs/"+existing.ID || job["state"] != "running" {
			t.Errorf("job = %v", m["job"])
		}
		if !strings.Contains(m["detail"].(string), existing.ID) {
			t.Errorf("detail = %v", m["detail"])
		}
		if len(e.fm.requests) != 0 || len(e.audit.events) != 0 || e.store.commits != 0 {
			t.Errorf("the refused run wrote: %+v %+v commits %d", e.fm.requests, e.audit.events, e.store.commits)
		}
	})
	t.Run("R-REPO-056 a remote in mode pullThrough answers 409 mirror_off", func(t *testing.T) {
		e, w := run(t, writeConfigCall, mirrorDecider{read: true}, "/api/v1/repositories/npm-pull/mirror/run", "", nil)
		problem(t, w, 409, "mirror_off")
		if len(e.queue.specs) != 0 {
			t.Errorf("enqueued %+v", e.queue.specs)
		}
	})
	t.Run("R-REPO-056 a paused mirror answers 409 mirror_paused", func(t *testing.T) {
		e, w := run(t, writeConfigCall, mirrorDecider{read: true}, "/api/v1/repositories/npm-paused/mirror/run", "", nil)
		problem(t, w, 409, "mirror_paused")
		if len(e.queue.specs) != 0 {
			t.Errorf("enqueued %+v", e.queue.specs)
		}
	})
	t.Run("R-REPO-056 a local and a virtual repository answer 400 not_applicable, an unknown one 404", func(t *testing.T) {
		for _, key := range []string{"npm-local", "npm-virtual"} {
			e, w := run(t, writeConfigCall, mirrorDecider{read: true}, "/api/v1/repositories/"+key+"/mirror/run", "", nil)
			problem(t, w, 400, "not_applicable")
			if len(e.queue.specs) != 0 {
				t.Errorf("%s: enqueued", key)
			}
		}
		_, w := run(t, writeConfigCall, mirrorDecider{read: true}, "/api/v1/repositories/nothing-here/mirror/run", "", nil)
		problem(t, w, 404, "repository_not_found")
	})
	t.Run("R-API-003 a body that is not the request is refused before anything is enqueued", func(t *testing.T) {
		for body, status := range map[string]int{`{"full":"yes"}`: 400, `{"ful":true}`: 400, `{"full":`: 400} {
			e, w := run(t, writeConfigCall, mirrorDecider{read: true}, runPath, body, nil)
			if w.Code != status || len(e.queue.specs) != 0 {
				t.Errorf("%s: %d %s", body, w.Code, w.Body)
			}
		}
		e := newMirrorEnv(t, val, writeConfigCall, mirrorDecider{read: true}, nil)
		problem(t, e.do("POST", runPath, `{}`, map[string]string{"Content-Type": "text/plain"}), 415, "unsupported_media_type")
	})
	t.Run("an unwired queue is 503", func(t *testing.T) {
		s := newSetup(t, func(o *Options) {
			o.Access, o.API.Config = writeConfigCall, apiConfigForTest()
			o.Repositories = RepositoryServices{Authz: mirrorDecider{read: true}}
			o.Mirror = MirrorServices{State: &fakeMirror{}}
		}, authed)
		problem(t, s.do("POST", runPath, "", nil), 503, "unavailable")
	})
	t.Run("R-API-002 R-REPO-056 every answer was validated against the document", func(t *testing.T) {
		if missing := val.Uncovered("runRepositoryMirror"); len(missing) > 0 {
			t.Fatalf("not reached: %v", missing)
		}
	})
}

func TestR_REPO_056_ErrorCodesAreCataloged(t *testing.T) {
	for _, c := range []string{errs.CodeMirrorPassRunning, errs.CodeMirrorOff, errs.CodeMirrorPaused, errs.CodeMirrorRateLimited} {
		if _, ok := errs.Lookup(c); !ok {
			t.Errorf("%s is not in the catalog", c)
		}
	}
}
