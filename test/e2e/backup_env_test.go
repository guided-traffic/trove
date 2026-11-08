//go:build e2e

package e2e

// The harness of the backup suite (R-BAK-029, docs/developer/e2e.md#the-backup-tests): the profile with garbage collection
// without a grace period, repositories of the three formats, a load that publishes, deletes and trashes while a backup runs,
// Jobs made from the backup CronJob, and the second installation (deploy/dev/restore-ns.sh) that a backup is restored into,
// reached through port-forwards.

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // dist.shasum is a SHA-1
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	bakNPM   = "bak-npm"
	bakNuGet = "bak-nuget"
	bakFiles = "bak-files"
)

// restoreNS is the namespace of the second installation.
var restoreNS = func() string {
	if v := os.Getenv("TROVE_E2E_RESTORE_NAMESPACE"); v != "" {
		return v
	}
	return "trove-restore"
}()

// ── the profile and the fixture ──────────────────────────────────────────────

var (
	bakProfileOnce sync.Once
	bakProfileErr  error
)

// useBackupProfile switches the cluster to the profile `backup` (garbage collection without a grace period) once per process;
// TestMain returns it to the default profile.
func useBackupProfile(t testing.TB) {
	t.Helper()
	bakProfileOnce.Do(func() { bakProfileErr = runUp("backup") })
	if bakProfileErr != nil {
		t.Fatal(bakProfileErr)
	}
}

var (
	bakFxOnce sync.Once
	bakFxAPI  *api
	bakFxErr  error
)

// bakAdmin creates the three repositories of the suite once and returns the administrator's client.
func bakAdmin(t testing.TB) *api {
	t.Helper()
	useBackupProfile(t)
	bakFxOnce.Do(func() {
		adm := adminAPI(t)
		for _, r := range []struct{ key, format string }{{bakNPM, "npm"}, {bakNuGet, "nuget"}, {bakFiles, "generic"}} {
			rep, err := adm.send(http.MethodPost, reposRoute, map[string]any{"key": r.key, "class": "local", "format": r.format})
			if err != nil || (rep.Status != 201 && rep.Status != 409) {
				bakFxErr = fmt.Errorf("create repository %s: %w %s", r.key, err, rep)
				return
			}
		}
		bakFxAPI = adm
	})
	if bakFxErr != nil {
		t.Fatal(bakFxErr)
	}
	return bakFxAPI
}

// ── publishing ───────────────────────────────────────────────────────────────

func bakNPMTarball(name, version string) []byte {
	raw, _ := json.Marshal(map[string]any{"name": name, "version": version})
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, f := range [][2]string{{"package.json", string(raw)}, {"README.md", "# " + name + " " + version + "\n"}} {
		_ = tw.WriteHeader(&tar.Header{Name: "package/" + f[0], Mode: 0o644, Size: int64(len(f[1])), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(f[1]))
	}
	_ = tw.Close()
	_ = zw.Close()
	return buf.Bytes()
}

// bakPublishNPM publishes name@version the way libnpmpublish does (one PUT of the document with the tarball attached).
func bakPublishNPM(a *api, name, version string) (reply, error) {
	tgz := bakNPMTarball(name, version)
	s1 := sha1.Sum(tgz) //nolint:gosec // see the import
	s5 := sha512.Sum512(tgz)
	file := name + "-" + version + ".tgz"
	manifest := map[string]any{
		"name": name, "version": version, "_id": name + "@" + version,
		"dist": map[string]any{"integrity": "sha512-" + base64.StdEncoding.EncodeToString(s5[:]), "shasum": hex.EncodeToString(s1[:]), "tarball": "http://x/" + name + "/-/" + file},
	}
	doc := map[string]any{
		"_id": name, "name": name, "dist-tags": map[string]string{"latest": version},
		"versions": map[string]any{version: manifest},
		"_attachments": map[string]any{name + "/-/" + file: map[string]any{
			"content_type": "application/octet-stream", "data": base64.StdEncoding.EncodeToString(tgz), "length": len(tgz),
		}},
	}
	return a.send(http.MethodPut, npmAddr(bakNPM)+"/"+name, doc)
}

func nupkgBytes(id, version, description string) []byte {
	nuspec := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://schemas.microsoft.com/packaging/2013/05/nuspec.xsd"><metadata><id>%s</id><version>%s</version><authors>Acme</authors><description>%s</description></metadata></package>`,
		id, version, description)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range [][2]string{{id + ".nuspec", nuspec}, {"lib/net8.0/" + id + ".dll", "MZ" + version + description}} {
		w, _ := zw.Create(f[0])
		_, _ = w.Write([]byte(f[1]))
	}
	_ = zw.Close()
	return buf.Bytes()
}

