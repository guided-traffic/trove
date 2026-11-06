//go:build e2e

package e2e

// The client load and the fault harness of the Bastion's failure suite (R-BAS-049, R-HA-052): real clients through a Bastion,
// recorded per command, a sampler of the Bastion's link gauges, and faults run one after the other under the same load.

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// basLoadCmd is one command of a client loop.
type basLoadCmd struct {
	Loop  string    `json:"loop"`
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	OK    bool      `json:"ok"`
	Log   string    `json:"log,omitempty"` // the tail of a failed command's output
}

// basLoad is the load of one scenario through one Bastion: three loops that run back to back until stop.
//
//	npm ci         the warm project of the set, an empty client cache each run: documents and tarballs answered by the Bastion
//	dotnet restore the warm NuGet project, an empty client cache each run
//	npm cold       one package that no client has asked the Bastion for yet, a new one each run: every run is a miss that the
//	               Bastion forwards to the main installation (R-BAS-021), so a fault there meets a forward and not only a hit
type basLoad struct {
	name     string
	site     *basSite
	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
	mu       sync.Mutex
	cmds     []basLoadCmd
	coldPref string
	coldN    int
	coldUsed atomic.Int64
	coldDone atomic.Bool // the pool ran out before the load was stopped
}

// basColdMarker is in the name of every package of the cold loop: the checks of warm content leave these out.
const basColdMarker = "-lc-"

// basLoopPause is the pause after a command of each loop. A client address that sends more than the Bastion's
// limits.authenticated.requests_per_second (50, burst 200, R-BAS-035) is answered 429 rate_limited and its client retries after 10 s and 60 s:
// back to back, an `npm ci` of 20 tarballs from the cluster's fast clients is 200 requests a second and tests the limiter, not the fault.
// The pauses keep each loop's address at about 20 requests a second, the pace of a CI fleet's runner.
var basLoopPause = map[string]time.Duration{"npm ci": 2 * time.Second, "dotnet restore": 2 * time.Second, "npm cold": time.Second}

// basSlowCommand is the duration above which a command that succeeded keeps the tail of its output in the record.
const basSlowCommand = 15 * time.Second

// startLoad prepares the clients (one warm run of each loop first), publishes the cold packages to the main installation and
// starts the loops. The load is halted when the test ends, whatever happens.
func (e *basEnv) startLoad(t *testing.T, site *basSite, name string, coldN int) *basLoad {
	t.Helper()
	l := &basLoad{name: name, site: site, stopCh: make(chan struct{}), coldN: coldN, coldPref: "bas" + e.run + basColdMarker + name + "-"}
	setN := e.npmSets["npm"]
	cn := e.npmClient(t, "npm", 0, "ld-"+name, e.full.Token)
	dir := e.basNPMProject(t, cn, setN, "load", site)
	nc := e.nugetClient(t, 0, "ld-"+name, e.full.Token)
	e.configureNuGet(t, nc, site.reg)
	project := e.nuget.project(nc, "consumer")
	cn.basInstall(t, dir, setN)
	e.nuget.restore(t, nc, project)

	cc := e.npmClient(t, "npm", 1, "ldc-"+name, e.full.Token)
	cdir := cc.project("cold", map[string]any{"name": "cold-consumer"})
	e.configureNPM(t, cc, cdir, site.reg)
	pubStart := time.Now()
	for i := range coldN {
		e.publishNPM(t, basNPMRepo, l.coldPref+itoa(i), "1.0.0", nil)
	}
	t.Logf("%d cold packages published in %s", coldN, time.Since(pubStart).Round(time.Second))
	time.Sleep(3 * time.Second) // the new packages reach every replica of the main installation

	t.Cleanup(l.halt)
	l.loop("npm ci", func() (string, bool, bool) {
		cn.wipeClientState(dir)
		// loglevel http prints every request with its status, which tells a retry from a slow answer
		r := cn.exec(dir, []string{"npm_config_loglevel=http"}, append([]string{cn.row}, cn.frozenInstall()...)...)
		return r.Out, r.Code == 0, false
	})
	l.loop("dotnet restore", func() (string, bool, bool) {
		if out, err := nc.try("sh", "-c", "rm -rf packages httpcache obj plugins"); err != nil {
			return out, false, false
		}
		out, err := nc.try("dotnet", "restore", project, "--nologo")
		return out, err == nil, false
	})
	l.loop("npm cold", func() (string, bool, bool) {
		i := int(l.coldUsed.Add(1)) - 1
		if i >= l.coldN {
			l.coldDone.Store(true)
			return "", true, true
		}
		cc.wipeClientState(cdir)
		r := cc.run(cdir, "install", "--no-save", l.coldPref+itoa(i)+"@1.0.0")
		return r.Out, r.Code == 0, false
	})
	return l
}

