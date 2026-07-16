package main

import (
	"os"

	"reasonix/internal/cli"

	// Built-in providers and tools self-register via init().
	_ "reasonix/internal/provider/anthropic"
	_ "reasonix/internal/provider/openai"
	_ "reasonix/internal/tool/builtin"
)

// version is injected at link time: -X main.version=...
var version = "dev"

func main() {
	os.Exit(cli.Run(os.Args[1:], version))
}
