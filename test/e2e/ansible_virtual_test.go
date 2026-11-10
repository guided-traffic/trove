//go:build e2e

package e2e

// The virtual class of the Ansible format with the real clients (R-ANS-032, R-ANS-022 to R-ANS-024): one list of local and remote versions
// through one address, a publish through the deploy target, and a server_list of two servers. The row waits for the virtual class (the
// work package B3 of ansible-work-packages.md): until it registers, the format answers 501 for a virtual repository and there is nothing
// for a client to run against. It is written and listed here so that the set is complete when the class lands, and it is a visible skip
// with its reason, never a silent pass.

import "testing"

func TestAnsibleVirtual(t *testing.T) {
	t.Skip("waiting for the virtual class of the format (B3, R-ANS-022 to R-ANS-024): the rows below are written when it registers")
}
