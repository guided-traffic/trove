package formattest_test

import "github.com/guided-traffic/trove/internal/qname"

// qr is the name of a repository key in the project the unit tests use.
func qr(key string) qname.Repo { return qname.Repo{Project: "it", Key: key} }
