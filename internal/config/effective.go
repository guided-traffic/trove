package config

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
)

// Entry is one key of the effective configuration.
type Entry struct {
	Key    string `json:"-"`
	Env    string `json:"-"`
	Value  any    `json:"value"`
	Source string `json:"source"`
	Secret bool   `json:"-"` // the key holds a secret value (never shown) or names a secret file
}

// Effective returns every key with its value and source, sorted by key path.
// Secret values are replaced by Redacted when set; the path of a secret file is
// not a secret and is shown.
func (l *Loaded) Effective() []Entry {
	root := l.root
	out := make([]Entry, 0, len(l.Schema.Keys))
	for _, k := range l.Schema.Keys {
		v := root.FieldByIndex(k.Index)
		e := Entry{Key: k.Path, Env: k.Env, Source: l.Sources[k.Path].String(), Secret: k.Kind.IsSecret()}
		if k.Kind == KindSecretValue {
			if v.String() != "" {
				e.Value = Redacted
			} else {
				e.Value = ""
			}
		} else {
			e.Value = displayValue(v)
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// EffectiveJSON renders the redacted view in the shape of
// GET /api/v1/system/config: {"values": {"<key>": {"value": …, "source": …}}}.
func (l *Loaded) EffectiveJSON() ([]byte, error) {
	values := map[string]Entry{}
	for _, e := range l.Effective() {
		values[e.Key] = e
	}
	return json.MarshalIndent(map[string]any{"values": values}, "", "  ")
}

// WriteTable prints one line per key: key, value, source and the environment
// variable that overrides it. This is the output of `trove admin config validate`.
func (l *Loaded) WriteTable(w io.Writer) error {
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "KEY\tVALUE\tSOURCE\tENV")
	for _, e := range l.Effective() {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", e.Key, oneLine(formatValue(e.Value)), e.Source, e.Env)
	}
	return tw.Flush()
}

func oneLine(s string) string { return strings.ReplaceAll(s, "\n", " ") }
