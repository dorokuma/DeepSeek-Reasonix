package mcptrust

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"
)

func testCapability(name string, readOnly, destructive bool, schema string) Capability {
	return Capability{RawName: name, ModelName: "mcp__srv__" + name, InputSchema: json.RawMessage(schema), ReadOnly: readOnly, Destructive: destructive}
}

func TestCapabilityFingerprintIgnoresDisplayFields(t *testing.T) {
	a := testCapability("read", true, false, `{"type":"object","description":"old","properties":{"q":{"type":"string","title":"Q"}}}`)
	b := testCapability("read", true, false, `{"properties":{"q":{"type":"string","title":"Changed"}},"description":"new","type":"object"}`)
	af, err := CapabilityFingerprint(a)
	if err != nil {
		t.Fatal(err)
	}
	bf, err := CapabilityFingerprint(b)
	if err != nil {
		t.Fatal(err)
	}
	if af != bf {
		t.Fatalf("display-only schema changes altered fingerprint: %s != %s", af, bf)
	}
	b.InputSchema = json.RawMessage(`{"type":"object","properties":{"q":{"type":"integer"}}}`)
	bf, err = CapabilityFingerprint(b)
	if err != nil {
		t.Fatal(err)
	}
	if af == bf {
		t.Fatal("structural schema change did not alter fingerprint")
	}
}

func TestIdentityFingerprintCanonicalizesOrderingAndPaths(t *testing.T) {
	dir := t.TempDir()
	a := Identity{Server: "srv", Transport: "streamable-http", Dir: dir, EnvKeys: []string{"TOKEN", "PATH"}, HeaderKeys: []string{"X-B", "Authorization"}, WriteRoots: []string{filepath.Join(dir, "."), dir}}
	b := Identity{Server: "srv", Transport: "http", Dir: dir, EnvKeys: []string{"path", "token"}, HeaderKeys: []string{"authorization", "x-b"}, WriteRoots: []string{dir}}
	af, err := IdentityFingerprint(a)
	if err != nil {
		t.Fatal(err)
	}
	bf, err := IdentityFingerprint(b)
	if err != nil {
		t.Fatal(err)
	}
	if af != bf {
		t.Fatalf("canonical identities differ: %s != %s", af, bf)
	}
}

func TestIdentityFingerprintPreservesArgumentSemantics(t *testing.T) {
	base := Identity{Server: "srv", Transport: "stdio", CommandPath: "/bin/server", Args: []string{"--allow", "read", "read"}}
	baseFP, err := IdentityFingerprint(base)
	if err != nil {
		t.Fatal(err)
	}

	for name, args := range map[string][]string{
		"reordered":    {"read", "--allow", "read"},
		"deduplicated": {"--allow", "read"},
		"trimmed":      {"--allow", "read", " read "},
	} {
		t.Run(name, func(t *testing.T) {
			changed := base
			changed.Args = args
			changedFP, err := IdentityFingerprint(changed)
			if err != nil {
				t.Fatal(err)
			}
			if changedFP == baseFP {
				t.Fatalf("argument change %q did not alter identity fingerprint", name)
			}
		})
	}
}

