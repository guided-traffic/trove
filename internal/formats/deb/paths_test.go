package deb

import (
	"testing"

	"github.com/guided-traffic/trove/internal/formats"
)

// The classification of R-DEB-003, by path and file name only.
func TestClassify(t *testing.T) {
	f := New()
	cases := []struct {
		name, path string
		want       formats.PathInfo
	}{
		{"a pool file", "pool/h/hello/hello_2.10-3_amd64.deb", formats.PathInfo{Kind: formats.Content, Package: "hello", Version: "2.10-3"}},
		{
			"a pool file with a plus in the version", "pool/libx/libxml2/libxml2-utils_2.9.14+dfsg-1.3_amd64.deb",
			formats.PathInfo{Kind: formats.Content, Package: "libxml2-utils", Version: "2.9.14+dfsg-1.3"},
		},
		{"an architecture-independent file", "pool/main/t/trove-hello/trove-hello_1.0_all.deb", formats.PathInfo{Kind: formats.Content, Package: "trove-hello", Version: "1.0"}},
		{"a file of a member", "members/deb-local/pool/h/hello/hello_2.10-3_amd64.deb", formats.PathInfo{Kind: formats.Content, Package: "hello", Version: "2.10-3"}},
		{"a pool file whose name has not the form gets a routing name", "pool/h/hello/hello-extra.deb", formats.PathInfo{Kind: formats.Content}},
		{"a pool file with one underscore", "pool/h/hello/hello_2.10-3.deb", formats.PathInfo{Kind: formats.Content, RoutingName: "hello"}},
		{"a pool file with an upper-case package keeps a routing name", "pool/h/Hello/Hello_2.10-3_amd64.deb", formats.PathInfo{Kind: formats.Content, RoutingName: "Hello"}},
		{"a pool file with a colon in the version", "pool/h/hello/hello_1:2.0_amd64.deb", formats.PathInfo{Kind: formats.Content, RoutingName: "hello"}},
		{"a source file has one underscore and a routing name", "pool/h/hello/hello_2.10.orig.tar.xz", formats.PathInfo{Kind: formats.Content, RoutingName: "hello"}},
		{"an index of a suite", "dists/stable/main/binary-amd64/Packages.gz", formats.PathInfo{Kind: formats.PathMetadata}},
		{"InRelease", "dists/stable/InRelease", formats.PathInfo{Kind: formats.PathMetadata}},
		{"Release.gpg", "dists/stable/Release.gpg", formats.PathInfo{Kind: formats.PathMetadata}},
		{"a by-hash file", "dists/stable/main/binary-amd64/by-hash/SHA256/" + zeros, formats.PathInfo{Kind: formats.Content}},
		{"a pdiff patch", "dists/bookworm/main/binary-amd64/Packages.diff/2026-11-08-0000.00.gz", formats.PathInfo{Kind: formats.PathMetadata}},
		{"the signing key", "signing-key.asc", formats.PathInfo{Kind: formats.PathComputed}},
		{"a flat package file", "binary/trove-hello_1.0_all.deb", formats.PathInfo{Kind: formats.Content, Package: "trove-hello", Version: "1.0"}},
		{"a flat index", "binary/Packages.gz", formats.PathInfo{Kind: formats.PathMetadata}},
		{"a flat Sources index", "binary/Sources.xz", formats.PathInfo{Kind: formats.PathMetadata}},
		{"a flat Contents index", "binary/Contents-amd64.gz", formats.PathInfo{Kind: formats.PathMetadata}},
		{"a flat translation", "binary/Translation-en.bz2", formats.PathInfo{Kind: formats.PathMetadata}},
		{"a flat release", "binary/InRelease", formats.PathInfo{Kind: formats.PathMetadata}},
		{"the flat root release", "InRelease", formats.PathInfo{Kind: formats.PathMetadata}},
		{"the flat root package", "trove-hello_1.0_all.deb", formats.PathInfo{Kind: formats.Content, Package: "trove-hello", Version: "1.0"}},
		{"a flat Index of a diff directory", "binary/Packages.diff/Index", formats.PathInfo{Kind: formats.PathMetadata}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := f.Classify(formats.Path{Path: c.path}); got != c.want {
				t.Fatalf("Classify(%q) = %+v, want %+v", c.path, got, c.want)
			}
		})
	}
}

const zeros = "0000000000000000000000000000000000000000000000000000000000000000"