// bakPushNuGet pushes a package to the NuGet repository of the suite.
func bakPushNuGet(a *api, pkg []byte) (reply, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="package"; filename="package"`)
	h.Set("Content-Type", "application/octet-stream")
	part, _ := mw.CreatePart(h)
	_, _ = part.Write(pkg)
	_ = mw.Close()
	return a.send(http.MethodPut, nugetAddr(bakNuGet)+"/v3/package", buf.Bytes(), "Content-Type", mw.FormDataContentType())
}

func bakPutFile(a *api, path string, data []byte) (reply, error) {
	return a.send(http.MethodPut, filesAddr(bakFiles)+"/"+path, data, "Content-Type", "application/octet-stream")
}

// retrying repeats a request that the write class refused (R-API-016) a few times.
func retrying(f func() (reply, error)) (reply, error) {
	var r reply
	var err error
	for range 20 {
		r, err = f()
		if err != nil || r.Status != http.StatusTooManyRequests {
			return r, err
		}
		wait := 2 * time.Second
		if n, perr := strconv.Atoi(r.Header.Get("Retry-After")); perr == nil && n > 0 && n < 30 {
			wait = time.Duration(n) * time.Second
		}
		time.Sleep(wait)
	}
	return r, err
}

// ── SQL on the main installation, from a goroutine ───────────────────────────

// bakSQL runs a statement on the primary and returns the error instead of failing a test, for the load's goroutine.
func bakSQL(ctx context.Context, query string) (string, error) {
	prim, err := kubectlCtx(ctx, "-n", cfg.namespace, "get", "cluster", "trove-db", "-o", "jsonpath={.status.currentPrimary}")
	if err != nil {
		return "", err
	}
	return kubectlCtx(ctx, "-n", cfg.namespace, "exec", strings.TrimSpace(prim), "-c", "postgres", "--", "psql", "-U", "postgres", "-d", "trove", "-At", "-F", "|", "-c", "SET search_path TO trove; "+query)
}

// krSQL runs a statement on the database of the restored installation.
func krSQL(t testing.TB, query string) string {
	t.Helper()
	out, err := kubectl("-n", restoreNS, "exec", "trove-db-1", "-c", "postgres", "--", "psql", "-U", "postgres", "-d", "trove", "-At", "-F", "|", "-c", query)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(out)
}

func kr(t testing.TB, args ...string) string {
	t.Helper()
	out, err := kubectl(append([]string{"-n", restoreNS}, args...)...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// ── the load ─────────────────────────────────────────────────────────────────

// bakStats is what a load did.
type bakStats struct {
	NPM, NuGet, Files, Deleted, Trashed, Emptied, Collections, Throttled int
	Errors                                                               []string
}

// bakLoad publishes npm packages and NuGet packages and puts files, deletes and trashes some, empties the trash and runs the
// garbage collection of grace period 0s every 10 seconds, until it is stopped (R-BAK-029 scenario 1).
type bakLoad struct {
	cancel context.CancelFunc
	wg     sync.WaitGroup
	mu     sync.Mutex
	st     bakStats
}

func (l *bakLoad) snapshot() bakStats {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.st
	s.Errors = append([]string(nil), l.st.Errors...)
	return s
}

func (l *bakLoad) note(f func(*bakStats)) { l.mu.Lock(); f(&l.st); l.mu.Unlock() }

func (l *bakLoad) fail(format string, args ...any) {
	l.note(func(s *bakStats) {
		if len(s.Errors) < 20 {
			s.Errors = append(s.Errors, fmt.Sprintf(format, args...))
		}
	})
}

// startBakLoad starts the load with the administrator's client. The pace is one round every 700 ms.
func startBakLoad(a *api, run string) *bakLoad {
	ctx, cancel := context.WithCancel(context.Background())
	l := &bakLoad{cancel: cancel}
	l.wg.Add(2)
	go func() {
		defer l.wg.Done()
		var files []string
		for i := 0; ctx.Err() == nil; i++ {
			time.Sleep(700 * time.Millisecond)
			if r, err := retrying(func() (reply, error) {
				return bakPublishNPM(a, fmt.Sprintf("bak-load-%s-%d", run, i%3), fmt.Sprintf("1.%d.0", i))
			}); err != nil || r.Status != 201 {
				l.fail("npm publish %d: %v %s", i, err, r)
			} else {
				l.note(func(s *bakStats) { s.NPM++ })
			}
			if r, err := retrying(func() (reply, error) {
				return bakPushNuGet(a, nupkgBytes(fmt.Sprintf("Bak.Load%s%d", strings.ToUpper(run), i%2), fmt.Sprintf("1.%d.0", i), "load"))
			}); err != nil || r.Status != 201 {
				l.fail("nuget push %d: %v %s", i, err, r)
			} else {
				l.note(func(s *bakStats) { s.NuGet++ })
			}
			body := make([]byte, 32<<10)
			_, _ = rand.Read(body)
			p := fmt.Sprintf("load-%s/%05d.bin", run, i)
			if r, err := retrying(func() (reply, error) { return bakPutFile(a, p, body) }); err != nil || r.Status != 201 {
				l.fail("file put %s: %v %s", p, err, r)
			} else {
				files = append(files, p)
				l.note(func(s *bakStats) { s.Files++ })
			}
			switch {
			case i%3 == 2 && len(files) > 4: // a delete
				victim := files[0]
				files = files[1:]
				if r, err := retrying(func() (reply, error) { return a.send(http.MethodDelete, filesAddr(bakFiles)+"/"+victim, nil) }); err != nil || (r.Status != 204 && r.Status != 200) {
					l.fail("file delete %s: %v %s", victim, err, r)
				} else {
					l.note(func(s *bakStats) { s.Deleted++ })
				}
			case i%4 == 3 && len(files) > 4: // a trashed item: the trash can has no API yet (R-STOR-022 is P1), so its row is marked
				victim := files[0]
				files = files[1:]
				q := fmt.Sprintf(`UPDATE trove.items SET trashed_at = now() WHERE kind = 'file' AND trashed_at IS NULL AND path = '%s'`, victim)
				if _, err := bakSQL(ctx, q); err != nil {
					if ctx.Err() == nil {
						l.fail("trash %s: %v", victim, err)
					}
				} else {
					l.note(func(s *bakStats) { s.Trashed++ })
				}
			}
			if i%9 == 8 { // the trash is emptied: its rows go and release their blobs
				if _, err := bakSQL(ctx, `DELETE FROM trove.items WHERE trashed_at < now() - interval '5 seconds'`); err != nil && ctx.Err() == nil {
					l.fail("empty the trash: %v", err)
				} else {
					l.note(func(s *bakStats) { s.Emptied++ })
				}
			}
		}
	}()
	go func() {
		defer l.wg.Done()
		for ctx.Err() == nil {
			r, err := a.send(http.MethodPost, "/api/v1/system/tasks/gc/run", nil)
			switch {
			case err != nil:
				l.fail("gc run: %v", err)
			case r.Status == 202:
				l.note(func(s *bakStats) { s.Collections++ })
			case r.Status == 409:
				l.note(func(s *bakStats) { s.Throttled++ })
			default:
				l.fail("gc run: %s", r)
			}
			select {
			case <-ctx.Done():
			case <-time.After(10 * time.Second):
			}
		}
	}()
	return l
}

func (l *bakLoad) stop() bakStats {
	l.cancel()
	l.wg.Wait()
	return l.snapshot()
}

// ── Jobs ─────────────────────────────────────────────────────────────────────

// bakJob describes a Job made from the backup CronJob with another command, other environment or another retry budget.
type bakJob struct {
	name    string
	args    []string          // replaces the container's arguments when set
	env     map[string]string // added to the container's environment
	backoff *int              // replaces spec.backoffLimit when set
	node    string            // pins the pod to a node when set
	// preferNode asks the scheduler for a node without pinning the pod to it (a preferred node affinity of weight 100): the pod that
	// replaces an evicted one is scheduled wherever it fits (Q-HA-174)
	preferNode string
}

// bakCreateJob creates the Job from cronjob/trove-backup the way `kubectl create job --from` does (R-BAK-012) and applies the
// changes of the description to it.
func bakCreateJob(t testing.TB, j bakJob) {
	t.Helper()
	out := kn(t, "create", "job", j.name, "--from=cronjob/trove-backup", "--dry-run=client", "-o", "json")
	var job map[string]any
	if err := json.Unmarshal([]byte(out), &job); err != nil {
		t.Fatal(err)
	}
	spec := job["spec"].(map[string]any)
	if j.backoff != nil {
		spec["backoffLimit"] = *j.backoff
	}
	c := spec["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
	if j.args != nil {
		c["args"] = j.args
	}
	if j.node != "" {
		podSpec := spec["template"].(map[string]any)["spec"].(map[string]any)
		podSpec["nodeSelector"] = map[string]any{"kubernetes.io/hostname": j.node}
	}
	if j.preferNode != "" {
		podSpec := spec["template"].(map[string]any)["spec"].(map[string]any)
		podSpec["affinity"] = map[string]any{"nodeAffinity": map[string]any{"preferredDuringSchedulingIgnoredDuringExecution": []any{map[string]any{
			"weight": 100,
			"preference": map[string]any{"matchExpressions": []any{map[string]any{
				"key": "kubernetes.io/hostname", "operator": "In", "values": []any{j.preferNode},
			}}},
		}}}}
	}
	env, _ := c["env"].([]any)
	for k, v := range j.env {
		env = append(env, map[string]any{"name": k, "value": v})
	}
	c["env"] = env
	raw, _ := json.Marshal(job)
	cmd := exec.Command("kubectl", "--context", cfg.kubeContext, "-n", cfg.namespace, "create", "-f", "-")
	cmd.Stdin = bytes.NewReader(raw)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create job %s: %v: %s", j.name, err, b)
	}
	t.Cleanup(func() {
		_, _ = kubectl("-n", cfg.namespace, "delete", "job", j.name, "--ignore-not-found", "--wait=false")
	})
}

// jobEnd is how a Job ended.
type jobEnd struct {
	Condition string // Complete, Failed or "" while it runs
	Exits     []int  // the exit codes of its pods, oldest first
	Pods      int
	Reason    string
}

// bakJobState reads a Job and the exit codes of its pods.
func bakJobState(ns, name string) (jobEnd, error) {
	raw, err := kubectl("-n", ns, "get", "job", name, "-o", "json")
	if err != nil {
		return jobEnd{}, err
	}
	var j struct {
		Status struct {
			Conditions []struct{ Type, Status, Reason string } `json:"conditions"`
		} `json:"status"`
	}
	if err := json.Unmarshal([]byte(raw), &j); err != nil {
		return jobEnd{}, err
	}
	var e jobEnd
	for _, c := range j.Status.Conditions {
		if (c.Type == "Complete" || c.Type == "Failed") && c.Status == "True" {
			e.Condition, e.Reason = c.Type, c.Reason
		}
	}
	praw, err := kubectl("-n", ns, "get", "pods", "-l", "job-name="+name, "--sort-by=.metadata.creationTimestamp", "-o", "json")
	if err != nil {
		return e, err
	}
	var pl struct {
		Items []struct {
			Status struct {
				ContainerStatuses []struct {
					State struct {
						Terminated *struct {
							ExitCode int `json:"exitCode"`
						} `json:"terminated"`
					} `json:"state"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(praw), &pl); err != nil {
		return e, err
	}
	// An evicted or killed pod is gone from the list, so the pods the Job created are counted from its events.
	if ev, err := kubectl("-n", ns, "get", "events", "--field-selector", "involvedObject.kind=Job,involvedObject.name="+name+",reason=SuccessfulCreate", "-o", "name"); err == nil {
		e.Pods = len(strings.Fields(ev))
	}
	for _, p := range pl.Items {
		if e.Pods < len(pl.Items) {
			e.Pods = len(pl.Items)
		}
		for _, cs := range p.Status.ContainerStatuses {
			if cs.State.Terminated != nil {
				e.Exits = append(e.Exits, cs.State.Terminated.ExitCode)
			}
		}
	}
	return e, nil
}