func TestTrustScopesAndFineGrainedDrift(t *testing.T) {
	path := filepath.Join(t.TempDir(), StateFilename)
	m := NewManager(path, "/workspace/a")
	caps := []Capability{
		testCapability("read", true, false, `{"type":"object"}`),
		testCapability("write", false, false, `{"type":"object"}`),
	}
	if err := m.Trust(ScopeWorkspace, SourceUser, "srv", "project", "identity-1", "", caps); err != nil {
		t.Fatal(err)
	}

	eval, err := m.Evaluate("srv", "project", "identity-1", caps)
	if err != nil {
		t.Fatal(err)
	}
	if eval.State != TrustWorkspace || !eval.TrustedReaders["read"] || eval.TrustedReaders["write"] {
		t.Fatalf("evaluation = %+v", eval)
	}

	drifted := append([]Capability(nil), caps...)
	drifted[0] = testCapability("read", false, false, `{"type":"object"}`)
	drifted = append(drifted, testCapability("new", true, false, `{"type":"object"}`))
	eval, err = m.Evaluate("srv", "project", "identity-1", drifted)
	if err != nil {
		t.Fatal(err)
	}
	if eval.State != TrustChanged || eval.TrustedReaders["read"] || !reflect.DeepEqual(eval.ChangedTools, []string{"new", "read"}) {
		t.Fatalf("drift evaluation = %+v", eval)
	}
	wantChanges := []ToolChange{{Name: "new", Kind: "added"}, {Name: "read", Kind: "reader_to_writer"}}
	if !reflect.DeepEqual(eval.ToolChanges, wantChanges) {
		t.Fatalf("tool change details = %+v, want %+v", eval.ToolChanges, wantChanges)
	}

	eval, err = m.Evaluate("srv", "project", "identity-2", caps)
	if err != nil {
		t.Fatal(err)
	}
	if eval.State != TrustChanged || !eval.IdentityChanged || len(eval.TrustedReaders) != 0 {
		t.Fatalf("identity evaluation = %+v", eval)
	}

	other := NewManager(path, "/workspace/b")
	eval, err = other.Evaluate("srv", "project", "identity-1", caps)
	if err != nil {
		t.Fatal(err)
	}
	if eval.State != TrustUntrusted {
		t.Fatalf("cross-workspace state = %q", eval.State)
	}
}

