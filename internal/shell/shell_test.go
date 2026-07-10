package shell

import (
	"runtime"
	"testing"
)

func TestShellKindString(t *testing.T) {
	if ShellBash.String() != "bash" {
		t.Fatalf("ShellBash.String = %q", ShellBash.String())
	}
	if ShellPowerShell.String() != "powershell" {
		t.Fatalf("ShellPowerShell.String = %q", ShellPowerShell.String())
	}
}

func TestResolveShellReturnsSomething(t *testing.T) {
	s := ResolveShell()
	if s.Path == "" {
		t.Fatal("ResolveShell path empty")
	}
	if s.Kind != ShellBash && s.Kind != ShellPowerShell {
		t.Fatalf("unexpected kind %v", s.Kind)
	}
	// On Linux CI we expect bash.
	if runtime.GOOS == "linux" && s.Kind != ShellBash {
		t.Fatalf("linux expected bash, got %v path=%s", s.Kind, s.Path)
	}
}

func TestResolveShellInjected(t *testing.T) {
	look := func(name string) (string, error) {
		if name == "bash" {
			return "/bin/bash", nil
		}
		return "", errNotFound
	}
	exists := func(string) bool { return false }
	probe := func(string) bool { return true }
	isWSL := func(string) bool { return false }
	got := resolveShell("linux", look, exists, nil, probe, isWSL)
	if got.Path != "/bin/bash" || got.Kind != ShellBash {
		t.Fatalf("got %+v", got)
	}
}

type notFoundError struct{}

func (notFoundError) Error() string { return "not found" }

var errNotFound = notFoundError{}
