package proxy

import "github.com/guided-traffic/trove/internal/qname"

// qnameRepo is the name of a repository in a project.
func qnameRepo(project, key string) qname.Repo { return qname.Repo{Project: project, Key: key} }

// qr is the name of a repository key in the project the unit tests use.
func qr(key string) qname.Repo { return qname.Repo{Project: "it", Key: key} }