func TestSessionTrustIsNotPersisted(t *testing.T) {
	path := filepath.Join(t.TempDir(), StateFilename)
	m := NewManager(path, "/workspace")
	caps := []Capability{testCapability("read", true, false, `{"type":"object"}`)}
	if err := m.Trust(ScopeSession, SourceUser, "srv", "project", "identity", "", caps); err != nil {
		t.Fatal(err)
	}
	eval, err := m.Evaluate("srv", "project", "identity", caps)
	if err != nil || eval.State != TrustSession || !eval.TrustedReaders["read"] {
		t.Fatalf("session evaluation = %+v, %v", eval, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("session trust wrote persistent state: %v", err)
	}
}

func TestGlobalTrustReservedForOfficialCatalog(t *testing.T) {
	m := NewManager(filepath.Join(t.TempDir(), StateFilename), "/workspace")
	caps := []Capability{testCapability("read", true, false, `{"type":"object"}`)}
	if err := m.Trust(ScopeGlobal, SourceUser, "srv", "", "identity", "", caps); err == nil {
		t.Fatal("user-created global trust should be rejected")
	}
	if err := m.Trust(ScopeGlobal, SourceOfficialCatalog, "srv", "", "identity", "entry", caps); err != nil {
		t.Fatal(err)
	}
	other := NewManager(m.path, "/other")
	eval, err := other.Evaluate("srv", "", "identity", caps)
	if err != nil || eval.State != TrustOfficial || !eval.TrustedReaders["read"] {
		t.Fatalf("official evaluation = %+v, %v", eval, err)
	}
}

func TestOfficialTrustOnlyGrantsCatalogReaders(t *testing.T) {
	m := NewManager(filepath.Join(t.TempDir(), StateFilename), "/workspace")
	caps := []Capability{
		testCapability("catalog_reader", true, false, `{"type":"object"}`),
		testCapability("undeclared_reader", true, false, `{"type":"object"}`),
		testCapability("writer", false, false, `{"type":"object"}`),
	}
	if err := m.TrustOfficial("srv", "official_catalog:entry", "identity", "entry", caps, []string{"catalog_reader", "writer"}); err != nil {
		t.Fatal(err)
	}
	eval, err := m.Evaluate("srv", "official_catalog:entry", "identity", caps)
	if err != nil {
		t.Fatal(err)
	}
	if !eval.TrustedReaders["catalog_reader"] {
		t.Fatalf("catalog reader was not trusted: %+v", eval)
	}
	if eval.TrustedReaders["undeclared_reader"] {
		t.Fatal("reader omitted from the signed catalog was trusted")
	}
	if eval.TrustedReaders["writer"] {
		t.Fatal("catalog entry promoted a writer to reader")
	}
}

func TestRevokeOfficialPreservesWorkspaceReceipt(t *testing.T) {
	path := filepath.Join(t.TempDir(), StateFilename)
	m := NewManager(path, "/workspace")
	caps := []Capability{testCapability("read", true, false, `{"type":"object"}`)}
	if err := m.TrustOfficial("official", "official_catalog:entry", "official-id", "entry", caps, []string{"read"}); err != nil {
		t.Fatal(err)
	}
	if err := m.Trust(ScopeWorkspace, SourceUser, "manual", "workspace_config", "manual-id", "", caps); err != nil {
		t.Fatal(err)
	}
	if err := m.RevokeOfficial("entry"); err != nil {
		t.Fatal(err)
	}
	if eval, err := m.Evaluate("official", "official_catalog:entry", "official-id", caps); err != nil || eval.State != TrustUntrusted {
		t.Fatalf("revoked official evaluation = %+v, %v", eval, err)
	}
	if eval, err := m.Evaluate("manual", "workspace_config", "manual-id", caps); err != nil || eval.State != TrustWorkspace {
		t.Fatalf("workspace evaluation after official revoke = %+v, %v", eval, err)
	}
}

func TestOfficialDenialPersistsUntilExplicitlyCleared(t *testing.T) {
	path := filepath.Join(t.TempDir(), StateFilename)
	m := NewManager(path, "/workspace")
	if err := m.DenyOfficial("catalog-entry"); err != nil {
		t.Fatal(err)
	}
	fresh := NewManager(path, "/other")
	if denied, err := fresh.OfficialDenied("catalog-entry"); err != nil || !denied {
		t.Fatalf("persisted official denial = %v, %v", denied, err)
	}
	if err := fresh.AllowOfficial("catalog-entry"); err != nil {
		t.Fatal(err)
	}
	if denied, err := m.OfficialDenied("catalog-entry"); err != nil || denied {
		t.Fatalf("cleared official denial = %v, %v", denied, err)
	}
}

func TestPersistentStoreUsesVersionAndPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", StateFilename)
	m := NewManager(path, "/workspace")
	if err := m.Trust(ScopeWorkspace, SourceUser, "srv", "project", "identity", "", []Capability{testCapability("read", true, false, `{"type":"object"}`)}); err != nil {
		t.Fatal(err)
	}
	state, err := m.Load()
	if err != nil {
		t.Fatal(err)
	}
	if state.Version != StoreVersion || len(state.Receipts) != 1 {
		t.Fatalf("state = %+v", state)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); runtime.GOOS != "windows" && got != 0o600 {
		t.Fatalf("mode = %o, want 600", got)
	}
}

func TestPersistentStoreSerializesConcurrentProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), StateFilename)
	const writers = 12
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			manager := NewManager(path, "/workspace")
			errs <- manager.Trust(ScopeWorkspace, SourceUser, fmt.Sprintf("srv-%02d", i), "project", fmt.Sprintf("identity-%02d", i), "", []Capability{testCapability("read", true, false, `{"type":"object"}`)})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	state, err := NewManager(path, "/workspace").Load()
	if err != nil || len(state.Receipts) != writers {
		t.Fatalf("concurrent trust state has %d receipts, err=%v", len(state.Receipts), err)
	}
}

func TestPersistentStoreRecoversStaleProcessLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), StateFilename)
	lockPath := path + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, []byte("dead-pid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-time.Minute)
	if err := os.Chtimes(lockPath, stale, stale); err != nil {
		t.Fatal(err)
	}
	m := NewManager(path, "/workspace")
	if err := m.Trust(ScopeWorkspace, SourceUser, "srv", "project", "identity", "", []Capability{testCapability("read", true, false, `{"type":"object"}`)}); err != nil {
		t.Fatalf("recover stale lock: %v", err)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("stale lock remains after update: %v", err)
	}
}

