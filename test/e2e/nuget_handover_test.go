//go:build e2e

package e2e

// The NuGet cases that wait for the remote and the virtual repositories (work packages W2-RM and W2-VI of phase 4).
// These are not skipped tests: nothing here runs, and nothing reports a pass for them. Each line names the case, the
// requirement and the harness it needs, so the package that lands the format part adds the test in a file of its own
// (nuget_remote_test.go, nuget_virtual_test.go) with the helpers of nuget_env_test.go and nuget_clients_test.go.
//
// Needs the counting NuGet test upstream (TROVE_E2E_NUGETSTUB=1, set by `deploy/dev/e2e.sh nuget`):
//
//	TODO(remote) R-NUGET-014..017, E3.1: dotnet restore through a remote repository of the test upstream: the first request
//	  fills, the second is served from the cache (the stub's counters per host and path), the checksum is verified;
//	  TROVE_E2E_NUGETSTUB_HOST and TROVE_E2E_NUGETSTUB_CONTENT_HOST name the two hosts of the service index.
//	TODO(remote) upstream failure: the stub in its down and delay modes; restores of cached packages keep working.
//	TODO(virtual) R-NUGET-019, R-NUGET-027 drain load: dotnet nuget push through nuget-all lands in the deployment target;
//	  restores through nuget-all of local packages and of remote packages not yet cached.
//	TODO(virtual) R-NUGET-010 search through nuget-all: the forwarded search of the remote member, cached per replica (Q-NUGET-8).
//	TODO(e3.2) E3.2: three clients run `dotnet restore --no-cache` at once, each against another replica (pinClient of
//	  npm_replicas_test.go), the reference solution of perf/ (100 packages) through nuget-all; all succeed, and the stub
//	  records one request per .nupkg.
//	TODO(e3.6) E3.6: a `dotnet nuget push` whose answer a fault-injecting edge drops (dropOneAnswer of npm_replicas_test.go)
//	  is repeated by the client and exits 0 (the identical push answers 201, Q-NUGET-6); needs the forwarder of the clients
//	  pod to drop the answer of one PUT.
//	TODO(e3.7, symbols) the customer endpoint (W2-EX) and the symbol packages (W2-SY) have tests of their own in their own files.
