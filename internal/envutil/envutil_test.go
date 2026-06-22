package envutil

import (
	"os"
	"reflect"
	"runtime"
	"testing"
)

// ---------------------------------------------------------------------------
// StripCredentialEnv
// ---------------------------------------------------------------------------

func TestStripCredentialEnv_RemovesSuffixKey(t *testing.T) {
	env := []string{
		"FOO=bar",
		"MY_KEY=secret",
		"OTHER_TOKEN=xyz",
		"DB_SECRET=pass",
		"USER_PASSWORD=hunter2",
	}
	got := StripCredentialEnv(env)
	want := []string{"FOO=bar"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestStripCredentialEnv_RemovesSuffixToken(t *testing.T) {
	env := []string{
		"SAFE=1",
		"GITHUB_TOKEN=ghp_abc",
	}
	got := StripCredentialEnv(env)
	want := []string{"SAFE=1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestStripCredentialEnv_RemovesSuffixSecret(t *testing.T) {
	env := []string{
		"KUBE_SECRET=xyz",
		"API_SECRET=s3cr3t",
		"VISIBLE=hello",
	}
	got := StripCredentialEnv(env)
	want := []string{"VISIBLE=hello"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestStripCredentialEnv_RemovesSuffixPassword(t *testing.T) {
	env := []string{
		"DB_PASSWORD=p@ss",
		"PGPASSWORD=pgpass",
		"KEEP=stay",
	}
	got := StripCredentialEnv(env)
	want := []string{"KEEP=stay"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestStripCredentialEnv_RemovesBareKeywords(t *testing.T) {
	env := []string{
		"TOKEN=abc",
		"SECRET=def",
		"PASSWORD=ghi",
		"AUTHORIZATION=Bearer xxx",
		"BEARER=yyy",
		"KEEP=visible",
	}
	got := StripCredentialEnv(env)
	want := []string{"KEEP=visible"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestStripCredentialEnv_PreservesNormalVariables(t *testing.T) {
	env := []string{
		"HOME=/root",
		"PATH=/usr/bin:/bin",
		"USER=admin",
		"LANG=en_US.UTF-8",
		"MYAPP_DEBUG=true",
		"PUBLIC_URL=https://example.com",
		"SENTRY_DSN=https://public@sentry.io/1",
	}
	got := StripCredentialEnv(env)
	want := env
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestStripCredentialEnv_EmptyInput(t *testing.T) {
	got := StripCredentialEnv([]string{})
	if got == nil {
		t.Fatal("expected non-nil slice")
	}
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %v", got)
	}
}

func TestStripCredentialEnv_NilInput(t *testing.T) {
	got := StripCredentialEnv(nil)
	// nil input should return an empty (non-nil) slice
	if got == nil {
		t.Fatal("expected non-nil slice for nil input")
	}
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %v", got)
	}
}

func TestStripCredentialEnv_MalformedEntry(t *testing.T) {
	// Entries without "=" are skipped.
	env := []string{
		"MALFORMED",
		"SOMETHING_ELSE",
	}
	got := StripCredentialEnv(env)
	if len(got) != 0 {
		t.Errorf("expected 0 entries, got %v", got)
	}
}

func TestStripCredentialEnv_AllowedURL(t *testing.T) {
	// PUBLIC_URL, WEBSITE_URL, APP_URL, REASONIX_BASE_URL, etc.
	env := []string{
		"PUBLIC_URL=https://public.example.com",
		"WEBSITE_URL=https://site.example.com",
		"APP_URL=https://app.example.com",
		"HOMEPAGE_URL=https://home.example.com",
		"REASONIX_BASE_URL=https://reasonix.example.com",
	}
	got := StripCredentialEnv(env)
	if !reflect.DeepEqual(got, env) {
		t.Errorf("allowed URL vars were stripped: got %v", got)
	}
}

func TestStripCredentialEnv_AllowedDSN(t *testing.T) {
	env := []string{"SENTRY_DSN=https://key@sentry.io/123"}
	got := StripCredentialEnv(env)
	if !reflect.DeepEqual(got, env) {
		t.Errorf("SENTRY_DSN was stripped: got %v", got)
	}
}

func TestStripCredentialEnv_DisallowedURL(t *testing.T) {
	// A *_URL not in the allowed list should be stripped.
	env := []string{"MY_SECRET_URL=http://evil.com"}
	got := StripCredentialEnv(env)
	if len(got) != 0 {
		t.Errorf("expected none, got %v", got)
	}
}

func TestStripCredentialEnv_DisallowedDSN(t *testing.T) {
	// A *_DSN not in the allowed list should be stripped.
	env := []string{"CUSTOM_DSN=postgres://user:pass@host/db"}
	got := StripCredentialEnv(env)
	if len(got) != 0 {
		t.Errorf("expected none, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// SetEnvValue
// ---------------------------------------------------------------------------

func TestSetEnvValue_ReplaceLast(t *testing.T) {
	env := []string{"A=1", "B=2", "A=old"}
	got := SetEnvValue(env, "A", "new")
	want := []string{"A=1", "B=2", "A=new"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSetEnvValue_Append(t *testing.T) {
	env := []string{"A=1"}
	got := SetEnvValue(env, "B", "2")
	want := []string{"A=1", "B=2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSetEnvValue_EmptyToAppend(t *testing.T) {
	got := SetEnvValue(nil, "X", "y")
	want := []string{"X=y"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// On Windows keys are case-insensitive; on Unix they are not.
func TestSetEnvValue_CaseSensitivity(t *testing.T) {
	env := []string{"a=1", "A=2"}
	if runtime.GOOS == "windows" {
		got := SetEnvValue(env, "A", "3")
		want := []string{"a=1", "A=3"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Windows: got %v, want %v", got, want)
		}
	} else {
		got := SetEnvValue(env, "A", "3")
		want := []string{"a=1", "A=3"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Unix: got %v, want %v", got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// EnvValue
// ---------------------------------------------------------------------------

func TestEnvValue_Found(t *testing.T) {
	env := []string{"A=first", "B=second", "A=last"}
	v, ok := EnvValue(env, "A")
	if !ok || v != "last" {
		t.Errorf(`EnvValue(_, "A") = %q, %v; want "last", true`, v, ok)
	}
}

func TestEnvValue_NotFound(t *testing.T) {
	v, ok := EnvValue([]string{"A=1"}, "B")
	if ok {
		t.Errorf("expected false, got true, value=%q", v)
	}
}

func TestEnvValue_EmptySlice(t *testing.T) {
	v, ok := EnvValue(nil, "A")
	if ok {
		t.Errorf("expected false, got true, value=%q", v)
	}
}

// ---------------------------------------------------------------------------
// KeyEqual
// ---------------------------------------------------------------------------

func TestKeyEqual(t *testing.T) {
	if runtime.GOOS == "windows" {
		if !KeyEqual("PATH", "path") {
			t.Error("expected case-insensitive match on Windows")
		}
	} else {
		if KeyEqual("PATH", "path") {
			t.Error("expected case-sensitive mismatch on Unix")
		}
	}
}

// ---------------------------------------------------------------------------
// MergePathLists
// ---------------------------------------------------------------------------

func TestMergePathLists(t *testing.T) {
	primary := "/a:/b:/c"
	secondary := "/b:/d:/e"
	got := MergePathLists(primary, secondary)
	sep := string(os.PathListSeparator)
	parts := stringsSplit(got, sep)
	expected := []string{"/a", "/b", "/c", "/d", "/e"}
	if !reflect.DeepEqual(parts, expected) {
		t.Errorf("got %v, want %v", parts, expected)
	}
}

func TestMergePathLists_EmptyParts(t *testing.T) {
	got := MergePathLists("/a::/b", "/b:/c")
	sep := string(os.PathListSeparator)
	parts := stringsSplit(got, sep)
	expected := []string{"/a", "/b", "/c"}
	if !reflect.DeepEqual(parts, expected) {
		t.Errorf("got %v, want %v", parts, expected)
	}
}

func TestMergePathLists_BothEmpty(t *testing.T) {
	got := MergePathLists("", "")
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

// stringsSplit is a small helper to avoid importing strings in tests.
func stringsSplit(s, sep string) []string {
	var out []string
	for i := 0; i < len(s); {
		j := i + len(sep)
		if j > len(s) || s[i:j] != sep {
			j = i + 1
			for j <= len(s) && s[j-1:j] != sep {
				j++
			}
		}
		out = append(out, s[i:j-len(sep)])
		i = j
	}
	return out
}
