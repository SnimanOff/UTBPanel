// Command execbot stands in for `core exec-bot` in runner tests.
package main

import (
	"fmt"
	"os"

	"github.com/SnimanOff/UTBPanel/backend/internal/runner"
)

func main() {
	if err := runner.ExecBot(os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
