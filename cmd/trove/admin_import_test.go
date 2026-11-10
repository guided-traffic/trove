package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/importer"
)

func TestR_IMP_001_TheImportCommandNeedsAFormatASourceAndATarget(t *testing.T) {
	for _, args := range [][]string{
		{"admin", "import"},
		{"admin", "import", "npm"},
		{"admin", "import", "npm", "--from", "https://x.example/"},
		{"admin", "import", "npm", "--to", "https://t.example/"},
	} {
		var out, errb bytes.Buffer
		if code := run(args, streams{stdout: &out, stderr: &errb}); code != 2 {
			t.Errorf("%v: exit %d, want 2 (usage)", args, code)
		}
		if !strings.Contains(errb.String(), "usage: trove admin import") {
			t.Errorf("%v: stderr %q does not show the usage", args, errb.String())
		}
	}
}

func TestR_IMP_002_ASecretComesFromAFileOrTheEnvironmentNeverFromAnArgument(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "token")
	if err := os.WriteFile(file, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := streams{environ: []string{"TROVE_IMPORT_TOKEN=from-env", "OTHER=x"}}
	if got, err := secret(s, envToken, file); err != nil || got != "from-file" {
		t.Errorf("file: %q %v, want the file's content without its line break", got, err)
	}
	if got, err := secret(s, envToken, ""); err != nil || got != "from-env" {
		t.Errorf("environment: %q %v", got, err)
	}
	if _, err := secret(streams{}, envToken, ""); err == nil {
		t.Error("no secret at all was accepted")
	}
	empty := filepath.Join(dir, "empty")
	_ = os.WriteFile(empty, []byte("\n"), 0o600)
	if _, err := secret(s, envToken, empty); err == nil {
		t.Error("an empty file was accepted")
	}
	big := filepath.Join(dir, "big")
	_ = os.WriteFile(big, bytes.Repeat([]byte("a"), maxSecretFile+1), 0o600)
	if _, err := secret(s, envToken, big); err == nil {
		t.Error("a file over 8 KiB was accepted")
	}
	// The usage names no flag that takes a secret as its value.
	for _, flag := range []string{"--token ", "--source-password ", "--password "} {
		if strings.Contains(importUsage, flag) {
			t.Errorf("the usage offers %q", flag)
		}
	}
}

func TestR_IMP_003_ANameFileHoldsOneNamePerLineWithComments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "names")
	_ = os.WriteFile(path, []byte("# the packages\n\n@acme/ui\n  left-pad  \n"), 0o600)
	got, err := readNames(path)
	if err != nil || strings.Join(got, ",") != "@acme/ui,left-pad" {
		t.Fatalf("%v %v", got, err)
	}
}

func TestR_IMP_006_TheReportNamesWhatNeedsALookAndNeverACredential(t *testing.T) {
	rep := &importer.Report{Format: "npm", Packages: []importer.PackageResult{
		{Name: "a", Versions: []importer.VersionResult{{Version: "1.0.0", Outcome: importer.OutcomeImported}}},
		{Name: "b", Versions: []importer.VersionResult{{Version: "1.0.0", Outcome: importer.OutcomeConflict, Code: "version_exists", Detail: "other content"}}, NotKept: []string{"deprecation of 1.0.0"}},
		{Name: "c", Error: "the source could not be read", Code: importer.CodeSourceError},
	}}
	var out bytes.Buffer
	path := filepath.Join(t.TempDir(), "report.json")
	if err := writeReport(streams{stdout: &out}, rep, path); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"conflict b@1.0.0: version_exists", "failed c: source_error", "not kept b: deprecation of 1.0.0", "2 packages need a look"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the summary lacks %q:\n%s", want, out.String())
		}
	}
	if b, err := os.ReadFile(path); err != nil || !strings.Contains(string(b), `"packages"`) {
		t.Errorf("report file: %v %q", err, b)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("report mode %v", st.Mode().Perm())
	}
}
