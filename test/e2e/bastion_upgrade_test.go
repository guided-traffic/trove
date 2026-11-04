//go:build e2e

package e2e

// The interface mismatch (R-BAS-028, R-BAS-049): the main installation is upgraded to a build with InterfaceVersion + 1.
// There is no build seam for the version: it is a constant. The build here is the release binary compiled with go build
// -overlay, which replaces internal/bastion/iface/version.go by a copy that says 2 (nothing in the tree changes), put into
// the release image as the tag guidedtraffic/trove:iface (Q-BAS-111).

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const basIfaceTag = "iface"

// buildIfaceImage builds guidedtraffic/trove:iface from the release image with the binary of the overlay and loads it into the
// kind cluster.
func buildIfaceImage(t testing.TB) {
	t.Helper()
	src, err := os.ReadFile(filepath.Join(cfg.repo, "internal/bastion/iface/version.go"))
	if err != nil {
		t.Fatal(err)
	}
	const from = "const InterfaceVersion = 1"
	if !strings.Contains(string(src), from) {
		t.Fatalf("internal/bastion/iface/version.go no longer holds %q: adapt the overlay of the mismatch scenario", from)
	}
	tmp := t.TempDir()
	version := filepath.Join(tmp, "version.go")
	if err := os.WriteFile(version, []byte(strings.Replace(string(src), from, "const InterfaceVersion = 2", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	// the embedded OpenAPI document names the interface version of the operations and the main installation refuses to start when it
	// differs from the constant (internal/api/v1/document.go), so the copy that is embedded says 2 as well
	doc, err := os.ReadFile(filepath.Join(cfg.repo, "internal/api/v1/openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	docPath := filepath.Join(tmp, "openapi.yaml")
	if err := os.WriteFile(docPath, []byte(strings.ReplaceAll(string(doc), "x-trove-bastion-interface: 1", "x-trove-bastion-interface: 2")), 0o644); err != nil {
		t.Fatal(err)
	}
	overlay, _ := json.Marshal(map[string]any{"Replace": map[string]string{
		filepath.Join(cfg.repo, "internal/bastion/iface/version.go"): version,
		filepath.Join(cfg.repo, "internal/api/v1/openapi.yaml"):      docPath,
	}})
	if err := os.WriteFile(filepath.Join(tmp, "overlay.json"), overlay, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := filepath.Join(tmp, "ctx")
	if err := os.MkdirAll(ctx, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(env []string, name string, args ...string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = cfg.repo
		cmd.Env = append(os.Environ(), env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, tail(string(out), 1500))
		}
	}
	run([]string{"CGO_ENABLED=0", "GOOS=linux", "GOARCH=" + runtime.GOARCH}, "go", "build", "-overlay", filepath.Join(tmp, "overlay.json"),
		"-trimpath", "-buildvcs=false", "-o", filepath.Join(ctx, "trove"), "./cmd/trove")
	if err := os.WriteFile(filepath.Join(ctx, "Dockerfile"), []byte("FROM guidedtraffic/trove:dev\nCOPY trove /usr/local/bin/trove\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(nil, "docker", "build", "-t", "guidedtraffic/trove:"+basIfaceTag, ctx)
	run(nil, "kind", "load", "docker-image", "guidedtraffic/trove:"+basIfaceTag, "--name", strings.TrimPrefix(cfg.kubeContext, "kind-"))
}

// upMainTry runs up.sh in the bastion profile with extra helm arguments for the main release; it returns when the rollout is done.
func upMainTry(extra string) error {
	cmd := exec.Command(filepath.Join(os.Getenv("TROVE_E2E_DEV_DIR"), "up.sh"))
	cmd.Dir = cfg.repo
	cmd.Env = append(os.Environ(), "TROVE_PROFILE=bastion", "TROVE_HELM_EXTRA="+extra)
	out, err := cmd.CombinedOutput()
	_ = os.WriteFile(filepath.Join(cfg.out, "profile-bastion-upgrade.log"), out, 0o644)
	if err != nil {
		return fmt.Errorf("up.sh: %w\n%s", err, tail(string(out), 2000))
	}
	return nil
}

// upMain is upMainTry for a test, followed by the wait until the api and packages pods have settled.
func upMain(t testing.TB, extra string) {
	t.Helper()
	if err := upMainTry(extra); err != nil {
		t.Fatal(err)
	}
	waitServingSettled(t)
}

// basMismatch reads the mismatch gauge and the version the main installation reported, per replica.
func basMismatch(t testing.TB, s *basSite) (mismatch []float64, mainVersion []float64) {
	t.Helper()
	for _, p := range basReadyPods(t, s) {
		m := basMetrics(t, p.Name)
		mismatch = append(mismatch, m["trove_bastion_interface_mismatch"])
		mainVersion = append(mainVersion, m[`trove_bastion_interface_version{side="main"}`])
	}
	return
}

// TestBastion_R_BAS_049_InterfaceMismatch: the main installation at InterfaceVersion + 1 makes both replicas report the
// mismatch (the gauge TroveBastionInterfaceMismatch reads stays 1 for its `for: 1m`), the Bastion serves warm content for the
// grace period, and when the Bastion is upgraded as well the mismatch clears and cold requests succeed.
func TestBastion_R_BAS_049_InterfaceMismatch(t *testing.T) {
	env := basFx(t)
	site := env.bkt
	buildIfaceImage(t)
	cold := "bas" + env.run + "-cold-mismatch"
	env.publishNPM(t, basNPMRepo, cold, "1.0.0", nil)
	setN := env.npmSets["pnpm"]
	c := env.npmClient(t, "pnpm", 0, "mismatch", env.full.Token)
	dir := env.basNPMProject(t, c, setN, "mismatch", site)

	t.Cleanup(func() {
		upMain(t, "")
		if out, err := basScriptTry("install", site.variant); err != nil {
			t.Errorf("restoring the Bastion: %v\n%s", err, tail(out, 1000))
		}
		waitFor(t, "the mismatch to be gone after the restore", 3*time.Minute, 2*time.Second, func() bool { m, _ := basMismatch(t, site); return allEqual(m, 0) })
	})

	c.basInstall(t, dir, setN)
	// The rollout of the main installation takes longer than the grace period, and an answer is renewed by use: a client keeps
	// installing until the mismatch is seen, so that the answers the grace period extends are those of the last moments before it.
	stopLoop, loopDone := make(chan struct{}), make(chan struct{})
	// every install of the loop is recorded, so that a refusal of the grace period's install can be told from answers that were
	// never warm: the loop's own results say whether the cache was filled by the moment the mismatch began
	type loopInstall struct {
		start, end time.Time
		code       int
	}
	var installs []loopInstall
	go func() {
		defer close(loopDone)
		for {
			select {
			case <-stopLoop:
				return
			default:
			}
			in := loopInstall{start: time.Now()}
			// The store below $HOME/.local too: a client that has the versions in its store installs them without a
			// request, and then nothing keeps the Bastion's answers fresh (the loop's installs took 0 s in run
			// 41714935482, and the grace-period install met expired answers).
			c.wipeAll(dir)
			in.code = c.run(dir, c.frozenInstall()...).Code
			in.end = time.Now()
			installs = append(installs, in)
			time.Sleep(10 * time.Second)
		}
	}()
	rollout := make(chan error, 1)
	go func() { rollout <- upMainTry("--set image.tag=" + basIfaceTag) }()
	// the moment both replicas report the mismatch, the answers the loop obtained a moment ago are the ones the grace period extends
	waitFor(t, "both replicas to report the interface mismatch", 6*time.Minute, time.Second, func() bool {
		m, v := basMismatch(t, site)
		return allEqual(m, 1) && allEqual(v, 2)
	})
	mismatchAt := time.Now()
	close(stopLoop)
	<-loopDone
	seen := time.Now()
	var history strings.Builder
	lastOK := time.Time{}
	for _, in := range installs {
		fmt.Fprintf(&history, "  install %s to %s (%s): exit %d\n", in.start.Format("15:04:05"), in.end.Format("15:04:05"), in.end.Sub(in.start).Round(time.Second), in.code)
		if in.code == 0 {
			lastOK = in.end
		}
	}
	t.Logf("installs of the loop (the mismatch was seen at %s):\n%s", mismatchAt.Format("15:04:05"), history.String())
	if !t.Run("the Bastion serves warm content during the grace period", func(t *testing.T) {
		c.basInstall(t, dir, setN)
	}) {
		basDumpLogs(t, "grace-period")
		if lastOK.IsZero() {
			t.Errorf("no install of the loop succeeded, the cache was never warm through this client:\n%s", history.String())
		} else {
			t.Errorf("the last successful install of the loop ended %s before both replicas reported the mismatch and %s before the grace-period install began (answer period %s, grace period %s):\n%s",
				mismatchAt.Sub(lastOK).Round(time.Second), seen.Sub(lastOK).Round(time.Second), basAnswerPeriod, basGrace, history.String())
		}
	}
	if err := <-rollout; err != nil {
		t.Fatal(err)
	}
	waitServingSettled(t)
	waitFor(t, "the mismatch to be steady on both replicas", 2*time.Minute, time.Second, func() bool {
		m, v := basMismatch(t, site)
		return allEqual(m, 1) && allEqual(v, 2)
	})
	seen = time.Now()
	t.Run("the gauge of TroveBastionInterfaceMismatch stays 1 for its `for` of one minute", func(t *testing.T) {
		for time.Since(seen) < 65*time.Second {
			m, v := basMismatch(t, site)
			if !allEqual(m, 1) || !allEqual(v, 2) {
				t.Fatalf("trove_bastion_interface_mismatch %v, main version %v after %s", m, v, time.Since(seen).Round(time.Second))
			}
			for _, p := range basReadyPods(t, site) {
				if basMetrics(t, p.Name)[`trove_bastion_interface_version{side="bastion"}`] != 1 {
					t.Fatalf("%s: the Bastion's own version is not 1", p.Name)
				}
			}
			time.Sleep(5 * time.Second)
		}
	})
	t.Run("a cold request is refused while the versions differ", func(t *testing.T) {
		if r := site.get(t, env.full.Token, npmDocPath(cold)); r.Status != 503 {
			t.Errorf("a cold package at a mismatching main installation: %s, want 503", r)
		}
	})

	// the Bastion is upgraded as well
	out, err := basScriptTryEnv([]string{"TROVE_BASTION_HELM_EXTRA=--set image.tag=" + basIfaceTag}, "install", site.variant)
	if err != nil {
		t.Fatalf("upgrading the Bastion: %v\n%s", err, tail(out, 1500))
	}
	waitFor(t, "the mismatch to clear after the Bastion's upgrade", 2*time.Minute, time.Second, func() bool {
		m, v := basMismatch(t, site)
		return len(m) == 2 && allEqual(m, 0) && allEqual(v, 2) && allEqual(basReachable(t, site), 1)
	})
	site.get(t, env.full.Token, npmDocPath(cold)).want(t, "a cold package after the Bastion's upgrade", 200)
	fmt.Fprintln(os.Stderr, "e2e: interface mismatch cleared after the Bastion's upgrade")
}

// basScriptTryEnv is basScriptTry with more environment.
func basScriptTryEnv(env []string, args ...string) (string, error) {
	cmd := exec.Command(filepath.Join(cfg.repo, "deploy/dev/bastion.sh"), args...)
	cmd.Dir = cfg.repo
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	_ = os.WriteFile(filepath.Join(cfg.out, "bastion-"+strings.Join(args, "-")+"-env.log"), out, 0o644)
	return string(out), err
}
