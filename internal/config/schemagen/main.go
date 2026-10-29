// Command schemagen writes the JSON Schema of the configuration tree. It is run
// by `go generate ./internal/config`.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/guided-traffic/trove/internal/config"
)

func main() {
	out := flag.String("out", "schema.json", "output file")
	flag.Parse()
	b, err := config.MustDescribe().JSONSchema()
	if err != nil {
		fmt.Fprintln(os.Stderr, "schemagen:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, append(b, '\n'), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "schemagen:", err)
		os.Exit(1)
	}
}