func (l *basLoad) loop(kind string, f func() (out string, ok, skip bool)) {
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		for {
			select {
			case <-l.stopCh:
				return
			default:
			}
			c := basLoadCmd{Loop: kind, Start: time.Now()}
			out, ok, skip := f()
			if skip {
				time.Sleep(time.Second) // the cold pool ran out; the check after the stop reports it
				continue
			}
			c.End, c.OK = time.Now(), ok
			if !ok || c.End.Sub(c.Start) > basSlowCommand {
				c.Log = tailLines(out, 60) // a slow success is kept too: a retry shows in the client's output
			}
			l.mu.Lock()
			l.cmds = append(l.cmds, c)
			l.mu.Unlock()
			time.Sleep(basLoopPause[kind])
		}
	}()
}

// halt ends the loops after the command each is running; it is the cleanup of a test that stopped early.
func (l *basLoad) halt() {
	l.stopOnce.Do(func() { close(l.stopCh) })
	l.wg.Wait()
}

// commands is a copy of the recorded commands.
func (l *basLoad) commands() []basLoadCmd {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]basLoadCmd(nil), l.cmds...)
}

// overlapping are the commands of the load that ran at any time in [from, to].
func overlapping(cmds []basLoadCmd, from, to time.Time) []basLoadCmd {
	var out []basLoadCmd
	for _, c := range cmds {
		if c.Start.Before(to) && c.End.After(from) {
			out = append(out, c)
		}
	}
	return out
}

// ── the sampler of the Bastion's link ────────────────────────────────────────

// basSample is the link gauges of the ready replicas of a release at one time.
type basSample struct {
	At        time.Time
	Mismatch  []float64
	Reachable []float64
}

// basWatch reads trove_bastion_interface_mismatch and trove_bastion_main_reachable of every running replica every two
// seconds. A replica that cannot be read at that moment (starting, terminating) is left out of the sample.
type basWatch struct {
	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
	mu       sync.Mutex
	samples  []basSample
}

const basWatchEvery = 2 * time.Second

func startBasWatch(site *basSite) *basWatch {
	w := &basWatch{stopCh: make(chan struct{})}
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		for {
			select {
			case <-w.stopCh:
				return
			case <-time.After(basWatchEvery):
			}
			names, err := kbTry("get", "pods", "-l", site.selector()+",app.kubernetes.io/component=bastion", "--field-selector=status.phase=Running",
				"-o", `jsonpath={range .items[*]}{.metadata.name}{"\n"}{end}`)
			if err != nil {
				continue
			}
			s := basSample{At: time.Now()}
			for _, n := range strings.Fields(names) {
				code, body := basOps(n, "/metrics")
				if code != 200 {
					continue
				}
				m := parseMetrics(body)
				s.Mismatch = append(s.Mismatch, m["trove_bastion_interface_mismatch"])
				s.Reachable = append(s.Reachable, m["trove_bastion_main_reachable"])
			}
			if len(s.Reachable) > 0 {
				w.mu.Lock()
				w.samples = append(w.samples, s)
				w.mu.Unlock()
			}
		}
	}()
	return w
}

func (w *basWatch) stop() []basSample {
	w.stopOnce.Do(func() { close(w.stopCh) })
	w.wg.Wait()
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]basSample(nil), w.samples...)
}