func TestPersistentStoreDoesNotStealStaleLiveProcessLock(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), StateFilename+".lock")
	owner := []byte(fmt.Sprintf("%d live-owner\n", os.Getpid()))
	if err := os.WriteFile(lockPath, owner, 0o600); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-time.Minute)
	if err := os.Chtimes(lockPath, stale, stale); err != nil {
		t.Fatal(err)
	}
	if unlock, err := acquireFileLock(lockPath, 50*time.Millisecond); err == nil {
		unlock()
		t.Fatal("acquired a stale-looking lock owned by a live process")
	}
	got, err := os.ReadFile(lockPath)
	if err != nil || string(got) != string(owner) {
		t.Fatalf("live owner lock changed: %q, %v", got, err)
	}
}

func TestFileLockUnlockPreservesReplacementOwner(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), StateFilename+".lock")
	unlock, err := acquireFileLock(lockPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	replacement := []byte(fmt.Sprintf("%d replacement-owner\n", os.Getpid()))
	if err := os.WriteFile(lockPath, replacement, 0o600); err != nil {
		t.Fatal(err)
	}
	unlock()
	got, err := os.ReadFile(lockPath)
	if err != nil || string(got) != string(replacement) {
		t.Fatalf("old unlock removed replacement owner: %q, %v", got, err)
	}
}

func TestHasReceiptTracksCurrentWorkspace(t *testing.T) {
	path := filepath.Join(t.TempDir(), StateFilename)
	m := NewManager(path, "/workspace/a")
	if ok, err := m.HasReceipt("srv", "project"); err != nil || ok {
		t.Fatalf("initial HasReceipt = %v, %v", ok, err)
	}
	if err := m.ImportLegacy("srv", "project", "identity", []string{"read"}); err != nil {
		t.Fatal(err)
	}
	if ok, err := m.HasReceipt("srv", "project"); err != nil || !ok {
		t.Fatalf("imported HasReceipt = %v, %v", ok, err)
	}
	other := NewManager(path, "/workspace/b")
	if ok, err := other.HasReceipt("srv", "project"); err != nil || ok {
		t.Fatalf("cross-workspace HasReceipt = %v, %v", ok, err)
	}
}

func TestLegacyImportMarkerSurvivesReceiptRevocation(t *testing.T) {
	path := filepath.Join(t.TempDir(), StateFilename)
	m := NewManager(path, "/workspace")
	if err := m.MarkLegacyImported("srv", "workspace_config"); err != nil {
		t.Fatal(err)
	}
	if err := m.Revoke("srv"); err != nil {
		t.Fatal(err)
	}
	fresh := NewManager(path, "/workspace")
	if imported, err := fresh.LegacyImported("srv", "workspace_config"); err != nil || !imported {
		t.Fatalf("legacy marker after revoke = %v, %v", imported, err)
	}
	other := NewManager(path, "/other")
	if imported, err := other.LegacyImported("srv", "workspace_config"); err != nil || imported {
		t.Fatalf("legacy marker leaked across workspaces = %v, %v", imported, err)
	}
}

func TestLauncherLocksAreWorkspaceScoped(t *testing.T) {
	path := filepath.Join(t.TempDir(), StateFilename)
	m := NewManager(path, "/workspace/a")
	lock := LauncherLock{Server: "srv", Locator: "package", ResolvedVersion: "package@1.2.3", ContentSHA256: "abc"}
	if err := m.PutLauncherLock(lock); err != nil {
		t.Fatal(err)
	}
	got, ok, err := m.GetLauncherLock("srv", "package")
	if err != nil || !ok || got.ResolvedVersion != "package@1.2.3" {
		t.Fatalf("launcher lock = %+v, %v, %v", got, ok, err)
	}
	other := NewManager(path, "/workspace/b")
	if _, ok, err := other.GetLauncherLock("srv", "package"); err != nil || ok {
		t.Fatalf("cross-workspace launcher lock = %v, %v", ok, err)
	}
}
