package nuget

import "github.com/guided-traffic/trove/internal/formats"

// clients are the native clients of the acceptance matrix (R-NUGET-026, R-NUGET-027): the
// .NET SDK, nuget.exe and Visual Studio. Visual Studio reads the same nuget.config as the
// other two and has no command line, so its templates are the configuration file and the
// PackageReference of a project.
func clients() []formats.Client {
	return []formats.Client{
		{ID: ClientDotnet, DisplayName: ".NET CLI (dotnet)", Versions: []string{"8.0", "9.0", "10.0"}},
		{ID: ClientNuGet, DisplayName: "nuget.exe", Versions: []string{"6.8"}},
		{ID: ClientVisualStudio, DisplayName: "Visual Studio 2022", Versions: []string{"17.8"}},
	}
}

// The client IDs.
const (
	ClientDotnet       = "dotnet"
	ClientNuGet        = "nuget"
	ClientVisualStudio = "visual-studio"
)

// sourceURL is the expression for the address of the service index of the repository the snippet is rendered
// for: RegistryURL has its trailing slash (R-NUGET-026: the instructions always use the
// index.json URL, never a v2 address).
const sourceURL = `(printf "%sv3/index.json" .RegistryURL)`

// SetupTemplates are the client setup snippets of R-NUGET-026 and R-UI-018 (Q-NUGET-49):
// how to point a client at the repository (configure), how to install a package through it
// (install) and how to push to it (publish; the API renders it for a caller who holds deploy on
// the publish target). A publish snippet names the repository the caller chose, not the
// deployment target of a virtual one: the virtual repository forwards the push. The dialog that
// shows them and the token it mints are the API's (R-API-033); a template sees
// [formats.SetupData] and never a token, so the credential is always a reference to the
// environment variable that the client or the shell expands itself, in the spelling of the
// syntax it is written in: %TROVE_TOKEN% in a nuget.config and in cmd, ${TROVE_TOKEN} in a
// POSIX shell, $env:TROVE_TOKEN in PowerShell.
//
// The credential of a configure snippet is written only for a repository that is not
// anonymously readable (SetupData.RequiresAuth): a client with a credential for a source that
// needs none would send it to the first request of every restore for nothing, and the API
// clears RequiresAuth for a snippet without credentials, so the whole credential part goes and
// what is left is a command or a file that works. A push carries its token as the API key,
// which is the credential of the publish resource (R-NUGET-012).
func (f *Format) SetupTemplates() []formats.SetupTemplate {
	// The server never sees the user name (R-NUGET-012: any name is accepted); the instructions
	// use a fixed one.
	const user = "trove"
	nugetConfig := `<?xml version="1.0" encoding="utf-8"?>
<configuration>
  <packageSources>
    <clear />
    <add key="trove" value="{{xml ` + sourceURL + `}}" protocolVersion="3" />
  </packageSources>
{{if .RequiresAuth}}  <packageSourceCredentials>
    <trove>
      <add key="Username" value="` + user + `" />
      <add key="ClearTextPassword" value="` + formats.SetupTokenRefWin + `" />
    </trove>
  </packageSourceCredentials>
{{end}}</configuration>
`
	dotnetShell := `dotnet nuget add source {{shell ` + sourceURL + `}} --name trove{{if .RequiresAuth}} \
  --username ` + user + ` --password "` + formats.SetupTokenRef + `" --store-password-in-clear-text{{end}}
`
	dotnetPS := "dotnet nuget add source {{powershell " + sourceURL + "}} --name trove{{if .RequiresAuth}} `\n" +
		"  --username " + user + ` --password "` + formats.SetupTokenRefPS + `" --store-password-in-clear-text{{end}}` + "\n"
	nugetCmd := `nuget sources add -Name trove -Source {{cmd ` + sourceURL + `}}{{if .RequiresAuth}} ^
  -Username ` + user + ` -Password ` + formats.SetupTokenRefWin + `{{end}}
`
	nugetPS := "nuget sources add -Name trove -Source {{powershell " + sourceURL + "}}{{if .RequiresAuth}} `\n" +
		"  -Username " + user + ` -Password "` + formats.SetupTokenRefPS + `"{{end}}` + "\n"
	const dotnetPush = "dotnet nuget push bin/Release/Acme.Widgets.1.2.3.nupkg --source "
	const nugetPush = "nuget push Acme.Widgets.1.2.3.nupkg -Source "
	return []formats.SetupTemplate{
		{ID: "dotnet.shell.configure", Client: ClientDotnet, Section: "configure", Lang: "shell", Template: dotnetShell},
		{ID: "dotnet.powershell.configure", Client: ClientDotnet, Section: "configure", Lang: "powershell", Template: dotnetPS},
		{ID: "dotnet.nugetconfig.configure", Client: ClientDotnet, Section: "configure", Lang: "xml", File: "nuget.config", Template: nugetConfig},
		{ID: "nuget.cmd.configure", Client: ClientNuGet, Section: "configure", Lang: "cmd", Template: nugetCmd},
		{ID: "nuget.powershell.configure", Client: ClientNuGet, Section: "configure", Lang: "powershell", Template: nugetPS},
		{ID: "nuget.nugetconfig.configure", Client: ClientNuGet, Section: "configure", Lang: "xml", File: "nuget.config", Template: nugetConfig},
		{ID: "visual-studio.nugetconfig.configure", Client: ClientVisualStudio, Section: "configure", Lang: "xml", File: "nuget.config", Template: nugetConfig},

		{ID: "dotnet.install", Client: ClientDotnet, Section: "install", Lang: "shell", Template: "dotnet add package <package>\n", VersionSpec: " --version " + formats.SetupVersionRef},
		{ID: "nuget.install", Client: ClientNuGet, Section: "install", Lang: "cmd", Template: "nuget install <package> -Source trove -OutputDirectory packages\n", VersionSpec: " -Version " + formats.SetupVersionRef},
		// The version attribute closes the Include attribute: the API replaces <package> by the
		// quoted package followed by the spec, so the spec starts with the closing quote.
		{ID: "visual-studio.packagereference.install", Client: ClientVisualStudio, Section: "install", Lang: "xml", Template: "<PackageReference Include=\"<package>\" />\n", VersionSpec: "\" Version=\"" + formats.SetupVersionRef},

		{
			ID: "dotnet.shell.publish", Client: ClientDotnet, Section: "publish", Lang: "shell",
			Template: dotnetPush + `{{shell ` + sourceURL + `}} --api-key "` + formats.SetupTokenRef + `"` + "\n",
		},
		{
			ID: "dotnet.powershell.publish", Client: ClientDotnet, Section: "publish", Lang: "powershell",
			Template: dotnetPush + `{{powershell ` + sourceURL + `}} --api-key "` + formats.SetupTokenRefPS + `"` + "\n",
		},
		{
			ID: "nuget.cmd.publish", Client: ClientNuGet, Section: "publish", Lang: "cmd",
			Template: nugetPush + `{{cmd ` + sourceURL + `}} -ApiKey ` + formats.SetupTokenRefWin + "\n",
		},
		{
			ID: "nuget.powershell.publish", Client: ClientNuGet, Section: "publish", Lang: "powershell",
			Template: nugetPush + `{{powershell ` + sourceURL + `}} -ApiKey "` + formats.SetupTokenRefPS + `"` + "\n",
		},
	}
}
