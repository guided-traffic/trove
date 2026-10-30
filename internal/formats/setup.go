package formats

// Client is a native client of a format and the majors tested against it
// (FMT-002, FMT-003).
type Client struct {
	// ID is "npm", "pnpm", "yarn", "yarn-classic".
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
	// Lang is "shell", "ini", "xml" or "yaml".
	Lang string
	// File is the target file when the snippet is file content: ".npmrc".
	File string
	// Template is text/template over [SetupData]; it never receives a token
	// (R-API-030). The functions shell and xml quote a value for a shell command
	// and for an XML text or attribute (R-API-030); a value of SetupData that a
	// template places in such a context goes through them.
	//
	// The credential is the one literal [SetupTokenRef], and the placeholder of
	// the package to install is [SetupPackageRef]. The API rewrites both after the
	// template ran, by the credential mode of the request and by the package asked
	// for, so that no template implements the three modes or the quoting.
	Template string
}

// SetupTokenRef is how a setup template spells the reference to the token, exactly:
// the environment variable the client expands itself. The API replaces it by the
// reference of the configured variable (credentials=env), by the placeholder the
// browser fills in (credentials=placeholder), or drops the line that holds it
// (credentials=none).
const SetupTokenRef = "${TROVE_TOKEN}" //nolint:gosec // a reference to an environment variable, not a credential

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
	// Scope is the scope a snippet binds to the repository without its "@"
	// ("acme"), "" for the default registry of the client.
	Scope    string
	Username string
	// RegistryKey is RegistryURL without its scheme, "//trove.example.com/api/npm/npm-all/":
	// the key an .npmrc credential line is bound to, so that the client sends the
	// credential to this registry and to no other URL (R-NPM-041). It is a key, not
	// a credential.
	RegistryKey string
	// RequiresAuth is true when the repository is not anonymously readable: a
	// snippet then makes the client send its credential without waiting for a
	// challenge (always-auth, npmAlwaysAuth).
	RequiresAuth bool
}
