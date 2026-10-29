package config

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
)

// EnvPrefix starts the name of every configuration environment variable.
const EnvPrefix = "TROVE_"

// KeyKind says what a key is.
type KeyKind int

// The kinds of keys.
const (
	// KindValue is an ordinary key.
	KindValue KeyKind = iota
	// KindSecretValue is the value half of a Secret: environment only.
	KindSecretValue
	// KindSecretFile is the `<name>_file` half of a Secret.
	KindSecretFile
	// KindSecretFileOnly is a file-only secret: a path read again on every use.
	KindSecretFileOnly
)

// IsSecret reports whether the key names or holds a secret.
func (k KeyKind) IsSecret() bool { return k != KindValue }

// Key is one leaf of the configuration tree.
type Key struct {
	Path       string       // dotted YAML path, e.g. "database.pool.max_conns"
	Env        string       // environment variable, e.g. "TROVE_DATABASE_POOL_MAX_CONNS"
	Kind       KeyKind      //
	Index      []int        // field index path from the root struct
	Type       reflect.Type // Go type of the stored value
	Default    string       // text form of the default; valid when HasDefault
	HasDefault bool         //
	Required   bool         // `validate:"required"`
	Rules      []rule       // other validation rules
	Doc        string       // `doc:"…"` tag
}

// Section is a struct node of the tree. Sections may implement Validator.
type Section struct {
	Path  string // "" for the root
	Index []int
}

// Schema is the flattened configuration tree of a root struct type.
type Schema struct {
	Keys     []Key
	Sections []Section
	byPath   map[string]int
	byEnv    map[string]int
	structs  map[string]bool
}

var (
	secretTyp = reflect.TypeOf(Secret{})
	durTyp    = reflect.TypeOf(time.Duration(0))
	sizeTyp   = reflect.TypeOf(ByteSize(0))
)

// Describe flattens the struct tree of t into its keys. It returns every
// problem of the tree at once: a field without a yaml tag, a name that is not
// snake_case, two keys that derive the same environment variable, an
// unsupported type, an unparsable default or rule (R-ARCH-020).
func Describe(t reflect.Type) (*Schema, []error) {
	s := &Schema{byPath: map[string]int{}, byEnv: map[string]int{}, structs: map[string]bool{}}
	var errs []error
	if t.Kind() != reflect.Struct {
		return nil, []error{fmt.Errorf("config root %s is not a struct", t)}
	}
	s.walk(t, "", nil, &errs)
	return s, errs
}

// MustDescribe is Describe for the compiled-in tree; it panics on a defect,
// which the unit tests of this package rule out.
func MustDescribe() *Schema {
	s, errs := Describe(reflect.TypeOf(Config{}))
	if len(errs) > 0 {
		panic(fmt.Sprintf("config tree is invalid: %v", errs))
	}
	return s
}

func envName(path string) string {
	return EnvPrefix + strings.ToUpper(strings.ReplaceAll(path, ".", "_"))
}

