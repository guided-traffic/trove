package formats

import (
	"bytes"
	"slices"
	"text/template"
)

// The topics of a guide, in the order the API serves the sections (api-and-ui.md
// R-UI-049, R-API-053).
const (
	// GuideWhat says what a repository of the class is and how it behaves.
	GuideWhat = "what"
	// GuideConfigure points the client at the repository.
	GuideConfigure = "configure"
	// GuideAuthenticate says which credential the client sends and where it lives.
	GuideAuthenticate = "authenticate"
	// GuideInstall installs packages through the repository.
	GuideInstall = "install"
	// GuidePublish publishes packages to the repository or through it.
	GuidePublish = "publish"
	// GuideMaintain changes what is published: tags, unpublish, unlist.
	GuideMaintain = "maintain"
	// GuideAudit is the client's vulnerability audit through the repository.
	GuideAudit = "audit"
	// GuideLockfiles is what a lockfile records and what moving it to another
	// repository takes.
	GuideLockfiles = "lockfiles"
	// GuideEndpoint is what changes on an endpoint with exposure rules.
	GuideEndpoint = "endpoint"
	// GuideTroubleshooting maps the answers of the repository to their causes.
	GuideTroubleshooting = "troubleshooting"
)

// GuideTopics returns the topics in serving order.
func GuideTopics() []string {
	return []string{
		GuideWhat, GuideConfigure, GuideAuthenticate, GuideInstall, GuidePublish, GuideMaintain,
		GuideAudit, GuideLockfiles, GuideEndpoint, GuideTroubleshooting,
	}
}

// RequiredGuideTopics are the topics every class of a format with clients covers
// for every client (kit check 8).
func RequiredGuideTopics() []string {
	return []string{GuideWhat, GuideConfigure, GuideAuthenticate, GuideInstall, GuideTroubleshooting}
}

// GuideRepositoryPlaceholder stands for the repository key in the addresses of a
// guide that names no repository (GET /api/v1/formats/{format}/guide).
const GuideRepositoryPlaceholder = "<repository>"

// GuideSection is one section of a format's guide (R-UI-049): what a repository of
// a class is and how a client uses it. The API renders the Markdown with the
// README renderer and sanitizer of R-UI-008 and attaches the setup snippets the
// section names, so the guide shows exactly what the client setup shows.
type GuideSection struct {
	// ID is stable: "npm.remote.what".
	ID string
	// Topic is one of [GuideTopics].
	Topic string
	// Title is the heading of the section, plain text.
	Title string
	// Classes are the classes the section is written for; empty is every class.
	Classes []Class
	// Clients are the client IDs the section is written for; empty is every client.
	Clients []string
	// Snippets is a setup section ("configure", "install", "publish") whose snippets
	// of the reader's client the API attaches below the text; "" attaches none.
	Snippets string
	// Markdown is text/template over [GuideData]. Like a setup template it never
	// receives a token: the one credential it may spell is the reference
	// [SetupTokenRef], which the API rewrites by the reader's credential mode, and a
	// line holding it is dropped for an anonymous reader. A section that renders to
	// white space only is left out.
	Markdown string
}

// GuideData is the only data a guide section sees: the data of the setup
// templates and what the guide adds. It has no credential field.
type GuideData struct {
	SetupData
	// Class is the class of the repository, or the class asked for when the guide
	// names no repository (SetupData.Repository is then "").
	Class Class
	// Client is the client ID the guide is rendered for.
	Client string
	// PublishTarget is the repository a publish through this one reaches when the
	// reader may deploy there; "" otherwise.
	PublishTarget string
	// Anonymous is true for a reader who is not signed in: sections leave out what
	// needs a credential.
	Anonymous bool
	// ExposureRules is true when the endpoint of the request serves under exposure
	// rules (endpoints.md R-EP-012).
	ExposureRules bool
	// TokenEnvVar is ui.client_setup.token_env_var: the name of the variable, not a
	// credential.
	TokenEnvVar string
}

// ExecuteGuide runs the Markdown template of a section over d. A reference to a
// field GuideData does not have fails, so a section cannot reach for a credential
// (kit check 8). The API and the kit both parse through it.
func ExecuteGuide(s GuideSection, d GuideData) (string, error) {
	t, err := template.New(s.ID).Option("missingkey=error").Parse(s.Markdown)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := t.Execute(&b, d); err != nil {
		return "", err
	}
	return b.String(), nil
}

// AppliesTo reports whether the section is written for a class and a client.
func (s GuideSection) AppliesTo(c Class, client string) bool {
	return (len(s.Classes) == 0 || slices.Contains(s.Classes, c)) && (len(s.Clients) == 0 || slices.Contains(s.Clients, client))
}
