package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// DefaultFile is the configuration file read when neither --config nor
// TROVE_CONFIG_FILE names one.
const DefaultFile = "/etc/trove/trove.yaml"

// EnvConfigFile is the reserved variable that names the configuration file.
const EnvConfigFile = "TROVE_CONFIG_FILE"

// Source says where a value came from.
type Source struct {
	Kind   string // "default", "file", "env", "flag", "derived" or "secret-file"
	Detail string // variable name, flag name or secret file path
}

// String renders the source as the API and `admin config validate` show it:
// default, file, env, flag:--role, or file:<path> for a value read from a
// secret file.
func (s Source) String() string {
	switch s.Kind {
	case "secret-file":
		return "file:" + s.Detail
	case "flag":
		return "flag:" + s.Detail
	}
	return s.Kind
}

// Options controls one Load.
type Options struct {
	// File is the configuration file (--config). Empty means TROVE_CONFIG_FILE,
	// then DefaultFile. A file named explicitly must exist; a missing DefaultFile
	// is not an error, so a replica can run from defaults and the environment.
	File string
	// Environ is the process environment ("NAME=value"); nil means os.Environ.
	Environ []string
	// Flags are values given on the command line, by key path (--role is
	// "process.role", --log-level is "log.level"). They win over the environment.
	Flags map[string]string
	// FlagNames names the flag a Flags key came from, for the source column.
	FlagNames map[string]string
	// ReadFile reads files; nil means os.ReadFile.
	ReadFile func(string) ([]byte, error)
	// SkipSecretFiles leaves `<name>_file` of value-and-file secrets unread.
	SkipSecretFiles bool
	// Command selects the command-specific checks of CheckFor; Load runs them.
	Command Command
}

// Loaded is the result of a successful Load.
type Loaded struct {
	Config  *Config
	Schema  *Schema
	Sources map[string]Source // by key path
	File    string            // the file that was read; empty if none
	root    reflect.Value
}

// reservedEnv are environment variables with the TROVE_ prefix that are not
// configuration keys (R-OPS-028), plus TROVE_TEST_ for the integration suite.
func reservedEnv(name string) bool {
	switch name {
	case EnvConfigFile, "TROVE_POD_NAME", "TROVE_POD_NAMESPACE", "TROVE_NODE_NAME", "TROVE_POOL_NAME":
		return true
	}
	return strings.HasPrefix(name, "TROVE_CAC_") || strings.HasPrefix(name, "TROVE_TEST_")
}

type loader struct {
	opts    Options
	schema  *Schema
	root    reflect.Value
	sources map[string]Source
	issues  []Issue
}

func (l *loader) fail(format string, args ...any) {
	l.issues = append(l.issues, Issue{Message: fmt.Sprintf(format, args...)})
}

// Load builds the configuration: defaults, then the file, then the
// environment, then flags (R-OPS-028). It reports every problem it finds.
func Load(opts Options) (*Loaded, error) {
	cfg := &Config{}
	loaded, err := loadInto(cfg, opts)
	if loaded != nil {
		loaded.Config = cfg
	}
	return loaded, err
}

// loadInto is Load for any tree; the tests use it with synthetic trees.
func loadInto(root any, opts Options) (*Loaded, error) {
	rv := reflect.ValueOf(root).Elem()
	schema, defErrs := Describe(rv.Type())
	if len(defErrs) > 0 {
		return nil, errors.Join(defErrs...)
	}
	l := &loader{opts: opts, schema: schema, root: rv, sources: map[string]Source{}}
	if l.opts.Environ == nil {
		l.opts.Environ = os.Environ()
	}
	if l.opts.ReadFile == nil {
		l.opts.ReadFile = os.ReadFile
	}

	l.applyDefaults()
	file := l.applyFile()
	l.applyEnv()
	l.applyFlags()
	l.resolveSecrets()
	if len(l.issues) == 0 {
		l.validate()
	}
	if len(l.issues) == 0 {
		l.finalize()
	}
	if len(l.issues) > 0 {
		return nil, &Error{Issues: l.issues}
	}
	return &Loaded{Schema: schema, Sources: l.sources, File: file, root: rv}, nil
}