func TestPoolPath(t *testing.T) {
	cases := []struct{ source, pkg, version, arch, want string }{
		{"hello", "hello", "2.10-3", "amd64", "pool/h/hello/hello_2.10-3_amd64.deb"},
		{"libxml2", "libxml2-utils", "2.9.14+dfsg-1.3", "amd64", "pool/libx/libxml2/libxml2-utils_2.9.14+dfsg-1.3_amd64.deb"},
		{"", "trove-hello", "1:1.0-1", "all", "pool/t/trove-hello/trove-hello_1.0-1_all.deb"},
		{"lib", "lib", "1.0", "amd64", "pool/lib/lib/lib_1.0_amd64.deb"},
		{"libc", "libc6", "2.36-9", "arm64", "pool/libc/libc/libc6_2.36-9_arm64.deb"},
	}
	for _, c := range cases {
		if got := PoolPath(c.source, c.pkg, c.version, c.arch); got != c.want {
			t.Errorf("PoolPath(%q, %q, %q, %q) = %q, want %q", c.source, c.pkg, c.version, c.arch, got, c.want)
		}
	}
}

func TestPoolPathIsAFixedPointOfClassify(t *testing.T) {
	p := PoolPath("libxml2", "libxml2-utils", "1:2.9.14+dfsg-1.3", "amd64")
	info := New().Classify(formats.Path{Path: p})
	if info.Package != "libxml2-utils" || info.Version != "2.9.14+dfsg-1.3" {
		t.Fatalf("Classify(%q) = %+v", p, info)
	}
}

// The grammars of R-DEB-002.
func TestGrammars(t *testing.T) {
	t.Run("package", func(t *testing.T) {
		for _, s := range []string{"hello", "libxml2-utils", "g++", "0ad", "a+", "x.y"} {
			if !ValidPackage(s) {
				t.Errorf("%q is refused", s)
			}
		}
		for _, s := range []string{"", "a", "Hello", "-hello", "he llo", "hello_1", "he/llo", "hello:amd64"} {
			if ValidPackage(s) {
				t.Errorf("%q is accepted", s)
			}
		}
	})
	t.Run("version", func(t *testing.T) {
		for _, s := range []string{"1.0", "2.10-3", "1:0.1", "0:2.10-3", "1.0~rc1", "2.9.14+dfsg-1.3", "1.0-1-2", "999999999:1", "1.0+b1", "1.1~"} {
			if !ValidVersion(s) {
				t.Errorf("%q is refused", s)
			}
		}
		for _, s := range []string{"", "1.0:2", "a1.0", "1.0-", "-1", "1000000000:1", ":1", "1:", "1.0_1", "1.0 1", "1:2:3", "1.0-1_2"} {
			if ValidVersion(s) {
				t.Errorf("%q is accepted", s)
			}
		}
		long := "1." + string(make([]byte, 0))
		for len(long) < MaxVersionLength {
			long += "0"
		}
		if !ValidVersion(long) || ValidVersion(long+"0") {
			t.Errorf("the bound of %d characters does not hold", MaxVersionLength)
		}
	})
	t.Run("architecture", func(t *testing.T) {
		for _, s := range []string{"amd64", "arm64", "all", "armhf", "i386", "kfreebsd-amd64"} {
			if !ValidArchitecture(s) {
				t.Errorf("%q is refused", s)
			}
		}
		for _, s := range []string{"", "any", "linux-any", "any-amd64", "Amd64", "-x", "a_b"} {
			if ValidArchitecture(s) {
				t.Errorf("%q is accepted", s)
			}
		}
	})
	t.Run("suite and component", func(t *testing.T) {
		for _, s := range []string{"stable", "noble-updates", "bookworm", "contrib", "a.b_c+d-e"} {
			if !ValidSuite(s) {
				t.Errorf("%q is refused", s)
			}
		}
		for _, s := range []string{"", "Stable", "a/b", "-x", ".x"} {
			if ValidSuite(s) {
				t.Errorf("%q is accepted", s)
			}
		}
	})
}

func TestNamesNormalize(t *testing.T) {
	n := names{}
	for _, s := range []string{"hello", "acme-*", "[a-z]?tool", "{a,b}"} {
		if got, ok := n.Normalize(s); !ok || got != s {
			t.Errorf("Normalize(%q) = %q, %v", s, got, ok)
		}
	}
	for _, s := range []string{"Hello", "a", "he llo", ""} {
		if _, ok := n.Normalize(s); ok {
			t.Errorf("Normalize(%q) accepts a string that is no name", s)
		}
	}
}

func TestIsIndexName(t *testing.T) {
	for _, s := range []string{"Packages", "Packages.gz", "Packages.xz", "Packages.bz2", "Packages.lzma", "Packages.zst", "Sources.gz", "Contents-amd64.gz", "Translation-pt_BR.xz", "Index"} {
		if !IsIndexName(s) {
			t.Errorf("%q is no index name", s)
		}
	}
	for _, s := range []string{"Packages.txt", "hello_1.0_all.deb", "Contents-", "Translation-", "packages", "Release"} {
		if IsIndexName(s) {
			t.Errorf("%q is an index name", s)
		}
	}
}
