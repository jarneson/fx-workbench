package main

import (
	"os"

	"github.com/jarneson/fx-workbench/internal/appcli"
)

func main() {
	args := append([]string{"principle"}, os.Args[1:]...)
	code := appcli.Run(args, os.Stdout, os.Stderr)
	if code < 0 {
		code = 0
	}
	os.Exit(code)
}