func (s *Schema) walk(t reflect.Type, prefix string, idx []int, errs *[]error) {
	s.Sections = append(s.Sections, Section{Path: strings.TrimSuffix(prefix, "."), Index: append([]int(nil), idx...)})
	if prefix != "" {
		s.structs[strings.TrimSuffix(prefix, ".")] = true
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		where := t.String() + "." + f.Name
		if !f.IsExported() {
			*errs = append(*errs, fmt.Errorf("%s: unexported field in the configuration tree", where))
			continue
		}
		tag, ok := f.Tag.Lookup("yaml")
		name, _, _ := strings.Cut(tag, ",")
		if !ok || name == "" || name == "-" {
			*errs = append(*errs, fmt.Errorf("%s: field has no yaml tag", where))
			continue
		}
		if !isSnakeCase(name) {
			*errs = append(*errs, fmt.Errorf("%s: yaml name %q is not snake_case", where, name))
			continue
		}
		path := prefix + name
		fidx := append(append([]int(nil), idx...), i)

		switch {
		case f.Type == secretTyp:
			s.addKey(Key{Path: path, Kind: KindSecretValue, Index: append(append([]int(nil), fidx...), 0), Type: reflect.TypeOf(""), Doc: f.Tag.Get("doc")}, where, errs)
			s.addKey(Key{Path: path + "_file", Kind: KindSecretFile, Index: append(append([]int(nil), fidx...), 1), Type: reflect.TypeOf(""), Doc: f.Tag.Get("doc")}, where, errs)
		case f.Type.Kind() == reflect.Struct:
			s.walk(f.Type, path+".", fidx, errs)
		default:
			k := Key{Path: path, Kind: KindValue, Index: fidx, Type: f.Type, Doc: f.Tag.Get("doc")}
			if sec := f.Tag.Get("secret"); sec != "" {
				if sec != "file" || f.Type.Kind() != reflect.String || !strings.HasSuffix(name, "_file") {
					*errs = append(*errs, fmt.Errorf("%s: secret:%q needs a string field whose yaml name ends in _file", where, sec))
					continue
				}
				k.Kind = KindSecretFileOnly
			}
			if !supportedType(f.Type) {
				*errs = append(*errs, fmt.Errorf("%s: unsupported type %s", where, f.Type))
				continue
			}
			if def, has := f.Tag.Lookup("default"); has {
				k.Default, k.HasDefault = def, true
				if _, err := parseText(f.Type, def); err != nil {
					*errs = append(*errs, fmt.Errorf("%s: default %q: %w", where, def, err))
				}
			}
			if v := f.Tag.Get("validate"); v != "" {
				rules, req, err := parseRules(f.Type, v)
				if err != nil {
					*errs = append(*errs, fmt.Errorf("%s: validate %q: %w", where, v, err))
					continue
				}
				k.Rules, k.Required = rules, req
			}
			s.addKey(k, where, errs)
		}
	}
}

func (s *Schema) addKey(k Key, where string, errs *[]error) {
	k.Env = envName(k.Path)
	if j, dup := s.byEnv[k.Env]; dup {
		*errs = append(*errs, fmt.Errorf("%s: keys %q and %q derive the same environment variable %s", where, s.Keys[j].Path, k.Path, k.Env))
		return
	}
	if _, dup := s.byPath[k.Path]; dup {
		*errs = append(*errs, fmt.Errorf("%s: key %q is defined twice", where, k.Path))
		return
	}
	s.byPath[k.Path] = len(s.Keys)
	s.byEnv[k.Env] = len(s.Keys)
	s.Keys = append(s.Keys, k)
}

// ByPath returns the key with the given dotted path.
func (s *Schema) ByPath(path string) (Key, bool) {
	i, ok := s.byPath[path]
	if !ok {
		return Key{}, false
	}
	return s.Keys[i], true
}

// ByEnv returns the key an environment variable maps to.
func (s *Schema) ByEnv(name string) (Key, bool) {
	i, ok := s.byEnv[name]
	if !ok {
		return Key{}, false
	}
	return s.Keys[i], true
}

// IsSection reports whether path names a struct node.
func (s *Schema) IsSection(path string) bool { return path == "" || s.structs[path] }

func supportedType(t reflect.Type) bool {
	switch t {
	case durTyp, sizeTyp:
		return true
	}
	switch t.Kind() {
	case reflect.String, reflect.Bool, reflect.Int, reflect.Int32, reflect.Int64, reflect.Float64:
		return true
	case reflect.Slice:
		return t.Elem().Kind() == reflect.String
	case reflect.Map:
		return t.Key().Kind() == reflect.String
	}
	return false
}

// Paths returns every key path in sorted order.
func (s *Schema) Paths() []string {
	out := make([]string, 0, len(s.Keys))
	for _, k := range s.Keys {
		out = append(out, k.Path)
	}
	sort.Strings(out)
	return out
}

// isSnakeCase reports whether s is lower-case words joined by single underscores.
func isSnakeCase(s string) bool {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '_' && i > 0 && i < len(s)-1 && s[i-1] != '_':
		default:
			return false
		}
	}
	return true
}
