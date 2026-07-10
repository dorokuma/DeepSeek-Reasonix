package fileref

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSearch(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pkg", "foo.go"), []byte("package pkg"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("#"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Join(root, "node_modules"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "node_modules", "foo.js"), []byte("x"), 0o644)

	got := Search(root, "foo", 10)
	if len(got) != 1 || got[0] != "pkg/foo.go" {
		t.Fatalf("Search foo = %v, want [pkg/foo.go]", got)
	}
	if Search(root, "a", 10) != nil {
		t.Fatal("query shorter than min should return nil")
	}
	if Search(root, "foo/bar", 10) != nil {
		t.Fatal("path-like query should return nil")
	}
	if Search(root, "foo", 0) != nil {
		t.Fatal("limit<=0 should return nil")
	}
}
