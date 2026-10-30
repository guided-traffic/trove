package repository

import "github.com/guided-traffic/trove/internal/secrets"

// RepositorySecrets is the sealed column of the stored secret fields of a
// repository (R-REPO-033, R-ARCH-021): the password of a remote repository's
// upstream credential and the other secret members of a repository document. The
// primary key is (repo_id, field), where field is the JSON pointer of the member,
// so a sealed value copied to another repository or another member does not open
// (the row key is part of the AAD).
var RepositorySecrets = secrets.Register("repository_secrets", "repo_id,field", "sealed")
