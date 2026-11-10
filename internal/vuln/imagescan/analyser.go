package imagescan

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Database is the one seam between the analysis and the vulnerability database (Q-VULN-137). A scan has two steps: the analysis of the
// layers, which needs no database and always runs in the worker job, and the detection, which compares what the analysis found against the
// database. Everything that depends on where the database lives is behind this interface: the client mode of [LocalDatabase] keeps a copy
// on the worker's disk, and a central server would be another implementation that answers with its flags. The analysis, the job, the
// findings and the blocking do not change with it.
type Database interface {
	// Acquire makes the database ready for one detection and returns how the analyser reaches it. It returns [ErrNoDatabase] while none
	// has been imported; the caller keeps the manifest pending and never records it as clean.
	Acquire(ctx context.Context) (DatabaseAccess, error)
}

// DatabaseAccess is what one detection needs to know about the database.
type DatabaseAccess struct {
	// Args are the analyser's flags that say where the database is.
	Args []string
	// Digest identifies the database the detection ran against; a different one makes every manifest due again.
	Digest string
	// Held is how many bytes of the spool's budget the unpacked database holds; 0 for a database that is not on the worker's disk.
	Held int64
	// Release ends the use of the database; it is never nil.
	Release func()
}

// ErrNoDatabase says that no vulnerability database has been imported yet.
var ErrNoDatabase = errors.New("no vulnerability database has been imported")

// Limits bound one run of the analyser. An image is untrusted input, so each of them is enforced by the worker and none is left to the
// analyser's good behaviour (docs/security/image-scanning.md).
type Limits struct {
	// Timeout ends one step of the analyser.
	Timeout time.Duration
	// MemoryBytes is the soft limit of the analyser's Go heap, 0 for none.
	MemoryBytes int64
	// FileBytes is the largest file the analyser may write, and OutputBytes the largest report it may leave.
	FileBytes, OutputBytes int64
	// StderrBytes is how much of the analyser's error output is kept for the log.
	StderrBytes int
}

// Analyser is the two steps of a scan.
type Analyser interface {
	// Version names the analyser; an analysis of another version is done again.
	Version() string
	// Analyse reads the OCI layout at layout and writes the list of what the image contains to sbom. It needs no database.
	Analyse(ctx context.Context, layout, sbom, scratch string, lim Limits) error
	// Detect compares the list at sbom with the database and writes the report to out.
	Detect(ctx context.Context, sbom, out, scratch string, db DatabaseAccess, lim Limits) error
}

// javaArchives are left out of the analysis: reading them needs a second database, of Java artifacts, which trove does not mirror (H-810).
// Without the option the analyser stops on the first image that holds a JAR, because it refuses to work offline without that database.
const javaArchives = "**/*.jar,**/*.war,**/*.ear,**/*.par"

// Trivy runs the pinned binary in client mode: a process per step, with an empty environment, a working directory of its own and the flags
// that keep it from reaching anything outside the installation (R-VULN-005).
type Trivy struct {
	// Binary is the path of the executable.
	Binary string
	// AnalyserVersion is the version of the binary, read once by [NewTrivy].
	AnalyserVersion string
}

var _ Analyser = (*Trivy)(nil)

// quietFlags keep a run from contacting the publisher: no version check, no telemetry, no update of a database.
func quietFlags() []string {
	return []string{"--no-progress", "--quiet", "--skip-version-check", "--disable-telemetry", "--skip-db-update", "--skip-java-db-update", "--offline-scan"}
}

// NewTrivy checks that the binary runs and reads its version.
func NewTrivy(ctx context.Context, binary string) (*Trivy, error) {
	t := &Trivy{Binary: binary}
	out, err := t.run(ctx, os.TempDir(), Limits{Timeout: time.Minute, StderrBytes: 4 << 10}, []string{"--version", "--format", "json"})
	if err != nil {
		return nil, fmt.Errorf("the analyser %s does not run: %w", binary, err)
	}
	v := versionOf(out)
	if v == "" {
		return nil, fmt.Errorf("the analyser %s reports no version", binary)
	}
	t.AnalyserVersion = v
	return t, nil
}

