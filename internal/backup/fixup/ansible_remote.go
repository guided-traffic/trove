package fixup

import (
	"context"
	"errors"

	"github.com/guided-traffic/trove/internal/formatdata/ansible"
	"github.com/guided-traffic/trove/internal/meta"
)

// The Ansible Galaxy purger (R-BAK-008, R-ANS-029): after a restore from a backup that left the remote caches out, it empties
// every Ansible remote repository's cache through the storage delete path and drops the repository's cache entries and the digests
// recorded in ansible_upstream_versions. Local and virtual repositories are not touched.
func init() {
	RegisterPurger(CachePurger{
		Name: "ansible-remote",
		Purge: func(ctx context.Context, db meta.Runner, env Env) error {
			if env.Items == nil {
				return errors.New("fixup: the ansible purger needs the storage delete path (Env.Items)")
			}
			return ansible.PurgeRemoteCaches(ctx, db, env.Items.EraseCache)
		},
	})
}
