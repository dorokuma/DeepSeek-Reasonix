// Command reasonix is the Reasonix CLI (chat, run, serve, setup, mcp, doctor).
package main

import (
	"os"

	"reasonix/internal/cli"
)

// version is set at link time: -ldflags "-X main.version=..."
var version = "dev"

func main() {
	os.Exit(cli.Run(os.Args[1:], version))
}