// bakWaitJob waits until the Job is Complete or Failed.
func bakWaitJob(t testing.TB, ns, name string, within time.Duration) jobEnd {
	t.Helper()
	var last jobEnd
	waitFor(t, "Job "+name+" to end", within, 2*time.Second, func() bool {
		e, err := bakJobState(ns, name)
		if err != nil {
			return false
		}
		last = e
		return e.Condition != ""
	})
	return last
}

func bakJobLogs(ns, name string) string {
	out, _ := kubectl("-n", ns, "logs", "job/"+name, "--all-containers", "--tail=-1")
	return out
}

var backupIDPattern = regexp.MustCompile(`\b(\d{8}T\d{6}Z-[0-9a-f]{4})\b`)

// bakBackupID reads the ID of the backup a create Job's output names.
func bakBackupID(logs string) string {
	m := backupIDPattern.FindAllStringSubmatch(logs, -1)
	if len(m) == 0 {
		return ""
	}
	return m[len(m)-1][1]
}

// ── the second installation ──────────────────────────────────────────────────

// restoreNSScript runs deploy/dev/restore-ns.sh, whose output goes to the log of the test.
func restoreNSScript(t testing.TB, args ...string) string {
	t.Helper()
	return script(t, "restore-ns.sh", args...)
}

// restored is the restored installation seen through port-forwards to its two Services.
type restored struct {
	api, pkg *api
}

// openRestored forwards the Services of the restored installation and returns clients with the token.
func openRestored(t testing.TB, token string) *restored {
	t.Helper()
	fa := portForwardIn(t, restoreNS, "svc/trove-api", 8080)
	fp := portForwardIn(t, restoreNS, "svc/trove-packages", 8080)
	t.Cleanup(func() { stopForward(fa); stopForward(fp) })
	mk := func(f *forward) *api {
		return newAPI(t, withRoute(&route{pins: map[string]string{"localhost": f.addr}})).withToken(token)
	}
	return &restored{api: mk(fa), pkg: mk(fp)}
}

// bakTimings is the artifact of scenario 9: what the drill measured. It is written to the artifact directory.
type bakTimings struct {
	mu   sync.Mutex
	Data map[string]any `json:"data"`
}

var timings = &bakTimings{Data: map[string]any{}}

func (b *bakTimings) set(k string, v any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Data[k] = v
	raw, _ := json.MarshalIndent(b.Data, "", "  ")
	_ = os.WriteFile(filepath.Join(cfg.out, "backup-timings.json"), raw, 0o644)
}