// linkStats are the facts about the link in [from, to]: the highest mismatch gauge, and the longest stretch in which every
// readable replica reported the main installation unreachable (what TroveBastionMainUnreachable, `for: 2m`, reads) and the
// longest in which any did (TroveBastionMainUnreachableSingleReplica, `for: 5m`).
type linkStats struct {
	Samples     int
	MaxMismatch float64
	AllDown     time.Duration
	AnyDown     time.Duration
}

func linkIn(samples []basSample, from, to time.Time) linkStats {
	var st linkStats
	var allFrom, anyFrom time.Time
	for _, s := range samples {
		if s.At.Before(from) || s.At.After(to) {
			continue
		}
		st.Samples++
		all, anyD := true, false
		for i, r := range s.Reachable {
			st.MaxMismatch = max(st.MaxMismatch, s.Mismatch[i])
			if r == 0 {
				anyD = true
			} else {
				all = false
			}
		}
		if all {
			if allFrom.IsZero() {
				allFrom = s.At
			}
			st.AllDown = max(st.AllDown, s.At.Sub(allFrom)+basWatchEvery)
		} else {
			allFrom = time.Time{}
		}
		if anyD {
			if anyFrom.IsZero() {
				anyFrom = s.At
			}
			st.AnyDown = max(st.AnyDown, s.At.Sub(anyFrom)+basWatchEvery)
		} else {
			anyFrom = time.Time{}
		}
	}
	return st
}

// ── the faults ───────────────────────────────────────────────────────────────

// basFault is one injection under the load of underLoad.
type basFault struct {
	name        string // the subtest's name, with the requirement IDs
	run         func()
	measureMain bool                                  // record what the Bastions asked the main installation from the start of run to the end of the settle time
	check       func(t *testing.T, f *basFaultRecord) // the scenario's own conditions, after the common ones
}

// basFaultRecord is what a fault left behind.
type basFaultRecord struct {
	basFault
	From, To, End time.Time // To is the end of run, End the end of the settle time
	Main          []mainReq
	Cmds          []basLoadCmd
	Link          linkStats
}

const (
	basLoadLead   = 30 * time.Second // the load runs alone before the first fault
	basLoadSettle = 30 * time.Second // and after each fault
)

