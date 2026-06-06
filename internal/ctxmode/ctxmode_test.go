package ctxmode

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestActive_defaultOn(t *testing.T) {
	t.Setenv("REASONIX_CTX", "")
	if !Active() {
		t.Fatal("want active by default")
	}
	t.Setenv("REASONIX_CTX", "off")
	if Active() {
		t.Fatal("want off")
	}
}

func TestTransform_readFile(t *testing.T) {
	t.Setenv("REASONIX_CTX", "on")
	t.Setenv("REASONIX_CTX_THRESHOLD", "100")
	store := NewStore()
	body := strings.Repeat("line\n", 200)
	args := json.RawMessage(`{"path":"foo.go"}`)
	out, notice, ok := Transform(store, "read_file", args, body)
	if !ok {
		t.Fatal("want transform")
	}
	if notice == "" || !strings.Contains(notice, "ctx-1") {
		t.Fatalf("notice = %q", notice)
	}
	if strings.Contains(out, strings.Repeat("line\n", 50)) {
		t.Fatal("full body should not appear in summary")
	}
	if !strings.Contains(out, "ctx-1") || !strings.Contains(out, "ctx_read") {
		t.Fatalf("summary missing refs: %q", out)
	}
}

func TestStore_read_search(t *testing.T) {
	store := NewStore()
	id, err := store.Put("grep", "foo", "a.go:1:alpha\nb.go:2:foo\nb.go:3:beta\n")
	if err != nil {
		t.Fatal(err)
	}
	read, err := store.Read(id, 1, 1)
	if err != nil || !strings.Contains(read, "b.go:2:foo") {
		t.Fatalf("read = %q err=%v", read, err)
	}
	search, err := store.Search(id, "foo", 10)
	if err != nil || !strings.Contains(search, "b.go:2:foo") {
		t.Fatalf("search = %q err=%v", search, err)
	}
}

func TestTransform_skipsSmall(t *testing.T) {
	t.Setenv("REASONIX_CTX", "on")
	store := NewStore()
	_, _, ok := Transform(store, "read_file", nil, "tiny")
	if ok {
		t.Fatal("small body should not transform")
	}
}