package debstub

import (
	"bytes"
	"crypto/md5" //nolint:gosec // by-hash directories named MD5Sum are part of the archive format
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/guided-traffic/trove/test/debstub/debgen"
)

// releaseTimeFormat is the format of the Date and Valid-Until fields.
const releaseTimeFormat = "Mon, 02 Jan 2006 15:04:05 UTC"

// archive is one Release directory: a suite below dists/, or a flat repository. It is rebuilt as a whole for each generation.
type archive struct {
	id string
	// dirs are the URL prefixes, ending in a slash, under which the Release, InRelease, Release.gpg and the index files lie. A
	// suite has one for its name and one for its codename.
	dirs []string
	// suite and codename are the fields of the Release; components and arches its Components and Architectures (flat: none).
	suite, codename string
	components      []string
	arches          []string
	flat            bool
	// base is the URL prefix the Filename fields of the Packages paragraphs are relative to; filePrefix is the directory of a
	// flat repository's files below it.
	base, filePrefix string
	indexes          []*index
	// gens are the generations built so far, oldest first; the last is the current one.
	gens []*generation
	// byHash holds the content of every generation's files, by the path below the Release directory.
	byHash map[string]*object
	// built is false for an archive whose first generation is made on the first request (the large index).
	built bool
	// bulk is the number of synthetic paragraphs of the large index.
	bulk int
}

// index is one Packages or Sources file with the packages in it.
type index struct {
	rel   string // the directory below the Release directory, ending in a slash, or "" for a flat repository
	kind  string // "Packages" or "Sources"
	pkgs  []*pkg
	srcs  []*srcPkg
	synth int // synthetic paragraphs after pkgs (the large index)
	extra []*pkg
	// diffs is the chain of patches that lead to the current content, oldest first (Packages only).
	diffs []diffRec
}

// diffRec is one patch: it turns the version of the index with the digest from into the next.
type diffRec struct {
	name                  string
	fromSHA256            string
	fromSize              int
	patchSHA256, gzSHA256 string
	patchSize, gzSize     int
	gz                    []byte
}

type generation struct {
	n    int
	date time.Time
	// files maps a path below the Release directory to the file as of this generation: every index file the Release lists.
	files map[string]*object
	// aux holds files the Release does not list: the patches of the pdiffs.
	aux map[string]*object
	// lines are the Release's checksum entries by hash algorithm, built with the files.
	paths []string
}

// object is a file the stub serves.
type object struct {
	data     []byte
	modified time.Time
	sum      debgen.Digests
}

func newObject(data []byte, modified time.Time) *object {
	return &object{data: data, modified: modified, sum: debgen.Sum(data)}
}

func (ix *index) render() []byte {
	var b bytes.Buffer
	if ix.kind == "Sources" {
		for _, sp := range ix.srcs {
			b.WriteString(sp.paragraph())
			b.WriteString("\n")
		}
		return b.Bytes()
	}
	for _, p := range ix.pkgs {
		b.WriteString(p.paragraph())
		b.WriteString("\n")
	}
	for i := 1; i <= ix.synth; i++ {
		b.WriteString(synthParagraph(i))
		b.WriteString("\n")
	}
	for _, p := range ix.extra {
		b.WriteString(p.paragraph())
		b.WriteString("\n")
	}
	return b.Bytes()
}

// synthParagraph is paragraph i of the large index: a complete Packages entry with digests made from the name. The file it names
// is not served.
func synthParagraph(i int) string {
	name := fmt.Sprintf("bulk-%05d", i)
	m := md5.Sum([]byte(name)) //nolint:gosec // see the import
	s := sha256.Sum256([]byte(name))
	return fmt.Sprintf("Package: %s\nVersion: 1.0-1\nInstalled-Size: 1\nMaintainer: trove test archive <debstub@trove.invalid>\nArchitecture: amd64\n"+
		"Description: synthetic package %d of the bounds index\nSection: misc\nPriority: optional\nFilename: pool/main/b/bulk/%s_1.0-1_amd64.deb\nSize: 1024\nMD5sum: %s\nSHA256: %s\n",
		name, i, name, hex.EncodeToString(m[:]), hex.EncodeToString(s[:]))
}

// build makes the next generation from the indexes' current content. date is the Release Date of the generation.
func (a *archive) build(date time.Time) {
	prev := (*generation)(nil)
	if len(a.gens) > 0 {
		prev = a.gens[len(a.gens)-1]
	}
	g := &generation{n: len(a.gens), date: date, files: map[string]*object{}, aux: map[string]*object{}}
	for _, ix := range a.indexes {
		plain := ix.render()
		name := ix.rel + ix.kind
		changed := prev == nil || prev.files[name] == nil || !bytes.Equal(prev.files[name].data, plain)
		modified := date
		if !changed {
			modified = prev.files[name].modified
		}
		g.files[name] = newObject(plain, modified)
		if changed {
			g.files[name+".gz"] = newObject(debgen.Gzip(plain), modified)
		} else {
			g.files[name+".gz"] = prev.files[name+".gz"]
		}
		if ix.kind == "Packages" && prev != nil && changed {
			ix.addDiff(prev.files[name].data, plain, date)
		}
		if len(ix.diffs) > 0 {
			ix.writeDiffIndex(g, name, plain, date, changed, prev)
		}
	}
	for p := range g.files {
		g.paths = append(g.paths, p)
	}
	sort.Strings(g.paths)
	a.gens = append(a.gens, g)
	a.built = true
	for p, o := range g.files {
		a.byHash[a.hashKey(p, "SHA256", o.sum.SHA256)] = o
		a.byHash[a.hashKey(p, "SHA512", o.sum.SHA512)] = o
		a.byHash[a.hashKey(p, "SHA1", o.sum.SHA1)] = o
		a.byHash[a.hashKey(p, "MD5Sum", o.sum.MD5)] = o
	}
	for p, o := range g.aux {
		a.byHash[a.hashKey(p, "SHA256", o.sum.SHA256)] = o
	}
}

