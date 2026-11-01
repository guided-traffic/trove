{{if eq .Client "dotnet"}}A package source is a name and the address of a service index. Always use the address of the snippet, which ends in `v3/index.json`: trove does not serve NuGet v2 addresses, and a source without `.json` at its end is read as v2 by older clients unless it says `protocolVersion="3"`.

There are two ways to configure the .NET CLI. The command adds the source to your user-level `NuGet.Config`, for every project on this machine. The `nuget.config` file belongs next to your solution file, applies to that solution only and can be committed, because it holds a reference to the credential and not the credential itself.{{if not .Anonymous}} On Linux and macOS NuGet cannot encrypt a stored password, so the command needs `--store-password-in-clear-text`, and the token your shell expands lands in the user-level file in clear text. NuGet expands `%{{.TokenEnvVar}}%` in a `nuget.config` from the environment on every operating system, so prefer the file for anything shared.{{end}}
{{else if eq .Client "nuget"}}A package source is a name and the address of a service index. Always use the address of the snippet, which ends in `v3/index.json`: trove does not serve NuGet v2 addresses.

`nuget sources add` writes the source into your user-level `NuGet.Config`; the `nuget.config` file belongs next to your solution file, applies to that solution only and can be committed, because it holds a reference to the credential and not the credential itself.{{if not .Anonymous}} On Windows nuget.exe encrypts the stored password. On Mono, Linux and macOS it cannot, and `nuget sources add` needs `-StorePasswordInClearText`. NuGet expands `%{{.TokenEnvVar}}%` in a `nuget.config` from the environment on every operating system, so prefer the file for anything shared.{{end}} nuget.exe 6.8 and later is tested; versions down to 4.3 work too, but ask for a credential on every resource before they send it.
{{else}}Visual Studio 2022 (17.8 and later) reads the same `nuget.config` as the command line tools, so the file is the way to configure it: save the snippet as `nuget.config` in the folder of your solution file and reopen the solution.{{if not .Anonymous}} NuGet expands `%{{.TokenEnvVar}}%` in the file from the environment of the Visual Studio process, so set the variable before Visual Studio starts.{{end}}

To add the source by hand instead, open Tools, NuGet Package Manager, Package Manager Settings, Package Sources, add a source and give it a name and the address of the snippet, which ends in `v3/index.json`. {{if .Anonymous}}A repository that is open to anonymous readers needs no more.{{else}}At the credential prompt enter any user name and the token as the password; Visual Studio keeps it in the Windows Credential Manager.{{end}}
{{end}}{{if .Anonymous}}
You are not signed in, so the snippets carry no credential. They work for repositories that are open to anonymous readers.
{{end}}
### One source, and no other

`<clear />` in the `nuget.config` removes every source the machine would otherwise add, nuget.org among them. {{if eq .Class "virtual"}}With a virtual repository as the only source, public packages come through trove as well, and a package of the same ID on a public feed cannot slip in.{{else}}If your solution needs public packages too, they have to come from somewhere: use a virtual repository that has a remote repository for nuget.org among its members, and make it the only source.{{end}} Two sources with overlapping IDs are what dependency confusion needs: NuGet takes the highest version from any source.

Where several sources are unavoidable, keep `<clear />`, add them all and say which source may serve which IDs with package source mapping. A source then serves only the IDs that match its patterns, and an ID that matches none cannot be restored:

```xml
<packageSourceMapping>
  <packageSource key="trove">
    <package pattern="Acme.*" />
  </packageSource>
  <packageSource key="nuget.org">
    <package pattern="*" />
  </packageSource>
</packageSourceMapping>
```

The most specific pattern wins, so `Acme.*` goes to trove only, although `*` also matches it. Put the element next to `packageSources`, and use the source names you gave the sources. trove's own protection against confusion (namespace claims, pin rules and the shadowing check) works on the server and does not depend on your files; the mapping protects clients that also use other servers.

The address of {{if .Repository}}`{{.Repository}}`{{else}}a repository{{end}} is `{{.RegistryURL}}v3/index.json`.
