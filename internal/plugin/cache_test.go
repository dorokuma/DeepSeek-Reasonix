package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/mcptrust"
	"reasonix/internal/sandbox"
	"reasonix/internal/tool"
)

// redirectCache points config.CacheDir() at a fresh temp dir for the duration
// of the test (without it the tests write the real user cache).
// Returns the temp dir so a test can also poke into it (e.g. write a corrupted file).
func redirectCache(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("REASONIX_CACHE_HOME", dir)
	return dir
}

func sampleSpec() Spec {
	return Spec{
		Name:    "my-server",
		Type:    "stdio",
		Command: "/usr/bin/example",
		Args:    []string{"--flag", "x"},
		Env:     map[string]string{"FOO": "1", "BAR": "2"},
		Headers: map[string]string{"X-Custom": "ok"},
		Dir:     "/work",
	}
}

func sampleCachedSchema(hash string) CachedSchema {
	return CachedSchema{
		SpecHash:     hash,
		Capabilities: map[string]bool{"prompts": true, "resources": false},
		Tools: []CachedTool{{
			Name:        "do_thing",
			Description: "does a thing",
			Schema:      json.RawMessage(`{"type":"object"}`),
			ReadOnly:    true,
			Destructive: true,
		}},
	}
}

func TestCacheRoundTrip(t *testing.T) {
	redirectCache(t)
	spec := sampleSpec()
	hash := SpecFingerprint(spec)
	cs := sampleCachedSchema(hash)

	if err := SaveCachedSchema(spec.Name, cs); err != nil {
		t.Fatalf("SaveCachedSchema: %v", err)
	}
	got, ok := LoadCachedSchema(spec.Name, hash)
	if !ok {
		t.Fatal("LoadCachedSchema: miss after save")
	}
	if got.SpecHash != hash {
		t.Errorf("SpecHash: got %q want %q", got.SpecHash, hash)
	}
	if len(got.Tools) != 1 || got.Tools[0].Name != "do_thing" {
		t.Errorf("Tools: %+v", got.Tools)
	}
	if !got.Tools[0].ReadOnly {
		t.Error("ReadOnly: lost across save/load")
	}
	if !got.Tools[0].Destructive {
		t.Error("Destructive: lost across save/load")
	}
	if !got.Capabilities["prompts"] || got.Capabilities["resources"] {
		t.Errorf("Capabilities: %+v", got.Capabilities)
	}
	if got.Version != cacheVersion {
		t.Errorf("Version: got %d want %d", got.Version, cacheVersion)
	}
	if got.LastValidated.IsZero() {
		t.Error("LastValidated: expected non-zero after save")
	}
}

func TestCachePersistsDeclaredReaderIndependentlyOfLocalTrust(t *testing.T) {
	cached := cacheableToolsOf([]tool.Tool{&remoteTool{
		rawName: "search", schema: json.RawMessage(`{"type":"object"}`),
		declaredReadOnly: true, readOnly: false, readOnlyTrusted: false,
	}})
	if len(cached) != 1 || !cached[0].ReadOnly {
		t.Fatalf("cached tool = %+v, want the server-declared reader snapshot", cached)
	}
}

