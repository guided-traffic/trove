package main

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// TestR_BAS_004_TheBastionCommandsRefuseWhatIsIncomplete: the three commands of R-BAS-004 print their usage and
// touch no database when the name or the token ID is missing or the command is unknown (the work of the
// commands is shown against a database in test/integration/bastionreg).
func TestR_BAS_004_TheBastionCommandsRefuseWhatIsIncomplete(t *testing.T) {
	s := streams{stdin: strings.NewReader(""), stdout: io.Discard, stderr: io.Discard}
	for _, args := range [][]string{
		{"admin", "bastion"},
		{"admin", "bastion", "create"},
		{"admin", "bastion", "frobnicate"},
		{"admin", "bastion", "token"},
		{"admin", "bastion", "token", "create"},
		{"admin", "bastion", "token", "revoke"},
		{"admin", "bastion", "token", "revoke", "partners"},
		{"admin", "bastion", "token", "list", "partners"},
	} {
		err := adminCmd(args[1:], s)
		var u usageError
		if !errors.As(err, &u) || !strings.Contains(u.Error(), "trove admin bastion") {
			t.Errorf("%v: err = %v, want the usage of trove admin bastion", args, err)
		}
	}
	if !strings.Contains(adminUsage, "bastion token revoke") {
		t.Error("the usage of trove admin does not list the Bastion commands")
	}
}