func (l *loader) field(k Key) reflect.Value { return l.root.FieldByIndex(k.Index) }

func (l *loader) applyDefaults() {
	for _, k := range l.schema.Keys {
		l.sources[k.Path] = Source{Kind: "default"}
		if !k.HasDefault {
			continue
		}
		v, err := parseText(k.Type, k.Default)
		if err != nil { // Describe already parsed it
			l.fail("%s: default %q: %v", k.Path, k.Default, err)
			continue
		}
		l.field(k).Set(v)
	}
}

func (l *loader) set(k Key, v reflect.Value, src Source) {
	l.field(k).Set(v)
	l.sources[k.Path] = src
}

func (l *loader) configFile() (path string, explicit bool) {
	if l.opts.File != "" {
		return l.opts.File, true
	}
	for _, e := range l.opts.Environ {
		if v, ok := strings.CutPrefix(e, EnvConfigFile+"="); ok && v != "" {
			return v, true
		}
	}
	return DefaultFile, false
}

func (l *loader) applyFile() string {
	path, explicit := l.configFile()
	data, err := l.opts.ReadFile(path)
	if err != nil {
		if !explicit && errors.Is(err, fs.ErrNotExist) {
			return ""
		}
		l.fail("cannot read configuration file %s: %v", path, err)
		return ""
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		l.fail("%s: %v", path, err)
		return path
	}
	if doc.Kind == 0 { // empty file
		return path
	}
	top := doc.Content[0]
	if top.Kind == yaml.ScalarNode && top.Tag == "!!null" {
		return path
	}
	if top.Kind != yaml.MappingNode {
		l.fail("%s:%d: the configuration must be a mapping of sections", path, top.Line)
		return path
	}
	l.walkNode(path, "", top)
	return path
}

// walkNode matches the keys of one mapping against the schema.
func (l *loader) walkNode(file, prefix string, n *yaml.Node) {
	var siblings []string
	for _, k := range l.schema.Keys {
		if p, ok := strings.CutPrefix(k.Path, prefix); ok && !strings.Contains(p, ".") {
			siblings = append(siblings, p)
		}
	}
	for s := range l.schema.structs {
		if p, ok := strings.CutPrefix(s, prefix); ok && !strings.Contains(p, ".") {
			siblings = append(siblings, p)
		}
	}
	sort.Strings(siblings)

	for i := 0; i+1 < len(n.Content); i += 2 {
		kn, vn := n.Content[i], n.Content[i+1]
		path := prefix + kn.Value
		at := fmt.Sprintf("%s:%d", file, kn.Line)
		if l.schema.IsSection(path) {
			switch {
			case vn.Kind == yaml.MappingNode:
				l.walkNode(file, path+".", vn)
			case vn.Kind == yaml.ScalarNode && vn.Tag == "!!null":
			default:
				l.fail("%s: %s must be a mapping", at, path)
			}
			continue
		}
		key, ok := l.schema.ByPath(path)
		if !ok {
			msg := fmt.Sprintf("%s: unknown key %q", at, path)
			if c, near := closest(kn.Value, siblings); near {
				msg += fmt.Sprintf(" (did you mean %q?)", prefix+c)
			}
			l.fail("%s", msg)
			continue
		}
		if vn.Kind == yaml.ScalarNode && vn.Tag == "!!null" {
			continue // an empty value keeps the default
		}
		if key.Kind == KindSecretValue {
			l.fail("%s: %s: a secret must not be written into the configuration file; set %s_file (%s_FILE) to a file that holds it, or the environment variable %s",
				at, path, path, key.Env, key.Env)
			continue
		}
		v, err := nodeValue(key.Type, vn)
		if err != nil {
			l.fail("%s: %s %v", at, path, err)
			continue
		}
		l.set(key, v, Source{Kind: "file"})
	}
}

