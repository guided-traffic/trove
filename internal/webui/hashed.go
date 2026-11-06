package webui

import (
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
)

// The hashed files of a UI build (R-UI-001, Q-UI-140). The Angular production build names every script, stylesheet and media
// file after its content: `main-6E6OBYEH.js` and `styles-DYCJMKF7.css` carry eight upper-case base32 characters, the chunks
// of the chunk optimizer eight base64url characters of either case (`chunk-Bo__WLzq.js`, `chunk-b3RHWPW-.js`). Such a name
// never changes its code, so the file is served as immutable, and it is the only kind of file a replica may serve from
// the shared asset store (Q-HA-120). The hash covers the code before the translations are inlined: the locales of one
// build share the names and differ in content, so a file is always named by its locale directory and its path.
//
// web/scripts/build-embed.mjs fails a build whose scripts, stylesheets or media files do not match this pattern, or whose
// unhashed files (the copies of web/public) do: the pattern is a contract between the two, and either side's change shows.

// hashedName matches the last path segment of a hashed file.
var hashedName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*-[A-Za-z0-9_-]{8}\.[A-Za-z0-9]{1,8}$`)

// hashedDir matches a directory segment above a hashed file (media/).
var hashedDir = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// maxHashedDepth bounds the directories above a hashed file: the build writes them at the top of the locale directory or
// one level down (media/).
const maxHashedDepth = 2

// IsHashed reports whether path, relative to a locale directory, names a hashed file of the build: at most two plain
// directories and a last segment of the form <name>-<8 hash characters>.<extension>. No dot segment, no empty segment and
// no index.html ever is.
func IsHashed(path string) bool {
	if len(path) == 0 || len(path) > 255 {
		return false
	}
	segs := strings.Split(path, "/")
	if len(segs)-1 > maxHashedDepth {
		return false
	}
	for _, d := range segs[:len(segs)-1] {
		if !hashedDir.MatchString(d) {
			return false
		}
	}
	return hashedName.MatchString(segs[len(segs)-1])
}

// File is one hashed file of a UI tree: its locale directory and its path below it.
type File struct {
	Locale string
	Path   string
	Size   int64
}

// Name is the file's path in the tree, <locale>/<path>.
func (f File) Name() string { return f.Locale + "/" + f.Path }

// Tree returns root, or the UI embedded in the binary when root is nil: the tree Options.Assets replaces.
func Tree(root fs.FS) (fs.FS, error) {
	if root != nil {
		return root, nil
	}
	return embeddedRoot()
}

// HashedFiles lists the hashed files of the locale directories of a UI tree, ordered by locale and path. A tree without
// locale directories (the placeholder of a binary built without the web build) has none; a configured locale that is
// missing from a built tree is New's error, not this one's.
func HashedFiles(tree fs.FS, locales []string) ([]File, error) {
	var out []File
	for _, l := range locales {
		if !localeCode.MatchString(l) {
			return nil, fmt.Errorf("webui: locale %q is not a valid locale code", l)
		}
		err := fs.WalkDir(tree, l, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			rel := strings.TrimPrefix(p, l+"/")
			if !IsHashed(rel) {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			out = append(out, File{Locale: l, Path: rel, Size: info.Size()})
			return nil
		})
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("webui: list the hashed files of %s: %w", l, err)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out, nil
}
