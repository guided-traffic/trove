package mainclient_test

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastion/mainclient"
	"github.com/guided-traffic/trove/internal/bastion/maintest"
)

type logSink struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *logSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *logSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// mountedSecret writes a token file the way the kubelet projects a Secret: token is a link to cur/token, cur a
// link to a versioned directory, and a rotation swaps cur atomically.
type mountedSecret struct {
	t   *testing.T
	dir string
	n   int
}

func newMountedSecret(t *testing.T, token string) *mountedSecret {
	t.Helper()
	m := &mountedSecret{t: t, dir: t.TempDir()}
	m.set(token)
	if err := os.Symlink(filepath.Join("cur", "token"), filepath.Join(m.dir, "token")); err != nil {
		t.Fatal(err)
	}
	return m
}

func (m *mountedSecret) path() string { return filepath.Join(m.dir, "token") }

// set writes a new version of the file and swaps the link to it.
func (m *mountedSecret) set(content string) {
	m.t.Helper()
	m.n++
	v := filepath.Join(m.dir, "v"+string(rune('0'+m.n)))
	if err := os.Mkdir(v, 0o700); err != nil {
		m.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(v, "token"), []byte(content), 0o600); err != nil {
		m.t.Fatal(err)
	}
	tmp := filepath.Join(m.dir, "cur.tmp")
	_ = os.Remove(tmp)
	if err := os.Symlink(filepath.Base(v), tmp); err != nil {
		m.t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(m.dir, "cur")); err != nil {
		m.t.Fatal(err)
	}
}

