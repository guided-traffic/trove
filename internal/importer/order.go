package importer

import (
	"bytes"
	"compress/gzip"
	"io"
	"sort"
	"strconv"
	"strings"
)

// sortVersions puts the versions in the order they are imported: by the time the source published them, so that the target
// publishes them in the same order, and by version number where the times are equal or unknown (R-IMP-005).
func sortVersions(vs []Version) {
	sort.SliceStable(vs, func(i, j int) bool {
		a, b := vs[i], vs[j]
		if !a.Published.Equal(b.Published) && !a.Published.IsZero() && !b.Published.IsZero() {
			return a.Published.Before(b.Published)
		}
		return compareVersions(a.Version, b.Version) < 0
	})
}

// compareVersions orders two version numbers by the rules both formats share: numeric parts first (a missing part is 0),
// a pre-release before its release, pre-release identifiers numerically where they are numbers. Build metadata is ignored.
func compareVersions(a, b string) int {
	a, _, _ = strings.Cut(a, "+")
	b, _, _ = strings.Cut(b, "+")
	ac, ap, _ := strings.Cut(a, "-")
	bc, bp, _ := strings.Cut(b, "-")
	as, bs := strings.Split(ac, "."), strings.Split(bc, ".")
	for i := 0; i < max(len(as), len(bs)); i++ {
		if c := cmpInt(part(as, i), part(bs, i)); c != 0 {
			return c
		}
	}
	switch {
	case ap == "" && bp == "":
		return 0
	case ap == "":
		return 1
	case bp == "":
		return -1
	}
	ai, bi := strings.Split(ap, "."), strings.Split(bp, ".")
	for i := 0; i < min(len(ai), len(bi)); i++ {
		x, xe := strconv.Atoi(ai[i])
		y, ye := strconv.Atoi(bi[i])
		switch {
		case xe == nil && ye == nil:
			if c := cmpInt(x, y); c != 0 {
				return c
			}
		case xe == nil:
			return -1
		case ye == nil:
			return 1
		default:
			if c := strings.Compare(strings.ToLower(ai[i]), strings.ToLower(bi[i])); c != 0 {
				return c
			}
		}
	}
	return cmpInt(len(ai), len(bi))
}

func part(s []string, i int) int {
	if i >= len(s) {
		return 0
	}
	n, _ := strconv.Atoi(s[i])
	return n
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// maybeGunzip returns the body unpacked when it is gzip, which a registration hive may send whatever was asked for.
func maybeGunzip(b []byte, limit int64) ([]byte, error) {
	if len(b) < 2 || b[0] != 0x1f || b[1] != 0x8b {
		return b, nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	out, err := io.ReadAll(io.LimitReader(zr, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(out)) > limit {
		return nil, ErrTooLarge
	}
	return out, nil
}
