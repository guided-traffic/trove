//go:build e2e

package e2e

// TestUpgrade_R_HA_020_AcrossTwoBundles is the rolling upgrade between two releases with different UI bundles (R-HA-020,
// Q-HA-120, Q-HA-124, finding F-58): the working tree is installed with the image of `make image-marked`, whose UI has the
// same code under other hashed names (UI_BUNDLE_MARK), and upgraded to the image under test, both with the shared asset
// store. Every loop is strict: a journey that meets a 404 for a hashed asset fails the test as any other failure does
// (requireClean with no release named). A sampler of its own reads the index.html through the edge while the upgrade runs
// and asks the edge for the main script it names, which lands on a replica of either release; the two bundles must both be
// seen in the window and every such script must load.

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// upMarkedImage is the image of the working tree with the second UI bundle, built by `make image-marked`.
func upMarkedImage() string {
	if v := os.Getenv("TROVE_IMAGE_MARKED"); v != "" {
		return v
	}
	return "guidedtraffic/trove:dev-marked"
}

// imageTag is the tag of an image reference of the repository the dev values name (guidedtraffic/trove).
func imageTag(t testing.TB, image string) string {
	t.Helper()
	repo, tag, ok := strings.Cut(image, ":")
	if !ok || strings.TrimPrefix(repo, "docker.io/") != "guidedtraffic/trove" {
		t.Fatalf("%s: the dev values install guidedtraffic/trove:<tag>; the marked image must be a tag of that repository", image)
	}
	return tag
}

// mainScript finds the main script an index.html names.
var mainScript = regexp.MustCompile(`src="(main-[A-Za-z0-9_-]{8}\.js)"`)

// bundleSampler reads /ui/en/ through the edge twice a second while an upgrade runs, notes the main script each index
// names, and asks the edge for that script three times; the edge spreads the four requests over the replicas of both
// releases.
type bundleSampler struct {
	stop    chan struct{}
	done    chan struct{}
	mu      sync.Mutex
	indexes map[string]int // main script -> index.html answers that named it
	scripts map[int]int    // status of the script requests
	failed  []string
}

func startBundleSampler() *bundleSampler {
	s := &bundleSampler{stop: make(chan struct{}), done: make(chan struct{}), indexes: map[string]int{}, scripts: map[int]int{}}
	go func() {
		defer close(s.done)
		for {
			s.sample()
			select {
			case <-s.stop:
				return
			case <-time.After(500 * time.Millisecond):
			}
		}
	}()
	return s
}

func (s *bundleSampler) sample() {
	code, hdr, body, err := edge("/ui/en/")
	if err != nil || code != 200 {
		s.mu.Lock()
		s.failed = append(s.failed, fmt.Sprintf("%s GET /ui/en/: %d %v (replica %s)", time.Now().Format("15:04:05.000"), code, err, replicaOf(hdr)))
		s.mu.Unlock()
		return
	}
	m := mainScript.FindStringSubmatch(body)
	if m == nil {
		s.mu.Lock()
		s.failed = append(s.failed, fmt.Sprintf("%s the index of %s names no main script", time.Now().Format("15:04:05.000"), replicaOf(hdr)))
		s.mu.Unlock()
		return
	}
	s.mu.Lock()
	s.indexes[m[1]]++
	s.mu.Unlock()
	for range 3 {
		code, h, _, err := edge("/ui/en/" + m[1])
		s.mu.Lock()
		s.scripts[code]++
		if err != nil || code != 200 {
			s.failed = append(s.failed, fmt.Sprintf("%s GET /ui/en/%s (named by the index of %s): %d %v (replica %s)",
				time.Now().Format("15:04:05.000"), m[1], replicaOf(hdr), code, err, replicaOf(h)))
		}
		s.mu.Unlock()
	}
}

