package app

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestCheckBinaryVersion(t *testing.T) {
	t.Run("R-ARCH-026 a commit hash as the binary version is logged as an ERROR that names the version and the way out", func(t *testing.T) {
		var buf bytes.Buffer
		log := slog.New(slog.NewJSONHandler(&buf, nil))
		if checkBinaryVersion(log, "190b780-dirty") {
			t.Fatal("a commit hash must not be a usable version")
		}
		out := buf.String()
		for _, want := range []string{`"level":"ERROR"`, `"version":"190b780-dirty"`, "never pass the schema check", "VERSION=dev"} {
			if !strings.Contains(out, want) {
				t.Errorf("log %q lacks %q", out, want)
			}
		}
	})

	t.Run("R-ARCH-026 dev, a release and a pre-release log nothing", func(t *testing.T) {
		for _, v := range []string{"dev", "0.3.1", "v0.3.1", "0.4.0-rc1"} {
			var buf bytes.Buffer
			if !checkBinaryVersion(slog.New(slog.NewJSONHandler(&buf, nil)), v) || buf.Len() != 0 {
				t.Errorf("version %q: usable = false or logged %q", v, buf.String())
			}
		}
	})
}
