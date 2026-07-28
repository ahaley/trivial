// Command trivial is the single binary that serves the web application and
// drives it from the terminal (SPEC.md §11).
package main

import (
	"fmt"
	"os"

	"github.com/ahaley/trivial/internal/cli"
)

// version is overridable at build time:
//
//	go build -ldflags "-X main.version=1.0.0" ./cmd/trivial
var version = "dev"

func main() {
	if err := cli.Execute(version); err != nil {
		fmt.Fprintln(os.Stderr, "trivial:", err)
		os.Exit(1)
	}
}
