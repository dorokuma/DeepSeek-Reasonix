package rtk

import (
	"strings"
	"testing"
)

// rewriteSamples maps shell commands to expected RTK rewrite prefixes.
// decline=true means rewrite must return "" (no hijack).
var rewriteSamples = []struct {
	cmd     string
	wantRTK string // required prefix when accepted
	decline bool
}{
	// Core file/search (bash + builtins)
	{cmd: "ls .", wantRTK: "rtk ls"},
	{cmd: "tree -L 2 .", wantRTK: "rtk tree"},
	{cmd: "cat README.md", wantRTK: "rtk read"},
	{cmd: "rg foo .", wantRTK: "rtk grep"},
	{cmd: `find . -name '*.go'`, wantRTK: "rtk find"},

	// VCS / cloud / ops
	{cmd: "git status", wantRTK: "rtk git"},
	{cmd: "git log -5", wantRTK: "rtk git"},
	{cmd: "gh pr list", wantRTK: "rtk gh"},
	{cmd: "glab mr list", wantRTK: "rtk glab"},
	{cmd: "aws s3 ls", wantRTK: "rtk aws"},
	{cmd: "docker ps", wantRTK: "rtk docker"},
	{cmd: "kubectl get pods", wantRTK: "rtk kubectl"},

	// Package managers / runtimes
	{cmd: "pnpm list", wantRTK: "rtk pnpm"},
	{cmd: "npm run build", wantRTK: "rtk npm"},
	{cmd: "npx tsc --noEmit", wantRTK: "rtk tsc"},
	{cmd: "pip list", wantRTK: "rtk pip"},
	{cmd: "cargo test", wantRTK: "rtk cargo"},
	{cmd: "go test ./...", wantRTK: "rtk go"},
	{cmd: "./gradlew test", wantRTK: "rtk gradlew"},

	// Test / lint / format toolchains
	{cmd: "jest", wantRTK: "rtk jest"},
	{cmd: "vitest run", wantRTK: "rtk vitest"},
	{cmd: "pytest -q", wantRTK: "rtk pytest"},
	{cmd: "eslint .", wantRTK: "rtk lint"},
	{cmd: "prettier --check .", wantRTK: "rtk prettier"},
	{cmd: "ruff check .", wantRTK: "rtk ruff"},
	{cmd: "ruff format --check .", wantRTK: "rtk ruff"},
	{cmd: "mypy .", wantRTK: "rtk mypy"},
	{cmd: "tsc --noEmit", wantRTK: "rtk tsc"},
	{cmd: "next build", wantRTK: "rtk next"},
	{cmd: "prisma validate", wantRTK: "rtk prisma"},
	{cmd: "playwright test", wantRTK: "rtk playwright"},
	{cmd: "dotnet build", wantRTK: "rtk dotnet"},
	{cmd: "rake test", wantRTK: "rtk rake"},
	{cmd: "rubocop", wantRTK: "rtk rubocop"},
	{cmd: "rspec", wantRTK: "rtk rspec"},
	{cmd: "golangci-lint run", wantRTK: "rtk golangci-lint"},
	{cmd: "gt log", wantRTK: "rtk gt"},

	// Misc shell utilities
	{cmd: "git diff", wantRTK: "rtk git"},
	{cmd: "wget -qO- https://example.com", wantRTK: "rtk wget"},
	{cmd: "wc -l README.md", wantRTK: "rtk wc"},
	{cmd: "curl -s https://example.com", wantRTK: "rtk curl"},
	{cmd: "cargo build 2>&1", wantRTK: "rtk cargo"},
	{cmd: "make test", wantRTK: "rtk make"},
	{cmd: "ls -la", wantRTK: "rtk ls"},

	// Explicit rtk subcommands (no native shell rewrite)
	{cmd: "rtk deps", wantRTK: "rtk deps"},
	{cmd: "rtk json", wantRTK: "rtk json"},
	{cmd: "rtk test", wantRTK: "rtk test"},
	{cmd: "rtk env", wantRTK: "rtk env"},
	{cmd: "rtk log", wantRTK: "rtk log"},
	{cmd: "rtk summary", wantRTK: "rtk summary"},
	{cmd: "rtk format", wantRTK: "rtk format"},
	{cmd: "rtk smart README.md", wantRTK: "rtk smart"},

	// Must decline — native path, no RTK hijack
	{cmd: "echo hello", decline: true},
	{cmd: "python3 -c 'print(1)'", decline: true},
	{cmd: "read README.md", decline: true},
	{cmd: "npm list", decline: true},
	{cmd: "npm install", decline: true},
	{cmd: "env", decline: true},
	{cmd: "black --check .", decline: true},
	{cmd: "journalctl -n 5", decline: true},
}

func TestRewriteMatrix(t *testing.T) {
	if !Available() {
		t.Skip("rtk not on PATH")
	}
	for _, tc := range rewriteSamples {
		tc := tc
		t.Run(tc.cmd, func(t *testing.T) {
			got := Rewrite(tc.cmd)
			if tc.decline {
				if got != "" {
					t.Fatalf("rewrite %q = %q, want decline", tc.cmd, got)
				}
				return
			}
			if got == "" || !strings.HasPrefix(got, tc.wantRTK) {
				t.Fatalf("rewrite %q = %q, want prefix %q", tc.cmd, got, tc.wantRTK)
			}
		})
	}
}