package webui

import (
	"sort"
	"strconv"
	"strings"
)

// negotiate picks the locale for an Accept-Language header value out of the served locales. A
// tag matches a locale exactly (case-insensitive) or through its primary subtag, so "de-DE"
// selects "de". It returns "" when no listed tag matches.
func negotiate(header string, locales []string) string {
	type entry struct {
		tag string
		q   float64
	}
	var entries []entry
	for _, part := range strings.Split(header, ",") {
		tag, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag == "" || tag == "*" {
			continue
		}
		q := 1.0
		if v, ok := strings.CutPrefix(strings.ReplaceAll(strings.TrimSpace(params), " ", ""), "q="); ok {
			parsed, err := strconv.ParseFloat(v, 64)
			if err != nil {
				continue
			}
			q = parsed
		}
		if q <= 0 {
			continue
		}
		entries = append(entries, entry{tag, q})
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].q > entries[j].q })

	for _, e := range entries {
		for _, l := range locales {
			if strings.EqualFold(l, e.tag) {
				return l
			}
		}
		primary, _, _ := strings.Cut(e.tag, "-")
		for _, l := range locales {
			if strings.EqualFold(l, primary) {
				return l
			}
		}
	}
	return ""
}
