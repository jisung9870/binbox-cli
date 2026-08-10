package main

import (
	"fmt"
	"os"

	"github.com/binbox/bb/internal/bb"
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
