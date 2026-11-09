//go:build integration

package deblocal

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/formats/formattest/crafted"
	"github.com/guided-traffic/trove/test/debstub/debgen"
)

type arMember struct {
	name string
	data []byte
}

// arArchive writes an ar archive with the members in order.
func arArchive(members ...arMember) []byte {
	var b bytes.Buffer
	b.WriteString("!<arch>\n")
	for _, m := range members {
		fmt.Fprintf(&b, "%-16s%-12d%-6d%-6d%-8s%-10d`\n", m.name+"/", 0, 0, 0, "100644", len(m.data))
		b.Write(m.data)
		if len(m.data)%2 == 1 {
			b.WriteByte('\n')
		}
	}
	return b.Bytes()
}

// bombControl writes the gzip stream of a tar archive whose control file declares and holds size bytes of zeros to a file, so that the
// size of the stream is known for the header of the ar member.
func bombControl(t *testing.T, size int64) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "control.tar.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	rc := crafted.TarGzEntry("control", size).Open()
	if _, err := io.Copy(f, rc); err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func tarGz(t *testing.T, name, content string) []byte {
	t.Helper()
	b, err := debgen.TarGz(map[string][]byte{name: []byte(content)}, []string{name})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// heapPeak samples the heap while fn runs and returns the highest value above the baseline taken before.
func heapPeak(fn func()) uint64 {
	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)
	var peak atomic.Uint64
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		var m runtime.MemStats
		for {
			runtime.ReadMemStats(&m)
			if m.HeapAlloc > peak.Load() {
				peak.Store(m.HeapAlloc)
			}
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	}()
	fn()
	close(stop)
	<-done
	if peak.Load() < base.HeapAlloc {
		return 0
	}
	return peak.Load() - base.HeapAlloc
}

// R-DEB-034: the crafted inputs of the conformance kit through the upload route: a control archive that expands to 10 GiB, a control file
// with 100,000 fields and an ar archive with a duplicate control.tar.gz are each refused within 5 s with 413 or 422, and the replica's
// memory stays within 512 MiB of its baseline (R-ARCH-028). The replicas run in this process, so its heap is theirs.
func TestR_DEB_034_CraftedPackagesAreRefusedQuicklyAndWithinTheMemoryBound(t *testing.T) {
	e := newEnv(t)
	debianBinary := arMember{"debian-binary", []byte("2.0\n")}
	data := arMember{"data.tar.gz", tarGz(t, "./usr/bin/x", "x")}
	ctl := "Package: crafted\nVersion: 1.0-1\nArchitecture: amd64\nMaintainer: A <a@example.org>\nDescription: crafted\n"

	var many strings.Builder
	many.WriteString(ctl)
	for i := 0; i < 100_000; i++ {
		fmt.Fprintf(&many, "X-Field-%d: v\n", i)
	}
	good := arMember{"control.tar.gz", tarGz(t, "control", ctl)}

	bombStart := time.Now()
	bomb := bombControl(t, 10<<30)
	t.Logf("the 10 GiB control archive is %d bytes (made in %s)", len(bomb), time.Since(bombStart).Round(time.Second))

	for _, tc := range []struct {
		name string
		body []byte
	}{
		{"a control archive that expands to 10 GiB", arArchive(debianBinary, arMember{"control.tar.gz", bomb}, data)},
		{"a control file with 100,000 fields", arArchive(debianBinary, arMember{"control.tar.gz", tarGz(t, "control", many.String())}, data)},
		{"an ar archive with a duplicate control.tar.gz", arArchive(debianBinary, good, good, data)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var status int
			var body []byte
			var took time.Duration
			peak := heapPeak(func() {
				start := time.Now()
				a := e.upload(e.tokA, "crafted_1.0-1_amd64.deb", tc.body, []string{"stable"})
				took, status, body = time.Since(start), a.Status, a.Body
			})
			if status != 413 && status != 422 {
				t.Fatalf("= %d %.300s, want 413 or 422", status, body)
			}
			if took > 5*time.Second {
				t.Errorf("refused after %s, want within 5 s", took)
			}
			if peak > 512<<20 {
				t.Errorf("the heap grew by %d MiB, want at most 512", peak>>20)
			}
			t.Logf("%d after %s, heap +%d MiB", status, took.Round(time.Millisecond), peak>>20)
		})
	}
	if n := e.st.Number(`SELECT count(*) FROM deb_binaries`); n != 0 {
		t.Errorf("a crafted package left %d rows", n)
	}
}
