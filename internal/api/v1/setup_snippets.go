package v1

import (
	"bytes"
	"fmt"
	"html"
	"regexp"
	"slices"
	"sort"
	"strings"
	"text/template"

	"github.com/guided-traffic/trove/internal/formats"
)

// The client setup snippets of R-API-030. A format contributes templates
// ([formats.SetupTemplate]); this file runs them with the data of the repository and
// the request, rewrites the credential by the mode, substitutes the package and
// quotes whatever it substitutes. The server never sees a token: the credential is
// the literal placeholder, a reference to an environment variable, or absent.

// Credential modes of the client setup (R-API-030).
const (
	credPlaceholder = "placeholder"
	credEnv         = "env"
	credNone        = "none"

	// tokenPlaceholder is what credentials=placeholder puts where the token goes;
	// the browser replaces it (R-UI-016).
	tokenPlaceholder = "{{token}}"
)

// setupInput is everything a snippet is made of.
type setupInput struct {
	// Templates are the format's templates; Client selects the ones of one client.
	Templates []formats.SetupTemplate
	Client    string
	// Credentials is credPlaceholder, credEnv or credNone.
	Credentials string
	// TokenEnvVar is the variable credentials=env refers to.
	TokenEnvVar string
	// Data is what the templates see. RequiresAuth is cleared for credNone, because a
	// snippet without a credential has nothing for an always-auth switch to send.
	Data formats.SetupData
	// Package and Version name what an install snippet installs; Package empty keeps
	// the placeholder.
	Package, Version string
	// Scope is the scope of the scoped configure variant, without its "@"; empty for
	// none.
	Scope string
	// MayPublish says that the caller may deploy to the target; publish templates are
	// rendered only then.
	MayPublish bool
}

// snippet is one rendered snippet.
type snippet struct {
	ID       string
	Tab      string
	FileName string
	Language string
	Content  string
}

// setupClients returns the clients the templates serve, sorted.
func setupClients(ts []formats.SetupTemplate) []string {
	var out []string
	for _, t := range ts {
		if !slices.Contains(out, t.Client) {
			out = append(out, t.Client)
		}
	}
	sort.Strings(out)
	return out
}

// snippetID derives the stable id of the API from a template: a template ID is
// "<client>.<name>.<section>" or "<client>.<section>", the snippet id is
// "<client>.<section>[.<name>]" (Q-API-160), with "-scoped" appended to the name of
// the scoped variant of a configure snippet.
func snippetID(t formats.SetupTemplate, scoped bool) string {
	parts := strings.Split(t.ID, ".")
	id := t.Client + "." + t.Section
	if len(parts) >= 3 {
		name := strings.Join(parts[1:len(parts)-1], ".")
		if scoped {
			name += "-scoped"
		}
		id += "." + name
	} else if scoped {
		id += ".scoped"
	}
	return id
}

// renderSnippets runs the templates of one client. Install snippets are kept for
// every caller, configure snippets too, publish snippets only for a caller who may
// publish. With a scope the configure snippets come twice: the default registry and
// the scoped one.
func renderSnippets(in setupInput) ([]snippet, error) {
	data := in.Data
	if in.Credentials == credNone {
		data.RequiresAuth = false
	}
	var out []snippet
	for _, t := range in.Templates {
		if t.Client != in.Client {
			continue
		}
		if t.Section == "publish" && !in.MayPublish {
			continue
		}
		variants := []string{""}
		if t.Section == "configure" && in.Scope != "" {
			variants = append(variants, in.Scope)
		}
		for _, scope := range variants {
			d := data
			d.Scope = scope
			content, err := runTemplate(t, d)
			if err != nil {
				return nil, fmt.Errorf("setup template %s: %w", t.ID, err)
			}
			content = applyCredentials(content, in)
			if t.Section == "install" {
				content = substitutePackage(content, t.Lang, in.Package, in.Version)
			}
			s := snippet{ID: snippetID(t, scope != ""), Tab: t.Section, FileName: t.File, Language: t.Lang, Content: content}
			out = append(out, s)
		}
	}
	return out, nil
}

// runTemplate executes a template over the data with the quoting functions. A
// reference to a field the data does not have fails, so a template cannot reach for a
// credential (kit check 8).
func runTemplate(t formats.SetupTemplate, d formats.SetupData) (string, error) {
	tm, err := template.New(t.ID).Option("missingkey=error").Funcs(template.FuncMap{
		"shell": shellQuote,
		"xml":   html.EscapeString,
	}).Parse(t.Template)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := tm.Execute(&b, d); err != nil {
		return "", err
	}
	return b.String(), nil
}

// applyCredentials rewrites the token reference of a rendered snippet by the
// credential mode.
func applyCredentials(content string, in setupInput) string {
	switch in.Credentials {
	case credEnv:
		return strings.ReplaceAll(content, formats.SetupTokenRef, "${"+in.TokenEnvVar+"}")
	case credNone:
		lines := strings.SplitAfter(content, "\n")
		kept := lines[:0]
		for _, l := range lines {
			if !strings.Contains(l, formats.SetupTokenRef) {
				kept = append(kept, l)
			}
		}
		return strings.Join(kept, "")
	}
	return strings.ReplaceAll(content, formats.SetupTokenRef, tokenPlaceholder)
}

// substitutePackage puts the package of an install snippet in place of the
// placeholder, quoted for the language of the snippet.
func substitutePackage(content, lang, pkg, version string) string {
	if pkg == "" {
		return content
	}
	spec := pkg
	if version != "" {
		spec += "@" + version
	}
	switch lang {
	case "shell":
		spec = shellQuote(spec)
	case "xml":
		spec = html.EscapeString(spec)
	}
	return strings.ReplaceAll(content, formats.SetupPackageRef, spec)
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9@/._+:=][A-Za-z0-9@/._+:=-]*$`)

// shellQuote quotes a value for a POSIX shell command (R-API-030): a value of safe
// characters stays as it is, every other value is single-quoted with an embedded
// single quote written as '\”. Legacy npm names can hold ', (, ) , ! and *, which a
// shell interprets when a user pastes the command. A value that starts with "-" is
// quoted as well, so it never reads as a bare option in a snippet; that the client
// does not take it for one is the caller's to ensure (the package and the version
// of the client setup are refused, Q-NPM-50).
func shellQuote(s string) string {
	if shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

var tokenShape = regexp.MustCompile(`trv_(pat|sat|cit)_`)

// holdsTokenShape reports whether s looks like one of the tokens of R-AUTH-001. No
// answer of the client setup may; the same check runs over the input, so that a
// request cannot make the server echo a token it was handed, and over the output.
func holdsTokenShape(s string) bool { return tokenShape.MatchString(s) }
