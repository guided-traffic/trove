package testenv

import (
	"strings"

	"github.com/guided-traffic/trove/internal/qname"
)

// DefaultProject is the project the stack's seed creates and [Stack.Repository] puts repositories in:
// every repository belongs to a project (Q-AUTHZ-206) and is addressed inside it (Q-REPO-212).
const DefaultProject = "it"

// splitName reads "<project>/<key>" or a bare key, which belongs to [DefaultProject].
func splitName(name string) (project, key string) {
	if p, k, ok := strings.Cut(name, "/"); ok {
		return p, k
	}
	return DefaultProject, name
}

// ProtocolURL is the path of a repository's protocol endpoint, "/api/<format>/<project>/<key>/", to
// append to a replica's base URL.
func ProtocolURL(format, project, key string) string {
	return qname.ProtocolBase("/api/"+format+"/{project}/{key}/", qname.Repo{Project: project, Key: key})
}

// FilesURL is the path of a repository's raw files, "/files/<project>/<key>/", to append to a replica's
// base URL.
func FilesURL(project, key string) string {
	return qname.FilesBase(qname.Repo{Project: project, Key: key})
}

// ManagementURL is the path of a repository below the management API,
// "/api/v1/projects/<project>/repositories/<key>", without a trailing slash.
func ManagementURL(project, key string) string {
	return "/api/v1/projects/" + project + "/repositories/" + key
}