// versionOf reads the Version member of `trivy --version --format json` without decoding the rest.
func versionOf(out []byte) string {
	const key = `"Version":"`
	s := string(bytes.ReplaceAll(out, []byte(" "), nil))
	i := strings.Index(s, key)
	if i < 0 {
		return ""
	}
	s = s[i+len(key):]
	j := strings.IndexByte(s, '"')
	if j <= 0 || j > 32 {
		return ""
	}
	return s[:j]
}

// Version implements [Analyser].
func (t *Trivy) Version() string { return t.AnalyserVersion }

// Analyse implements [Analyser].
func (t *Trivy) Analyse(ctx context.Context, layout, sbom, scratch string, lim Limits) error {
	args := []string{
		"image", "--input", layout, "--format", "cyclonedx", "--output", sbom, "--cache-dir", filepath.Join(scratch, "cache"),
		"--timeout", timeoutArg(lim.Timeout), "--skip-files", javaArchives,
	}
	args = append(args, quietFlags()...)
	if _, err := t.run(ctx, scratch, lim, args); err != nil {
		return err
	}
	return checkOutput(sbom, lim.OutputBytes)
}

// Detect implements [Analyser]. The list goes in, the database comes from the seam, and the layers are not touched.
func (t *Trivy) Detect(ctx context.Context, sbom, out, scratch string, db DatabaseAccess, lim Limits) error {
	args := []string{"sbom", sbom, "--format", "json", "--output", out, "--scanners", "vuln", "--timeout", timeoutArg(lim.Timeout)}
	args = append(args, db.Args...)
	args = append(args, quietFlags()...)
	if _, err := t.run(ctx, scratch, lim, args); err != nil {
		return err
	}
	return checkOutput(out, lim.OutputBytes)
}

func timeoutArg(d time.Duration) string { return d.Round(time.Second).String() }

func checkOutput(path string, limit int64) error {
	st, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("the analyser wrote no output: %w", err)
	}
	if limit > 0 && st.Size() > limit {
		return &ScanError{Code: CodeReportTooLarge, Detail: fmt.Sprintf("the analyser's output is %d bytes, the limit is %d", st.Size(), limit)}
	}
	return nil
}

// run starts one process of the analyser. The environment is empty but for what it needs, so no credential of the worker reaches it; the
// process group is killed with the context, and the memory limit and working directory are its own.
func (t *Trivy) run(ctx context.Context, scratch string, lim Limits, args []string) ([]byte, error) {
	if lim.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, lim.Timeout+30*time.Second)
		defer cancel()
	}
	//nolint:gosec // the binary comes from the configuration and the arguments are built above, not from user input.
	cmd := exec.CommandContext(ctx, t.Binary, args...)
	cmd.Dir = scratch
	cmd.Env = []string{"HOME=" + scratch, "TMPDIR=" + scratch, "PATH="}
	if lim.MemoryBytes > 0 {
		cmd.Env = append(cmd.Env, "GOMEMLIMIT="+strconv.FormatInt(lim.MemoryBytes, 10))
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	var stdout bytes.Buffer
	stderr := &boundedBuffer{limit: max(lim.StderrBytes, 1024)}
	cmd.Stdout, cmd.Stderr = &stdout, stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, &ScanError{Code: CodeTimeout, Detail: "the analyser did not finish within its time limit", Terminal: false}
			}
			return nil, context.Cause(ctx)
		}
		return nil, &ScanError{Code: CodeAnalyserFailed, Detail: fmt.Sprintf("%s: %s", err, stderr.Text())}
	}
	return stdout.Bytes(), nil
}

// boundedBuffer keeps the end of what is written to it, the part that names the error.
type boundedBuffer struct {
	limit int
	buf   []byte
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.buf = append(b.buf, p...)
	if over := len(b.buf) - b.limit; over > 0 {
		b.buf = b.buf[over:]
	}
	return len(p), nil
}

// Text is the kept output on one line.
func (b *boundedBuffer) Text() string {
	return strings.Join(strings.Fields(string(b.buf)), " ")
}
