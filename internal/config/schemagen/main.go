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
	out := flag.String("out", "schema.json", "output file of the main configuration tree")
	bastionOut := flag.String("bastion-out", "", "output file of the role bastion's tree (empty: not written)")
	flag.Parse()
	write(*out, config.MustDescribe().JSONSchema)
	if *bastionOut != "" {
		write(*bastionOut, config.BastionDescribe().BastionJSONSchema)
	}
}

func write(path string, render func() ([]byte, error)) {
	b, err := render()
	if err != nil {
		fmt.Fprintln(os.Stderr, "schemagen:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "schemagen:", err)
		os.Exit(1)
	}
}
