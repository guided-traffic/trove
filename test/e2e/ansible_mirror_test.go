//go:build e2e

package e2e

// The mirror mode of the Ansible remote with the real clients (R-ANS-032, R-ANS-021): a pass keeps the collections the patterns select, and
// installs after it cost the upstream no request. The row waits for the mirror (the work package B2 of ansible-work-packages.md builds it
// after the pull-through class): it is written and listed here so that the set is complete when the mirror lands, and it is a visible skip
// with its reason, never a silent pass.

import "testing"

func TestAnsibleMirror(t *testing.T) {
	t.Skip("waiting for the mirror of the remote class (B2, R-ANS-021): the rows below are written when it lands")
}
