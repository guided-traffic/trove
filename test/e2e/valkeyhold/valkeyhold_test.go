package valkeyhold

import (
	"errors"
	"testing"
	"time"
)

func TestBenignPatchError(t *testing.T) {
	for _, c := range []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New(`Error from server (NotFound): pods "x" not found`), true},
		{errors.New(`pods "x" not found`), true},
		{errors.New("Precondition failed: UID in precondition: a, UID in object meta: b"), true},
		// "guided-traffic" contains "uid"; only the answers above are final
		{errors.New("the finalizer e2e.trove.guided-traffic.com/hold-valkey-primary could not be patched"), false},
		{errors.New("Unable to connect to the server: context deadline exceeded"), false},
	} {
		if got := BenignPatchError(c.err); got != c.want {
			t.Errorf("BenignPatchError(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

func TestParseFlags(t *testing.T) {
	for _, c := range []struct {
		name, out     string
		inProgress, f bool
	}{
		{"running", "name\ntrove-valkey\nip\nx\nflags\nmaster\nnum-slaves\n1\n", false, true},
		{"down", "flags\ns_down,o_down,master\n", false, true},
		{"failing over", "name\ntrove-valkey\nflags\no_down,failover_in_progress,master\nnum-slaves\n1\n", true, true},
		{"padded", "  flags  \n  master,failover_in_progress \n", true, true},
		{"flags last line", "name\nx\nflags", false, false},
		{"flags value empty", "flags\n\nnum-slaves\n1\n", false, false},
		{"no flags key", "name\ntrove-valkey\nnum-slaves\n1\n", false, false},
		{"error", "ERR No such master with that name\n", false, false},
		{"empty", "", false, false},
	} {
		in, found := ParseFlags(c.out)
		if in != c.inProgress || found != c.f {
			t.Errorf("%s: ParseFlags = (%v, %v), want (%v, %v)", c.name, in, found, c.inProgress, c.f)
		}
	}
}

type rig struct {
	now        time.Time
	inProgress bool
	readable   bool
	answers    []string // the answers of Send in order; "OK" is accepted
	sent       int
	logs       []string
	a          *Assister
}

func newRig() *rig {
	r := &rig{now: time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC), readable: true}
	r.a = &Assister{
		Now:        func() time.Time { return r.now },
		InProgress: func() (bool, bool) { return r.inProgress, r.readable },
		Send: func() (string, bool) {
			ans := r.answers[r.sent]
			r.sent++
			return ans, ans == "OK"
		},
		Log: func(s string) { r.logs = append(r.logs, s) },
	}
	return r
}

// at steps the assister at evicted+d.
func (r *rig) at(evicted time.Time, d time.Duration) {
	r.now = evicted.Add(d)
	r.a.Step(evicted)
}

func TestAssisterNothingBeforeTheDelay(t *testing.T) {
	r := newRig()
	r.answers = []string{"OK"}
	ev := r.now
	for d := time.Duration(0); d < AssistAfter; d += 500 * time.Millisecond {
		r.at(ev, d)
	}
	if r.sent != 0 || r.a.Attempts != 0 {
		t.Fatalf("sent %d, attempts %d before %s", r.sent, r.a.Attempts, AssistAfter)
	}
	r.at(ev, AssistAfter)
	if r.sent != 1 || len(r.a.Accepted) != 1 {
		t.Fatalf("sent %d, accepted %d at %s", r.sent, len(r.a.Accepted), AssistAfter)
	}
}

func TestAssisterRefusalsAreSpaced(t *testing.T) {
	r := newRig()
	r.answers = []string{"-NOGOODSLAVE", "-NOGOODSLAVE", "OK"}
	ev := r.now
	// a tick every 500 ms from 25 s to 35 s: attempts at 25, 28 and 31 s only, the third answers OK
	for d := AssistAfter; d <= 35*time.Second; d += 500 * time.Millisecond {
		r.at(ev, d)
	}
	if r.sent != 3 {
		t.Fatalf("sent %d commands, want 3 (25 s refused, 28 s refused, 31 s accepted); logs %v", r.sent, r.logs)
	}
	if len(r.a.Accepted) != 1 || !r.a.Accepted[0].Equal(ev.Add(31*time.Second)) {
		t.Fatalf("accepted %v, want one at 31 s", r.a.Accepted)
	}
	if len(r.logs) != 3 {
		t.Fatalf("%d log lines, want one per attempt: %v", len(r.logs), r.logs)
	}
}

func TestAssisterSecondAfterTheElectionTimeoutThenStop(t *testing.T) {
	r := newRig()
	r.answers = []string{"OK", "OK", "OK"}
	ev := r.now
	r.at(ev, 25*time.Second)
	for d := 25*time.Second + 500*time.Millisecond; d < 35*time.Second; d += 500 * time.Millisecond {
		r.at(ev, d)
	}
	if r.sent != 1 {
		t.Fatalf("sent %d within %s of the first accepted command, want 1", r.sent, AssistRetry)
	}
	r.at(ev, 35*time.Second)
	if r.sent != 2 || len(r.a.Accepted) != 2 {
		t.Fatalf("sent %d, accepted %d at 35 s, want 2 and 2", r.sent, len(r.a.Accepted))
	}
	for d := 36 * time.Second; d < 120*time.Second; d += 500 * time.Millisecond {
		r.at(ev, d)
	}
	if r.sent != 2 {
		t.Fatalf("sent %d after the second accepted command, want 2 in all", r.sent)
	}
}

func TestAssisterInProgressBlocksAndIsSpaced(t *testing.T) {
	r := newRig()
	r.answers = []string{"OK"}
	r.inProgress = true
	ev := r.now
	for d := AssistAfter; d < 34*time.Second; d += 500 * time.Millisecond {
		r.at(ev, d)
	}
	if r.sent != 0 {
		t.Fatalf("sent %d while a failover is in progress", r.sent)
	}
	if r.a.Attempts != 3 { // 25, 28, 31 s
		t.Fatalf("%d attempts in 9 s, want 3 (spaced by %s)", r.a.Attempts, AssistEvery)
	}
	r.inProgress = false
	r.at(ev, 34*time.Second)
	if r.sent != 1 {
		t.Fatalf("sent %d after the failover ended, want 1", r.sent)
	}
}

func TestAssisterUnreadableFlagsSendNothing(t *testing.T) {
	r := newRig()
	r.answers = []string{"OK"}
	r.readable = false
	ev := r.now
	r.at(ev, AssistAfter)
	r.at(ev, AssistAfter+AssistEvery)
	if r.sent != 0 || len(r.logs) != 2 {
		t.Fatalf("sent %d, logs %v: want no command and one log line per attempt", r.sent, r.logs)
	}
}
