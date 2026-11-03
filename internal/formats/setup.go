package formats

// Client is a native client of a format and the majors tested against it
// (FMT-002, FMT-003).
type Client struct {
	// ID is "npm", "pnpm", "yarn", "yarn-classic", "dotnet", "nuget".
	ID          string
	DisplayName string
	// Versions are the tested majors (R-NPM-045).
	Versions []string
}

// SetupTemplate is one client setup snippet (api-and-ui.md R-API-033).
type SetupTemplate struct {
	// ID is stable: "npm.npmrc.configure".
	ID     string
	Client string
	// Section is "configure", "publish" or "install".
	Section string
	// Lang is "shell" (a POSIX shell), "powershell", "cmd", "ini", "xml" or "yaml".
	// It fixes how the API quotes the package and the version it substitutes into an
	// install template.
	Lang string
	// File is the target file when the snippet is file content: ".npmrc".
	File string
	// Template is text/template over [SetupData]; it never receives a token
	// (R-API-030). The functions shell, powershell, cmd and xml quote a value for a
	// command of that shell and for an XML text or attribute (R-API-030); a value of
	// SetupData that a template places in such a context goes through them. The
	// function schemeless drops the scheme of a URL ("https://host/p/" becomes
	// "//host/p/"): the form in which npm and pnpm bind a credential to a registry
	// (R-NPM-041), so a template derives that key from RegistryURL and the core
	// carries no field for it.
	//
	// The credential is one of the literals [SetupTokenRef], [SetupTokenRefWin] and
	// [SetupTokenRefPS], the one the syntax of the file or shell the template writes
	// expands, and the placeholder of the package to install is [SetupPackageRef].
	// The API rewrites them after the template ran, by the credential mode of the
	// request and by the package asked for, so that no template implements the three
	// modes or the quoting.
	Template string
	// VersionSpec is how an install template attaches the version to the package when
	// the request names one: the text that replaces [SetupPackageRef] after the quoted
	// package, with [SetupVersionRef] standing for the quoted version. "" is
	// "@<version>" (npm: pkg@1.2.3); dotnet writes " --version <version>". A request
	// that names no version leaves the package alone. VersionSpec is meaningful for an
	// install template only (kit check 8).
	VersionSpec string
}

// SetupTokenRef is how a setup template spells the reference to the token, exactly:
// the environment variable the client expands itself. The API replaces it by the
// reference of the configured variable (credentials=env), by the placeholder the
// browser fills in (credentials=placeholder), or drops the line that holds it
// (credentials=none).
const SetupTokenRef = "${TROVE_TOKEN}" //nolint:gosec // a reference to an environment variable, not a credential

// SetupTokenRefWin is the reference to the token where the syntax is that of Windows:
// a NuGet.Config value, which NuGet expands itself, and a cmd command. The API
// rewrites it like [SetupTokenRef], to "%<variable>%" for credentials=env.
const SetupTokenRefWin = "%TROVE_TOKEN%" //nolint:gosec // a reference to an environment variable, not a credential

// SetupTokenRefPS is the reference to the token in a PowerShell command. The API
// rewrites it like [SetupTokenRef], to "$env:<variable>" for credentials=env.
const SetupTokenRefPS = "$env:TROVE_TOKEN" //nolint:gosec // a reference to an environment variable, not a credential

// SetupTokenRefs are all the spellings of the token reference, in the order the API
// looks for them. A template spells exactly one per credential line.
func SetupTokenRefs() []string {
	return []string{SetupTokenRef, SetupTokenRefWin, SetupTokenRefPS}
}

// SetupVersionRef stands for the version in [SetupTemplate.VersionSpec].
const SetupVersionRef = "<version>"

// SetupPackageRef is how an install template spells the package to install. The
// API replaces it by the quoted name and version when the request names a package,
// and leaves it in place otherwise.
const SetupPackageRef = "<package>"

// SetupData is the only data a setup template sees. It has no credential field,
// so a template that names one fails to execute (kit check 8).
type SetupData struct {
	// RegistryURL is the registry URL of the repository, with its trailing slash.
	RegistryURL string
	Repository  string
	Endpoint    string
	// Variant names the variant of a configure snippet the request asked for, in the
	// words of the format: for npm the scope without its "@" ("acme"). "" is the
	// default variant, the one that binds the whole client to the registry.
	Variant  string
	Username string
	// RequiresAuth is true when the repository is not anonymously readable: a
	// snippet then makes the client send its credential without waiting for a
	// challenge (always-auth, npmAlwaysAuth).
	RequiresAuth bool
	// Bastion is true when the snippets are for a trove Bastion (R-BAS-007): a read-only
	// cache in front of the installation, which a lockfile written against another host
	// must resolve through. A configure template may add what the client needs for that.
	Bastion bool
}
