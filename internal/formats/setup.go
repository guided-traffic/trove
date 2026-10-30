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
	// (R-API-030).
	Template string
}

// SetupData is the only data a setup template sees. It has no credential field,
// so a template that names one fails to execute (kit check 8).
type SetupData struct {
	RegistryURL string
	Repository  string
	Endpoint    string
	Scope       string
	Username    string
}