func eventually(t *testing.T, within time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("not within %v: %s", within, what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func startReloader(t *testing.T, r *rig, path string, interval time.Duration, onChange func(mainclient.Token)) (*logSink, context.CancelFunc) {
	t.Helper()
	logs := &logSink{}
	rl := mainclient.NewReloader(mainclient.ReloaderOptions{
		Path: path, Interval: interval, WatchEvery: 10 * time.Millisecond, Holder: r.holder, Metrics: r.m,
		Log: slog.New(slog.NewJSONHandler(logs, nil)), OnChange: onChange,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { rl.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return logs, cancel
}

// TestR_BAS_006_ARotatedTokenIsUsedFromTheNextRequestAndTheOldOneMayBeRevoked: the Secret's file is swapped; the
// reloader notices it within the watch cadence, not the reload interval; the next request carries the new token
// and revoking the old one afterwards fails no request of a concurrent loop.
func TestR_BAS_006_ARotatedTokenIsUsedFromTheNextRequestAndTheOldOneMayBeRevoked(t *testing.T) {
	r := newRig(t, nil)
	secret := newMountedSecret(t, r.token)
	var changed atomic.Value
	startReloader(t, r, secret.path(), time.Hour, func(tok mainclient.Token) { changed.Store(tok.Hint) })

	// A loop of requests, one at a time, as a replica under load makes them.
	var failures atomic.Int64
	var calls atomic.Int64
	stop := make(chan struct{})
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		for {
			select {
			case <-stop:
				return
			default:
			}
			calls.Add(1)
			if _, err := r.c.Handshake(context.Background()); err != nil {
				failures.Add(1)
			}
		}
	}()

	next := r.main.NewToken()
	secret.set(next + "\n")
	eventually(t, 5*time.Second, "the new token in use", func() bool { return r.holder.Token().Value == next })
	eventually(t, 5*time.Second, "a request with the new token", func() bool {
		h := r.main.Headers("handshake")
		return h != nil && h.Get("Authorization") == "Bearer "+next
	})
	if changed.Load() != next[:12] {
		t.Errorf("OnChange got %v", changed.Load())
	}
	if n := maintest.Value(r.m.TokenReloads.WithLabelValues("loaded")); n != 1 {
		t.Errorf("reloads loaded = %v", n)
	}
	// Every earlier request was sent and answered before one with the new token was seen, so the old token
	// can go now.
	r.main.Revoke(r.token)
	before := calls.Load()
	eventually(t, 5*time.Second, "requests after the revocation", func() bool { return calls.Load() > before+20 })
	close(stop)
	<-loopDone
	if failures.Load() != 0 {
		t.Errorf("%d of %d requests failed across the rotation", failures.Load(), calls.Load())
	}
}

// TestR_BAS_006_AMalformedTokenFileKeepsTheOldTokenAndIsLoggedWithoutItsContent: an unreadable or malformed file
// (wrong prefix, failed checksum, empty while the Secret is being replaced) leaves the token in use, counts a
// refusal, logs an error that names the file and never the content; the next good file is used.
func TestR_BAS_006_AMalformedTokenFileKeepsTheOldTokenAndIsLoggedWithoutItsContent(t *testing.T) {
	r := newRig(t, nil)
	secret := newMountedSecret(t, r.token)
	logs, _ := startReloader(t, r, secret.path(), time.Hour, nil)
	junk := "trv_bst_" + strings.Repeat("Z", 43) + "AAAAAA"
	for i, bad := range []string{junk, "", "trv_pat_" + r.token[8:], r.token[:30]} {
		secret.set(bad)
		want := float64(i + 1)
		eventually(t, 5*time.Second, "the refusal is counted", func() bool { return maintest.Value(r.m.TokenReloads.WithLabelValues("refused")) >= want })
		if r.holder.Token().Value != r.token {
			t.Fatalf("file %d replaced the token in use", i)
		}
	}
	out := logs.String()
	if !strings.Contains(out, `"level":"ERROR"`) || !strings.Contains(out, secret.path()) {
		t.Errorf("the error log does not name the file: %s", out)
	}
	for _, secretText := range []string{junk, r.token} {
		if strings.Contains(out, secretText) {
			t.Errorf("the log shows token content")
		}
	}
	// Requests continue with the old token.
	if _, err := r.c.Handshake(context.Background()); err != nil {
		t.Errorf("after the refused files: %v", err)
	}
	good := r.main.NewToken()
	secret.set(good)
	eventually(t, 5*time.Second, "the good token", func() bool { return r.holder.Token().Value == good })
	// A file that disappears is refused too.
	if err := os.Remove(filepath.Join(secret.dir, "token")); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "the vanished file is refused", func() bool { return maintest.Value(r.m.TokenReloads.WithLabelValues("refused")) >= 5 })
	if r.holder.Token().Value != good {
		t.Error("a vanished file replaced the token in use")
	}
}

// TestR_BAS_006_TheReloadIntervalCatchesAChangeTheFileIdentityDoesNot: a file rewritten in place with the same
// size and modification time looks unchanged to the identity check; the full read every token_reload_interval
// finds it.
func TestR_BAS_006_TheReloadIntervalCatchesAChangeTheFileIdentityDoesNot(t *testing.T) {
	r := newRig(t, nil)
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(r.token), 0o600); err != nil {
		t.Fatal(err)
	}
	old, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	startReloader(t, r, path, 60*time.Millisecond, nil)
	next := r.main.NewToken()
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(next); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if err := os.Chtimes(path, old.ModTime(), old.ModTime()); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "the full read", func() bool { return r.holder.Token().Value == next })
}

// TestR_BAS_006_TheReloaderRefusesWhatTheClientWouldSend: the file's token is the one the client sends, and an
// unchanged file changes nothing and counts nothing.
func TestR_BAS_006_TheReloaderRefusesWhatTheClientWouldSend(t *testing.T) {
	r := newRig(t, nil)
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(r.token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rl := mainclient.NewReloader(mainclient.ReloaderOptions{Path: path, Interval: time.Hour, Holder: r.holder, Metrics: r.m})
	for i := 0; i < 3; i++ {
		if changed, err := rl.Reload(); changed || err != nil {
			t.Fatalf("reload %d = %v, %v", i, changed, err)
		}
	}
	if n := maintest.Value(r.m.TokenReloads.WithLabelValues("loaded")) + maintest.Value(r.m.TokenReloads.WithLabelValues("refused")); n != 0 {
		t.Errorf("an unchanged file was counted: %v", n)
	}
	if _, err := r.c.Check(context.Background(), mainclient.Authenticated, iface.CheckRequest{Repositories: []string{"a"}}); err != nil {
		t.Error(err)
	}
}
