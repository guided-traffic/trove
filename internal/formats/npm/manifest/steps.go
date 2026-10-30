package manifest

import (
	"encoding/json"
	"path"
	"regexp"
	"sort"
	"strings"
)

// Facts are the tarball facts the normalization steps read. They come from the
// tarball whose package.json is compared, and both sides of a comparison are
// normalized against the same facts.
type Facts struct {
	// Files are the install paths (the entry name without its first component)
	// of the regular files of the tarball, slash-separated.
	Files []string
}

// hasFile reports whether a regular file has exactly this install path.
func (f Facts) hasFile(name string) bool {
	for _, p := range f.Files {
		if p == name {
			return true
		}
	}
	return false
}

// HasBindingGyp reports whether the tarball holds a top-level binding.gyp.
func (f Facts) HasBindingGyp() bool { return f.hasFile("binding.gyp") }

// hasTopLevelGyp reports whether the tarball holds a top-level "*.gyp" file, as
// the glob of npm's gypfile step matches (not a dotfile).
func (f Facts) hasTopLevelGyp() bool {
	for _, p := range f.Files {
		if !strings.Contains(p, "/") && strings.HasSuffix(p, ".gyp") && !strings.HasPrefix(p, ".") {
			return true
		}
	}
	return false
}

// The sources of the steps below are npm's @npmcli/package-json normalize.js
// (fix and prepare) and Yarn 1's normalization, R-NPM-007 and Q-NPM-6. Each step
// works on one manifest in place, is idempotent, and leaves a field it does not
// understand alone.

// NormalizeName trims the name.
func NormalizeName(m Manifest) {
	if s, ok := m["name"].(string); ok {
		m["name"] = strings.TrimSpace(s)
	}
}

// NormalizeVersion cleans the version the way semver's loose clean() does:
// "v1.2.3" and " =1.2.3+build " become "1.2.3". A version that is not one stays.
func NormalizeVersion(m Manifest) {
	if s, ok := m["version"].(string); ok {
		if c, ok := CleanVersion(s); ok {
			m["version"] = c
		}
	}
}

