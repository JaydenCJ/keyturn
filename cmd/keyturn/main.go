// Command keyturn is the entry point: everything real lives in
// internal/cli so the whole surface is testable in-process.
package main

import (
	"os"

	"github.com/JaydenCJ/keyturn/internal/cli"
)

func main() {
	app := &cli.App{Stdout: os.Stdout, Stderr: os.Stderr}
	os.Exit(app.Run(os.Args[1:]))
}