// underLoad starts the load through the site, runs the faults one after the other (each followed by the settle time), stops
// the load and judges: for each fault a subtest with the common conditions - every command that ran during the fault or
// its settle time exited 0 with the client's own retries, every loop met the fault, the interface never mismatched, the
// replicas were never all unreachable for the alert's two minutes - and the fault's own. A failed command outside every
// fault, and a cold pool that ran out, fail the test itself.
func (e *basEnv) underLoad(t *testing.T, site *basSite, name string, coldN int, faults []basFault) {
	t.Helper()
	l := e.startLoad(t, site, name, coldN)
	watch := startBasWatch(site)
	t.Cleanup(func() { watch.stop() })
	loadStart := time.Now()
	time.Sleep(basLoadLead)
	recs := make([]*basFaultRecord, len(faults))
	for i, f := range faults {
		r := &basFaultRecord{basFault: f}
		var w *mainWindow
		if f.measureMain {
			w = openMainWindow(t)
		}
		r.From = time.Now()
		f.run()
		r.To = time.Now()
		t.Logf("%s: the fault took %s", f.name, r.To.Sub(r.From).Round(time.Second))
		time.Sleep(basLoadSettle)
		r.End = time.Now()
		if w != nil {
			r.Main = w.done()
		}
		recs[i] = r
	}
	l.halt()
	samples := watch.stop()
	cmds := l.commands()
	noteResult(t, "bastion-load-"+site.variant+"-"+name, map[string]any{"commands": cmds, "samples": len(samples), "faults": recs})

	perLoop := map[string][2]int{}
	for _, c := range cmds {
		n := perLoop[c.Loop]
		n[0]++
		if !c.OK {
			n[1]++
		}
		perLoop[c.Loop] = n
	}
	t.Logf("%s through %s: %d commands in %s, per loop (runs, failed) %v", name, site.release, len(cmds), time.Since(loadStart).Round(time.Second), perLoop)
	for _, loop := range []string{"npm ci", "dotnet restore", "npm cold"} {
		if perLoop[loop][0] < 3 {
			t.Errorf("the %s loop ran %d commands: the load did not run", loop, perLoop[loop][0])
		}
	}
	if l.coldDone.Load() {
		t.Errorf("the %d cold packages of the load ran out before it ended: the cold loop did not meet the last faults", coldN)
	}
	covered := map[int]bool{}
	for _, r := range recs {
		r.Cmds = overlapping(cmds, r.From, r.End)
		r.Link = linkIn(samples, r.From, r.End)
		t.Run(r.name, func(t *testing.T) {
			loops := map[string]int{}
			slowest := map[string]time.Duration{}
			for _, c := range r.Cmds {
				loops[c.Loop]++
				slowest[c.Loop] = max(slowest[c.Loop], c.End.Sub(c.Start).Round(100*time.Millisecond))
				if !c.OK {
					t.Errorf("%s (%s to %s) failed after the client's own retries:\n%s", c.Loop, c.Start.Format("15:04:05"),
						c.End.Format("15:04:05"), c.Log)
				}
			}
			for _, loop := range []string{"npm ci", "dotnet restore", "npm cold"} {
				if len(overlapping(filterLoop(cmds, loop), r.From, r.To.Add(time.Second))) == 0 {
					t.Errorf("the %s loop ran no command during the fault (%s to %s): the fault did not meet it", loop,
						r.From.Format("15:04:05"), r.To.Format("15:04:05"))
				}
			}
			t.Logf("commands that overlapped the fault and its settle time: %v, the slowest of each loop %v (a command that needed a retry takes seconds longer); link: %d samples, interface mismatch %v, replicas all unreachable for %s, any for %s",
				loops, slowest, r.Link.Samples, r.Link.MaxMismatch, r.Link.AllDown, r.Link.AnyDown)
			if r.Link.Samples == 0 {
				t.Errorf("no sample of the link gauges during the fault")
			}
			if r.Link.MaxMismatch != 0 {
				t.Errorf("trove_bastion_interface_mismatch rose to %v: the interface version of the two sides must stay compatible (R-BAS-028)", r.Link.MaxMismatch)
			}
			if r.Link.AllDown >= 2*time.Minute {
				t.Errorf("every replica reported the main installation unreachable for %s: TroveBastionMainUnreachable (for 2m) would fire on planned maintenance", r.Link.AllDown)
			}
			if r.check != nil {
				r.check(t, r)
			}
		})
		for i, c := range cmds {
			if c.Start.Before(r.End) && c.End.After(r.From) {
				covered[i] = true
			}
		}
	}
	for i, c := range cmds {
		if !c.OK && !covered[i] {
			t.Errorf("%s (%s to %s) failed outside every fault:\n%s", c.Loop, c.Start.Format("15:04:05"), c.End.Format("15:04:05"), c.Log)
		}
	}
}

// filterLoop keeps the commands of one loop.
func filterLoop(cmds []basLoadCmd, loop string) []basLoadCmd {
	var out []basLoadCmd
	for _, c := range cmds {
		if c.Loop == loop {
			out = append(out, c)
		}
	}
	return out
}

// warmFills are the requests for an artifact that the Bastions sent to the main installation and the main installation answered
// with the artifact (200), not counting the cold packages of the load: what a Bastion fetches again because it lost its content.
func warmFills(rs []mainReq) []mainReq {
	var out []mainReq
	for _, r := range rs {
		if r.kind() == "artifact" && r.Status == 200 && !strings.Contains(r.Path, basColdMarker) {
			out = append(out, r)
		}
	}
	return out
}

func (f *basFaultRecord) String() string {
	return fmt.Sprintf("%s [%s - %s]", f.name, f.From.Format("15:04:05"), f.To.Format("15:04:05"))
}