func (l *loader) applyEnv() {
	var names []string
	vals := map[string]string{}
	for _, e := range l.opts.Environ {
		name, val, ok := strings.Cut(e, "=")
		if !ok || !strings.HasPrefix(name, EnvPrefix) || reservedEnv(name) {
			continue
		}
		names = append(names, name)
		vals[name] = val
	}
	sort.Strings(names)
	all := make([]string, 0, len(l.schema.Keys))
	for _, k := range l.schema.Keys {
		all = append(all, k.Env)
	}
	for _, name := range names {
		key, ok := l.schema.ByEnv(name)
		if !ok {
			msg := fmt.Sprintf("unknown environment variable %s", name)
			if c, near := closest(name, all); near {
				msg += fmt.Sprintf(" (did you mean %s?)", c)
			}
			l.fail("%s", msg)
			continue
		}
		v, err := parseText(key.Type, vals[name])
		if err != nil {
			l.fail("%s: %s %v", name, key.Path, err)
			continue
		}
		l.set(key, v, Source{Kind: "env", Detail: name})
	}
}

func (l *loader) applyFlags() {
	paths := make([]string, 0, len(l.opts.Flags))
	for p := range l.opts.Flags {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		key, ok := l.schema.ByPath(p)
		if !ok {
			l.fail("flag for unknown key %q", p)
			continue
		}
		v, err := parseText(key.Type, l.opts.Flags[p])
		if err != nil {
			l.fail("%s: %s %v", flagName(l.opts, p), p, err)
			continue
		}
		l.set(key, v, Source{Kind: "flag", Detail: flagName(l.opts, p)})
	}
}

func flagName(o Options, path string) string {
	if n := o.FlagNames[path]; n != "" {
		return n
	}
	return path
}

// resolveSecrets turns `<name>_file` of every value-and-file secret into its
// value, and rejects a secret set both ways.
func (l *loader) resolveSecrets() {
	for _, fk := range l.schema.Keys {
		if fk.Kind != KindSecretFile {
			continue
		}
		valuePath := strings.TrimSuffix(fk.Path, "_file")
		vk, _ := l.schema.ByPath(valuePath)
		file := l.field(fk).String()
		hasValue := l.field(vk).String() != ""
		if hasValue && file != "" {
			l.fail("%s and %s are both set; set exactly one (%s or %s)", valuePath, fk.Path, vk.Env, fk.Env)
			continue
		}
		if file == "" || l.opts.SkipSecretFiles {
			continue
		}
		data, err := l.opts.ReadFile(file)
		if err != nil {
			l.fail("%s: cannot read %s: %v", fk.Path, file, err)
			continue
		}
		l.field(vk).SetString(strings.TrimRight(string(data), "\r\n"))
		l.sources[vk.Path] = Source{Kind: "secret-file", Detail: file}
	}
}

// role returns process.role, empty when the root has no such field.
func (l *loader) role() string {
	p := l.root.FieldByName("Process")
	if !p.IsValid() {
		return ""
	}
	if f := p.FieldByName("Role"); f.IsValid() && f.Kind() == reflect.String {
		return f.String()
	}
	return ""
}

// validate runs the tag rules, the section validators and the command checks.
func (l *loader) validate() {
	for _, k := range l.schema.Keys {
		for _, msg := range k.check(l.field(k)) {
			l.issues = append(l.issues, Issue{Message: msg})
		}
	}
	for _, s := range l.schema.Sections {
		prefix := ""
		if s.Path != "" {
			prefix = s.Path + "."
		}
		sec := l.root
		if len(s.Index) > 0 {
			sec = l.root.FieldByIndex(s.Index)
		}
		ptr := sec.Addr().Interface()
		rep := &Report{prefix: prefix, issues: &l.issues, role: l.role()}
		if v, ok := ptr.(Validator); ok {
			v.Validate(rep)
		}
		if v, ok := ptr.(CommandValidator); ok {
			v.ValidateFor(l.opts.Command, rep)
		}
	}
}