func TestCachedToolTrustForSpecRequiresMatchingExistingReceipt(t *testing.T) {
	redirectCache(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	manager := mcptrust.NewManager(filepath.Join(t.TempDir(), mcptrust.StateFilename), t.TempDir())
	spec := Spec{
		Name: "cached-reader", Type: "stdio", Command: exe,
		ConfigSource: "workspace_config", TrustManager: manager,
	}
	reader := CachedTool{
		Name: "search", Schema: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`), ReadOnly: true,
	}
	if err := SaveCachedSchema(spec.Name, CachedSchema{SpecHash: SpecFingerprint(spec), Tools: []CachedTool{reader}}); err != nil {
		t.Fatal(err)
	}
	before, found, err := CachedToolTrustForSpec(context.Background(), spec, "search")
	if err != nil || !found || before.TrustedReader {
		t.Fatalf("before receipt = (%+v,%v,%v), want found but untrusted", before, found, err)
	}
	identity, err := specIdentityFingerprint(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	capability := mcptrust.Capability{
		RawName: "search", ModelName: toolName(spec.Name, "search"), InputSchema: reader.Schema, ReadOnly: true,
	}
	if err := manager.Trust(mcptrust.ScopeSession, mcptrust.SourceUser, spec.Name, trustConfigSource(spec), identity, "", []mcptrust.Capability{capability}); err != nil {
		t.Fatal(err)
	}
	after, found, err := CachedToolTrustForSpec(context.Background(), spec, "search")
	if err != nil || !found || !after.TrustedReader {
		t.Fatalf("matching receipt = (%+v,%v,%v), want trusted reader", after, found, err)
	}

	reader.Schema = json.RawMessage(`{"type":"object","properties":{"q":{"type":"number"}}}`)
	if err := SaveCachedSchema(spec.Name, CachedSchema{SpecHash: SpecFingerprint(spec), Tools: []CachedTool{reader}}); err != nil {
		t.Fatal(err)
	}
	drifted, found, err := CachedToolTrustForSpec(context.Background(), spec, "search")
	if err != nil || !found || drifted.TrustedReader || drifted.TrustState != mcptrust.TrustChanged {
		t.Fatalf("schema drift = (%+v,%v,%v), want changed and untrusted", drifted, found, err)
	}
}

func TestCacheLoadsLegacyToolWithoutDestructiveField(t *testing.T) {
	redirectCache(t)
	spec := sampleSpec()
	hash := SpecFingerprint(spec)
	p := cachePath(spec.Name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `{"version":2,"spec_hash":"` + hash + `","capabilities":{},"tools":[{"name":"read","description":"legacy","schema":{"type":"object"},"read_only":true}],"last_validated":"2026-01-01T00:00:00Z"}`
	if err := os.WriteFile(p, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	got, ok := LoadCachedSchema(spec.Name, hash)
	if !ok || len(got.Tools) != 1 {
		t.Fatalf("legacy cache = (%+v,%v), want one tool", got, ok)
	}
	if got.Tools[0].Destructive {
		t.Fatal("legacy cache without destructive field must default to false")
	}
}

func TestCacheLoadQuarantinesMalformedToolSchema(t *testing.T) {
	redirectCache(t)
	spec := sampleSpec()
	hash := SpecFingerprint(spec)
	cs := sampleCachedSchema(hash)
	cs.Tools = append(cs.Tools, CachedTool{
		Name: "generate_yso_bytes",
		Schema: json.RawMessage(`{
			"type":"object",
			"properties":{"options":{"type":"array","items":{"key":{"type":"string"},"type":{"type":"string"},"value":{"type":"string"}}}}
		}`),
	})

	if err := SaveCachedSchema(spec.Name, cs); err != nil {
		t.Fatalf("SaveCachedSchema: %v", err)
	}
	got, ok := LoadCachedSchema(spec.Name, hash)
	if !ok {
		t.Fatal("LoadCachedSchema: miss after save")
	}
	if len(got.Tools) != 1 || got.Tools[0].Name != "do_thing" {
		t.Fatalf("cached tools = %+v, want only valid do_thing", got.Tools)
	}
	if schema := string(got.Tools[0].Schema); schema != `{"properties":{},"type":"object"}` {
		t.Fatalf("valid cached schema = %s", schema)
	}
}

func TestCacheInvalidatesOnSpecHashMismatch(t *testing.T) {
	redirectCache(t)
	spec := sampleSpec()
	hash := SpecFingerprint(spec)
	if err := SaveCachedSchema(spec.Name, sampleCachedSchema(hash)); err != nil {
		t.Fatalf("SaveCachedSchema: %v", err)
	}
	if _, ok := LoadCachedSchema(spec.Name, "different-hash"); ok {
		t.Fatal("LoadCachedSchema: hit despite mismatching expectedHash")
	}
}

func TestCacheInvalidatesWhenReadOnlyTrustChanges(t *testing.T) {
	redirectCache(t)
	spec := sampleSpec()
	spec.ReadOnlyToolNames = map[string]bool{"echo": true}
	spec.ReadOnlyModelToolNames = map[string]bool{"mcp__my-server__search": true}
	hash := SpecFingerprint(spec)
	if err := SaveCachedSchema(spec.Name, sampleCachedSchema(hash)); err != nil {
		t.Fatalf("SaveCachedSchema: %v", err)
	}

	withoutTrust := sampleSpec()
	if _, ok := LoadCachedSchema(spec.Name, SpecFingerprint(withoutTrust)); ok {
		t.Fatal("LoadCachedSchema: hit after trusted read-only config changed")
	}
}

func TestCacheCorruptedFileReturnsFalse(t *testing.T) {
	redirectCache(t)
	p := cachePath("broken")
	if p == "" {
		t.Skip("cachePath unavailable in this environment")
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{this is not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("LoadCachedSchema panicked on corrupt file: %v", r)
		}
	}()
	if _, ok := LoadCachedSchema("broken", "any"); ok {
		t.Fatal("LoadCachedSchema: hit on corrupt file")
	}
}

func TestCacheVersionMismatchReturnsFalse(t *testing.T) {
	// Pin the on-disk version to one we don't recognise: a future writer must
	// not poison an older binary's cache reads.
	redirectCache(t)
	spec := sampleSpec()
	hash := SpecFingerprint(spec)
	cs := sampleCachedSchema(hash)
	cs.Version = cacheVersion + 99
	p := cachePath(spec.Name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(cs)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := LoadCachedSchema(spec.Name, hash); ok {
		t.Fatal("LoadCachedSchema: hit on future-version cache file")
	}
}

func TestSpecFingerprintStable(t *testing.T) {
	spec := sampleSpec()
	h1 := SpecFingerprint(spec)
	h2 := SpecFingerprint(spec)
	if h1 != h2 {
		t.Fatalf("SpecFingerprint not stable: %q vs %q", h1, h2)
	}

	// Reorder the env (build a new map; Go map iteration order is randomised
	// but a fresh map can incidentally iterate the same way, so we run a few
	// iterations to give the runtime a chance to shuffle).
	reordered := spec
	for i := 0; i < 32; i++ {
		reordered.Env = map[string]string{"BAR": "2", "FOO": "1"}
		if got := SpecFingerprint(reordered); got != h1 {
			t.Fatalf("SpecFingerprint changed when env was rebuilt: %q vs %q", got, h1)
		}
	}
}

func TestSpecFingerprintIgnoresHostLocalTrustAndIsolation(t *testing.T) {
	base := sampleSpec()
	changed := base
	changed.TrustManager = mcptrust.NewManager(filepath.Join(t.TempDir(), mcptrust.StateFilename), "/workspace")
	changed.ConfigSource = "project:.mcp.json"
	changed.OfficialCatalogEntryID = "plugin@example.com@1.0.0"
	changed.OfficialReaderNames = []string{"search"}
	changed.PackageDigest = "sha256:package"
	changed.VerifiedVersion = "1.0.0"
	changed.CatalogSequence = 42
	changed.ReaderSandbox = sandbox.Spec{Mode: "enforce", Network: true, WriteRoots: []string{"/host/state"}, MinimalWrites: true}
	changed.WriterSandbox = sandbox.Spec{Mode: "enforce", WriteRoots: []string{"/workspace"}, MinimalWrites: true}
	changed.StateDir = "/host/state"
	changed.OneShot = true
	if got, want := SpecFingerprint(changed), SpecFingerprint(base); got != want {
		t.Fatalf("host-local security state changed provider cache fingerprint: %q != %q", got, want)
	}
}

func TestSpecFingerprintTracksNonSecretIdentityOnly(t *testing.T) {
	a := sampleSpec()
	b := a
	b.Command = "/usr/local/bin/other"
	if SpecFingerprint(a) == SpecFingerprint(b) {
		t.Fatal("SpecFingerprint did not change when Command changed")
	}

	c := a
	c.Args = append([]string{}, a.Args...)
	c.Args[0] = "--different"
	if SpecFingerprint(a) == SpecFingerprint(c) {
		t.Fatal("SpecFingerprint did not change when Args changed")
	}

	d := a
	d.Env = map[string]string{"FOO": "1", "BAR": "different"}
	if SpecFingerprint(a) != SpecFingerprint(d) {
		t.Fatal("SpecFingerprint changed when an environment credential value rotated")
	}

	e := a
	e.Env = map[string]string{"FOO": "1", "NEW_KEY": "2"}
	if SpecFingerprint(a) == SpecFingerprint(e) {
		t.Fatal("SpecFingerprint did not change when environment key names changed")
	}

	f := a
	f.Headers = map[string]string{"X-Custom": "rotated-secret"}
	if SpecFingerprint(a) != SpecFingerprint(f) {
		t.Fatal("SpecFingerprint changed when a header credential value rotated")
	}

	g := a
	g.Headers = map[string]string{"Authorization": "secret"}
	if SpecFingerprint(a) == SpecFingerprint(g) {
		t.Fatal("SpecFingerprint did not change when header key names changed")
	}
}

func TestCacheMissForUnknownName(t *testing.T) {
	redirectCache(t)
	if _, ok := LoadCachedSchema("never-saved", "anything"); ok {
		t.Fatal("LoadCachedSchema: hit for a name that was never saved")
	}
}

func TestSlugSafeForFilesystem(t *testing.T) {
	cases := map[string]string{
		"My Server!":           "my-server",
		"weird/name\\with:bad": "weird-name-with-bad",
		"":                     "_",
		"------":               "_",
		"a_b-c":                "a_b-c",
	}
	for in, want := range cases {
		if got := slug(in); got != want {
			t.Errorf("slug(%q) = %q want %q", in, got, want)
		}
	}
}
