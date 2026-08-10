package main

import (
	"fmt"
	"os"

	"github.com/jisung9870/binbox-cli/internal/bb"
)

func main() {
	app := bb.New(os.Stdout, os.Stderr, os.Environ())
	if err := app.Run(os.Args[1:]); err != nil {
		if !bb.Reported(err) {
			fmt.Fprintln(os.Stderr, "bb:", err)
		}
		os.Exit(bb.ExitCode(err))
	}
}