// replicaOf is the replica that answered, from its X-Trove-Replica header; empty for an answer of no replica (the edge).
func replicaOf(h http.Header) string {
	if h == nil {
		return ""
	}
	return h.Get("X-Trove-Replica")
}

func (s *bundleSampler) finish() *bundleSampler {
	close(s.stop)
	<-s.done
	return s
}

func (s *bundleSampler) mains() []string {
	var out []string
	for k := range s.indexes {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// bundleAtTheEdge is the main script the index.html names on every replica that answers: it reads the index twelve times
// through the edge and fails when the answers name more than one.
func bundleAtTheEdge(t *testing.T) string {
	t.Helper()
	seen := map[string]int{}
	for range 12 {
		code, _, body, err := edge("/ui/en/")
		if err != nil || code != 200 {
			t.Fatalf("GET /ui/en/ through the edge: %d %v", code, err)
		}
		m := mainScript.FindStringSubmatch(body)
		if m == nil {
			t.Fatalf("the index names no main script:\n%s", tail(body, 600))
		}
		seen[m[1]]++
	}
	if len(seen) != 1 {
		t.Fatalf("the replicas serve more than one bundle outside an upgrade: %v", seen)
	}
	for k := range seen {
		return k
	}
	return ""
}

func TestUpgrade_R_HA_020_AcrossTwoBundles(t *testing.T) {
	requireUpgradeSetup(t)
	marked := upMarkedImage()
	if err := exec.Command("docker", "image", "inspect", marked).Run(); err != nil {
		t.Fatalf("the image %s is not in the local Docker: run `make image-marked` (the working tree with the second UI bundle)", marked)
	}
	tag := imageTag(t, marked)
	t.Cleanup(func() { restoreCurrentRelease(t) })
	upKindLoad(t)
	upKindLoadImage(t, marked)

	resetInstallation(t)
	args := []string{"install", "trove", upChart(), "-n", cfg.namespace}
	args = append(args, upReleaseValuesOf(t, "", marked)...)
	args = append(args, "--set", "image.tag="+tag, "--wait", "--timeout", "15m")
	if out, err := upHelm(t, "install-marked", 20*time.Minute, args...); err != nil {
		t.Fatalf("helm install of the working tree with %s: %v\n%s", marked, err, tail(out, 3000))
	}
	from := bundleAtTheEdge(t)
	t.Logf("installed the working tree with %s: the index names %s", marked, from)

	fx := buildUpgradeFixture(t)
	loops := startUpgradeLoops(t, fx, "bundles")
	time.Sleep(upgradeHold)

	sampler := startBundleSampler()
	start, end, out, err := upgradeToTree(t, "bundles")
	bs := sampler.finish()
	if err != nil {
		t.Fatalf("helm upgrade --wait to %s: %v\n%s", upImage(), err, tail(out, 3000))
	}
	t.Logf("upgrade took %s; the index named %v, the main scripts answered %v", end.Sub(start).Round(time.Second), bs.indexes, bs.scripts)

	t.Run("R-HA-020 the two bundles were served side by side and every main script an index named loaded through the edge", func(t *testing.T) {
		rolloutComplete(t, deploymentNames(upDeployments(t)))
		to := bundleAtTheEdge(t)
		if to == from {
			t.Fatalf("the image under test serves the bundle of %s (%s): the two images do not have different bundles", marked, from)
		}
		if got := bs.mains(); len(got) != 2 || !strings.Contains(strings.Join(got, " "), from) || !strings.Contains(strings.Join(got, " "), to) {
			t.Errorf("the index answers during the upgrade named %v, want both %s and %s: the upgrade did not run in a window with both bundles", got, from, to)
		}
		for _, f := range bs.failed {
			t.Errorf("%s", f)
		}
	})
	time.Sleep(upgradeHold)

	res := loops.stop(t)
	res.requireClean(t, "", upWindow{"the upgrade across two bundles", start, end})
	res.publishedAreReadable(t, fx)
}
