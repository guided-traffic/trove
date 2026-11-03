// Command bsttoken prints a syntactically valid Bastion token (trv_bst_, R-AUTH-022) on its standard output.
// It is a tool for running the role bastion locally (docs/developer/bastion.md): the token is not known to any
// main installation, so the Bastion starts, reads it and reports the main installation unreachable or refuses
// it, which is what a local run of the skeleton needs. A real token is issued by an administrator of the main
// installation (R-BAS-004).
package main

import (
	"fmt"
	"os"

	"github.com/guided-traffic/trove/internal/auth/token"
)

func main() {
	t, err := token.Generate(token.TypeBST)
	if err != nil {
		fmt.Fprintln(os.Stderr, "bsttoken:", err)
		os.Exit(1)
	}
	fmt.Println(t)
}
