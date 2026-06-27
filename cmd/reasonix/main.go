// Command reasonix is a config- and plugin-driven coding agent CLI.
package main

import (
	"os"

	"reasonix/internal/cli"
	"reasonix/internal/diag"

	// Blank imports wire compile-time built-ins into their registries.
	_ "reasonix/internal/provider/anthropic"
	_ "reasonix/internal/provider/openai"
	_ "reasonix/internal/tool/builtin"
)

// version is injected at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	diag.Init()
	defer diag.Close()
	os.Exit(cli.Run(os.Args[1:], version))
}