// hashKey is the path of a by-hash file: the directory of the file, by-hash, the algorithm and the digest.
func (a *archive) hashKey(path, algo, digest string) string {
	dir := ""
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		dir = path[:i+1]
	}
	return dir + "by-hash/" + algo + "/" + digest
}

// addDiff records the patch from old to cur when cur is old plus appended paragraphs, the only change the stub makes.
func (ix *index) addDiff(old, cur []byte, date time.Time) {
	if !bytes.HasPrefix(cur, old) {
		ix.diffs = nil
		return
	}
	lines := bytes.Count(old, []byte("\n"))
	script := fmt.Sprintf("%da\n%s.\n", lines, cur[len(old):])
	name := date.Format("2006-01-02-1504.05")
	gz := debgen.Gzip([]byte(script))
	ps, gs := sha256.Sum256([]byte(script)), sha256.Sum256(gz)
	oldSum := sha256.Sum256(old)
	ix.diffs = append(ix.diffs, diffRec{
		name: name, fromSHA256: hex.EncodeToString(oldSum[:]), fromSize: len(old),
		patchSHA256: hex.EncodeToString(ps[:]), patchSize: len(script), gzSHA256: hex.EncodeToString(gs[:]), gzSize: len(gz), gz: gz,
	})
}

func (ix *index) writeDiffIndex(g *generation, name string, cur []byte, date time.Time, changed bool, prev *generation) {
	dir := name + ".diff/"
	for _, d := range ix.diffs {
		if _, ok := g.aux[dir+d.name+".gz"]; !ok {
			g.aux[dir+d.name+".gz"] = newObject(d.gz, date)
		}
	}
	if !changed && prev != nil && prev.files[dir+"Index"] != nil {
		g.files[dir+"Index"] = prev.files[dir+"Index"]
		return
	}
	curSum := sha256.Sum256(cur)
	var b strings.Builder
	fmt.Fprintf(&b, "SHA256-Current: %s %d\nSHA256-History:\n", hex.EncodeToString(curSum[:]), len(cur))
	for _, d := range ix.diffs {
		fmt.Fprintf(&b, " %s %d %s\n", d.fromSHA256, d.fromSize, d.name)
	}
	b.WriteString("SHA256-Patches:\n")
	for _, d := range ix.diffs {
		fmt.Fprintf(&b, " %s %d %s\n", d.patchSHA256, d.patchSize, d.name)
	}
	b.WriteString("SHA256-Download:\n")
	for _, d := range ix.diffs {
		fmt.Fprintf(&b, " %s %d %s.gz\n", d.gzSHA256, d.gzSize, d.name)
	}
	g.files[dir+"Index"] = newObject([]byte(b.String()), date)
}

// current is the newest generation.
func (a *archive) current() *generation { return a.gens[len(a.gens)-1] }

// releaseText renders the Release of the current generation under the mode's date and by-hash switches.
func (a *archive) releaseText(m Mode, now time.Time) []byte {
	g := a.current()
	date := g.date
	switch m.Date {
	case DateOlder:
		date = g.date.Add(-time.Hour)
	case DateAhead:
		date = now.Add(20 * time.Minute)
	}
	var b strings.Builder
	b.WriteString("Origin: trove-debstub\nLabel: trove-debstub\n")
	fmt.Fprintf(&b, "Suite: %s\nCodename: %s\n", a.suite, a.codename)
	fmt.Fprintf(&b, "Date: %s\n", date.UTC().Format(releaseTimeFormat))
	switch m.ValidUntil {
	case ValidUntilPassed:
		fmt.Fprintf(&b, "Valid-Until: %s\n", now.Add(-10*time.Minute).UTC().Format(releaseTimeFormat))
	case ValidUntilSixDays:
		fmt.Fprintf(&b, "Valid-Until: %s\n", date.Add(6*24*time.Hour).UTC().Format(releaseTimeFormat))
	}
	if !m.NoByHash {
		b.WriteString("Acquire-By-Hash: yes\n")
	}
	if !a.flat {
		fmt.Fprintf(&b, "Architectures: %s\nComponents: %s\n", strings.Join(a.arches, " "), strings.Join(a.components, " "))
	}
	b.WriteString("Description: trove counting test archive, generation " + fmt.Sprint(g.n) + "\n")
	for _, algo := range []struct {
		field string
		sum   func(*object) string
	}{
		{"MD5Sum", func(o *object) string { return o.sum.MD5 }},
		{"SHA1", func(o *object) string { return o.sum.SHA1 }},
		{"SHA256", func(o *object) string { return o.sum.SHA256 }},
		{"SHA512", func(o *object) string { return o.sum.SHA512 }},
	} {
		b.WriteString(algo.field + ":\n")
		for _, p := range g.paths {
			o := g.files[p]
			fmt.Fprintf(&b, " %s %8d %s\n", algo.sum(o), o.sum.Size, p)
		}
	}
	return []byte(b.String())
}

// hashes of an unsigned document, for the control API.
func sha256Hex(data []byte) string {
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}