// jsSpace is JavaScript's \s, which RE2's \s is narrower than.
const jsSpace = `\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

var (
	depListSeparator = regexp.MustCompile(`[\n\r` + jsSpace + `,]+`)
	depNameSeparator = regexp.MustCompile(`:?[@` + jsSpace + `><=]`)
	scriptBinPrefix  = regexp.MustCompile(`^(?:\.[/\\])?node_modules[/\\]\.bin[/\\]`)
	engineSeparator  = regexp.MustCompile(`[` + jsSpace + `]+`)
)

func trimJS(s string) string {
	return strings.Trim(s, " \t\n\v\f\r\u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000\ufeff")
}

// NormalizeDependencies turns "dependencies" and "optionalDependencies" given
// as a string or an array into objects ("name@range" entries). In "dependencies"
// a value that is not an object is removed, so is a non-string range, and a
// hosted-git range is written in npm's canonical form (CanonicalGitSpec).
// Optional dependencies are not copied into "dependencies": npm no longer does.
func NormalizeDependencies(m Manifest) {
	for _, key := range []string{"dependencies", "optionalDependencies"} {
		v, present := m[key]
		if !present || !truthy(v) {
			continue
		}
		if s, ok := v.(string); ok {
			list := depListSeparator.Split(trimJS(s), -1)
			arr := make([]any, len(list))
			for i, e := range list {
				arr[i] = e
			}
			v = arr
		}
		if arr, ok := v.([]any); ok {
			obj := make(map[string]any, len(arr))
			for _, e := range arr {
				d, ok := e.(string)
				if !ok {
					continue
				}
				d = trimJS(d)
				name, rest := d, ""
				if loc := depNameSeparator.FindStringIndex(d); loc != nil {
					name, rest = d[:loc[0]], d[loc[0]:]
				}
				rest = strings.TrimPrefix(rest, "@")
				obj[name] = trimJS(rest)
			}
			v = obj
		}
		m[key] = v
	}

	if _, present := m["dependencies"]; present {
		obj, ok := m["dependencies"].(map[string]any)
		if !ok {
			delete(m, "dependencies")
			return
		}
		for name, rng := range obj {
			s, ok := rng.(string)
			if !ok {
				delete(obj, name)
				continue
			}
			obj[name] = CanonicalGitSpec(s)
		}
	}
}

// NormalizeBundleDependencies renames "bundledDependencies", expands "true" to
// the names of "dependencies", reduces an object to its keys, drops non-string
// members, adds a bundled name that "dependencies" lacks there with range "*",
// and writes the list sorted and without duplicates. "false", an empty list and
// absence are the same. Run it after NormalizeDependencies.
func NormalizeBundleDependencies(m Manifest) {
	if _, ok := m["bundleDependencies"]; !ok {
		if v, ok := m["bundledDependencies"]; ok {
			m["bundleDependencies"] = v
		}
	}
	delete(m, "bundledDependencies")

	var names []string
	switch v := m["bundleDependencies"].(type) {
	case bool:
		if v {
			if deps, ok := m["dependencies"].(map[string]any); ok {
				for n := range deps {
					names = append(names, n)
				}
			}
		}
	case map[string]any:
		for n := range v {
			names = append(names, n)
		}
	case []any:
		for _, e := range v {
			if s, ok := e.(string); ok && s != "" {
				names = append(names, s)
			}
		}
	}
	delete(m, "bundleDependencies")

	sort.Strings(names)
	list := make([]any, 0, len(names))
	deps, _ := m["dependencies"].(map[string]any)
	for i, n := range names {
		// the empty name is no member, whichever form the list came in
		if n == "" || (i > 0 && names[i-1] == n) {
			continue
		}
		if deps == nil {
			deps = map[string]any{}
			m["dependencies"] = deps
		}
		if _, ok := deps[n]; !ok {
			deps[n] = "*"
		}
		list = append(list, n)
	}
	if len(list) > 0 {
		m["bundleDependencies"] = list
	}
}

// secureAndUnixifyPath is npm's function of the same name: backslashes and
// colons become slashes and the path is resolved as if rooted, so no ".." and
// no leading slash survives. "" means nothing usable is left.
func secureAndUnixifyPath(ref string) string {
	u := strings.NewReplacer(`\`, "/", ":", "/").Replace(ref)
	joined := path.Join("/", u)
	out := strings.TrimPrefix(joined, "/")
	if out != "" && strings.HasSuffix(u, "/") {
		out += "/"
	}
	return out
}

// jsBasename is path.basename of Node.js for a posix path.
func jsBasename(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		p = p[i+1:]
	}
	return p
}

// NormalizeBin turns "bin" into an object of base name to clean relative
// target, drops entries that are not strings or end up empty, and removes an
// empty "bin". A string takes the package name as its key, an array the base
// names of its entries. When "bin" is absent and "directories.bin" names a
// folder, the bin entries are the regular files below it in the tarball.
func NormalizeBin(m Manifest, f Facts) {
	if dirs, ok := m["directories"].(map[string]any); ok && !truthy(m["bin"]) {
		if dir, ok := dirs["bin"].(string); ok && dir != "" {
			expandBinDir(m, secureAndUnixifyPath(dir), f)
			return
		}
	}

	bin := m["bin"]
	if !truthy(bin) {
		delete(m, "bin")
		return
	}
	switch v := bin.(type) {
	case string:
		name, _ := m["name"].(string)
		if name == "" {
			delete(m, "bin")
			return
		}
		bin = map[string]any{name: v}
	case []any:
		obj := make(map[string]any, len(v))
		for _, e := range v {
			if k, ok := e.(string); ok {
				obj[jsBasename(k)] = k
			}
		}
		bin = obj
	}
	obj, ok := bin.(map[string]any)
	if !ok {
		delete(m, "bin")
		return
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make(map[string]any, len(obj))
	for _, k := range keys {
		target, ok := obj[k].(string)
		if !ok {
			continue
		}
		base := jsBasename(secureAndUnixifyPath(k))
		clean := secureAndUnixifyPath(target)
		if base == "" || clean == "" {
			continue
		}
		out[base] = clean
	}
	if len(out) == 0 {
		delete(m, "bin")
		return
	}
	m["bin"] = out
}

// expandBinDir builds "bin" from the files below dir (already cleaned), as npm
// does for a package directory: dotfiles are skipped, the key is the file's base
// name.
func expandBinDir(m Manifest, dir string, f Facts) {
	prefix := ""
	if dir != "" {
		prefix = strings.TrimSuffix(dir, "/") + "/"
	}
	// npm globs "**", which lists the folders below the directory as well as its
	// files; a tarball names the folders only through its files.
	rels := map[string]struct{}{}
	for _, file := range f.Files {
		if !strings.HasPrefix(file, prefix) {
			continue
		}
		rel := file[len(prefix):]
		if rel == "" || hiddenPath(rel) {
			continue
		}
		rels[rel] = struct{}{}
		for d := path.Dir(rel); d != "."; d = path.Dir(d) {
			rels[d] = struct{}{}
		}
	}
	sorted := make([]string, 0, len(rels))
	for rel := range rels {
		sorted = append(sorted, rel)
	}
	sort.Strings(sorted)
	out := make(map[string]any, len(sorted))
	for _, rel := range sorted {
		// With the package root as the folder npm writes "/file", which its own
		// "bin" step would clean to "file"; write the clean form.
		out[path.Base(rel)] = prefix + secureAndUnixifyPath(rel)
	}
	if len(out) == 0 {
		delete(m, "bin")
		return
	}
	m["bin"] = out
}

func hiddenPath(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if strings.HasPrefix(seg, ".") {
			return true
		}
	}
	return false
}

// NormalizeScripts drops script entries that are not strings and removes a
// leading "node_modules/.bin/" (also written "./node_modules\.bin\") from the
// others. A "scripts" value that is not an object is removed.
func NormalizeScripts(m Manifest) {
	v, present := m["scripts"]
	if !present {
		return
	}
	scripts, ok := v.(map[string]any)
	if !ok {
		delete(m, "scripts")
		return
	}
	for name, s := range scripts {
		str, ok := s.(string)
		if !ok {
			delete(scripts, name)
			continue
		}
		scripts[name] = scriptBinPrefix.ReplaceAllString(str, "")
	}
}

// gypRebuild is the install script both clients add for a package with a gyp file.
const gypRebuild = "node-gyp rebuild"

// AddGypInstall adds the script "install": "node-gyp rebuild" the two rules
// add. npm's rule: a top-level "*.gyp" file, neither "install" nor
// "preinstall", and "gypfile" not false. Yarn 1's rule: a top-level
// "binding.gyp" and no "install", whatever "preinstall" and "gypfile" say. Run
// it after NormalizeScripts.
func AddGypInstall(m Manifest, f Facts) {
	scripts, _ := m["scripts"].(map[string]any)
	has := func(name string) bool {
		s, _ := scripts[name].(string)
		return s != ""
	}
	gypfileOff := false
	if b, ok := m["gypfile"].(bool); ok && !b {
		gypfileOff = true
	}
	npmRule := !has("install") && !has("preinstall") && !gypfileOff && f.hasTopLevelGyp()
	yarnRule := !has("install") && f.HasBindingGyp()
	if !npmRule && !yarnRule {
		return
	}
	if scripts == nil {
		scripts = map[string]any{}
		m["scripts"] = scripts
	}
	scripts["install"] = gypRebuild
}

// NormalizeEngines turns "engines" given as an array of "<engine> <range>"
// strings into an object (Yarn 1). An entry without a range gets "*".
func NormalizeEngines(m Manifest) {
	arr, ok := m["engines"].([]any)
	if !ok {
		return
	}
	obj := make(map[string]any, len(arr))
	for _, e := range arr {
		s, ok := e.(string)
		if !ok {
			continue
		}
		s = trimJS(s)
		name, rng := s, ""
		if loc := engineSeparator.FindStringIndex(s); loc != nil {
			name, rng = s[:loc[0]], trimJS(s[loc[1]:])
		}
		if name == "" {
			continue
		}
		if rng == "" {
			rng = "*"
		}
		obj[name] = rng
	}
	m["engines"] = obj
}

// truthy is JavaScript's truthiness for a decoded JSON value.
func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case json.Number:
		f, err := t.Float64()
		return err != nil || f != 0
	default:
		return true
	}
}
