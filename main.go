package main

import (
	"os"

	"github.com/hoophq/julius/internal/cli"
)

// version is set at release time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(cli.Execute(version))
}
