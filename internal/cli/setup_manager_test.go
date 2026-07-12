package cli

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/config"
)

func setupTestConfig() *config.Config {
	cfg := config.Default()
	cfg.Providers = []config.ProviderEntry{
		{Name: "desktop-provider", Kind: "openai", BaseURL: "https://desktop.example/v1", Model: "desktop-model", APIKeyEnv: "SHARED_API_KEY"},
		{Name: "cli-provider", Kind: "openai", BaseURL: "https://cli.example/v1", Model: "cli-model"},
	}
	cfg.DefaultModel = "desktop-provider"
	cfg.Agent.MaxSteps = 77
	cfg.Desktop.ProviderAccess = []string{"desktop-provider", "cli-provider"}
	return cfg
}

func TestProviderSetupSessionAddPreservesExistingProvidersAndSettings(t *testing.T) {
	cfg := setupTestConfig()
	s := newProviderSetupSession(cfg)
	added := config.ProviderEntry{Name: "grok-relay", Kind: "openai", BaseURL: "https://relay.example/v1", Model: "grok-4.5", APIKeyEnv: "GROK_RELAY_API_KEY"}
	if err := s.upsert([]config.ProviderEntry{added}); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Providers) != 3 || cfg.Providers[0].Name != "desktop-provider" || cfg.Providers[1].Name != "cli-provider" {
		t.Fatalf("existing providers were not preserved: %+v", cfg.Providers)
	}
	if cfg.DefaultModel != "desktop-provider" || cfg.Agent.MaxSteps != 77 {
		t.Fatalf("unrelated settings changed: default=%q max_steps=%d", cfg.DefaultModel, cfg.Agent.MaxSteps)
	}
	s.addProviderAccess([]config.ProviderEntry{added})
	if got := cfg.Desktop.ProviderAccess; !containsString(got, "desktop-provider") || !containsString(got, "cli-provider") || !containsString(got, "grok-relay") {
		t.Fatalf("desktop provider access was not preserved and extended: %v", got)
	}
}

func TestProviderSetupSessionEditPreservesSiblingAndAdvancedFields(t *testing.T) {
	cfg := setupTestConfig()
	cfg.Providers[0].Headers = map[string]string{"X-Relay": "yes"}
	s := newProviderSetupSession(cfg)
	edited := cfg.Providers[0]
	edited.Models = []string{"desktop-model", "desktop-model-2"}
	edited.Model = ""
	if err := s.upsert([]config.ProviderEntry{edited}); err != nil {
		t.Fatal(err)
	}
	if cfg.Providers[1].Name != "cli-provider" {
		t.Fatalf("sibling provider changed: %+v", cfg.Providers[1])
	}
	if cfg.Providers[0].Headers["X-Relay"] != "yes" {
		t.Fatalf("advanced provider fields were lost: %+v", cfg.Providers[0])
	}
}

func TestProviderSetupSessionRemovalIsExplicitAndRepairsDefault(t *testing.T) {
	cfg := setupTestConfig()
	s := newProviderSetupSession(cfg)
	if len(cfg.Providers) != 2 {
		t.Fatal("provider changed before explicit remove")
	}
	if err := s.remove("desktop-provider"); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Providers) != 1 || cfg.Providers[0].Name != "cli-provider" {
		t.Fatalf("remove result = %+v", cfg.Providers)
	}
	if cfg.DefaultModel != "cli-provider" {
		t.Fatalf("default fallback = %q, want cli-provider", cfg.DefaultModel)
	}
	if containsString(cfg.Desktop.ProviderAccess, "desktop-provider") || !containsString(cfg.Desktop.ProviderAccess, "cli-provider") {
		t.Fatalf("desktop provider access was not cleaned safely: %v", cfg.Desktop.ProviderAccess)
	}
}

func TestProviderSetupSessionAddAccessRespectsExplicitEmptyList(t *testing.T) {
	cfg := setupTestConfig()
	cfg.Desktop.ProviderAccess = nil
	s := newProviderSetupSession(cfg)
	s.accessDeclared = true
	added := config.ProviderEntry{Name: "grok-relay", Kind: "openai", BaseURL: "https://relay.example/v1", Model: "grok-4.5", APIKeyEnv: "GROK_API_KEY"}
	s.addProviderAccess([]config.ProviderEntry{added})
	if got := cfg.Desktop.ProviderAccess; len(got) != 1 || got[0] != "grok-relay" {
		t.Fatalf("explicit empty access should enable only the added provider, got %v", got)
	}
}

func TestProviderSetupSessionAddAccessSeedsUndeclaredLegacyProviders(t *testing.T) {
	cfg := setupTestConfig()
	cfg.Desktop.ProviderAccess = nil
	s := newProviderSetupSession(cfg)
	added := config.ProviderEntry{Name: "grok-relay", Kind: "openai", BaseURL: "https://relay.example/v1", Model: "grok-4.5", APIKeyEnv: "GROK_API_KEY"}
	s.addProviderAccess([]config.ProviderEntry{added, added})
	if got := cfg.Desktop.ProviderAccess; !containsString(got, "cli-provider") || !containsString(got, "grok-relay") {
		t.Fatalf("undeclared legacy access should preserve configured siblings and add the new provider: %v", got)
	}
	count := 0
	for _, name := range cfg.Desktop.ProviderAccess {
		if name == "grok-relay" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("added provider access should be deduplicated: %v", cfg.Desktop.ProviderAccess)
	}
}

func TestNewProviderSetupSessionDetectsExplicitProviderAccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[desktop]\nprovider_access = []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newProviderSetupSessionForPath(setupTestConfig(), path)
	if !s.accessDeclared {
		t.Fatal("explicit empty desktop.provider_access was treated as undeclared")
	}
}

func TestProviderSetupSessionPersistsEmptyAccessAfterLastRemoval(t *testing.T) {
	isolateUserConfig(t)
	cfg := setupTestConfig()
	cfg.Desktop.ProviderAccess = []string{"desktop-provider"}
	s := newProviderSetupSession(cfg)
	s.accessDeclared = true
	if err := s.remove("desktop-provider"); err != nil {
		t.Fatal(err)
	}
	if cfg.Desktop.ProviderAccess == nil || len(cfg.Desktop.ProviderAccess) != 0 {
		t.Fatalf("last removal should retain an explicit empty access list: %#v", cfg.Desktop.ProviderAccess)
	}
	path := config.UserConfigPath()
	if err := cfg.SaveTo(path); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "provider_access = []") {
		t.Fatalf("explicit empty provider access was omitted from saved config:\n%s", body)
	}
}

func TestProviderSetupSessionAllowsSharedCredentialName(t *testing.T) {
	cfg := setupTestConfig()
	s := newProviderSetupSession(cfg)
	shared := config.ProviderEntry{Name: "second-relay", Kind: "openai", BaseURL: "https://other.example/v1", Model: "grok-4.5", APIKeyEnv: "SHARED_API_KEY"}
	if err := s.upsert([]config.ProviderEntry{shared}); err != nil {
		t.Fatalf("intentional shared api_key_env should be valid: %v", err)
	}
	if err := s.setCredential("SHARED_API_KEY", "shared-secret"); err != nil {
		t.Fatal(err)
	}
	if got := s.credentialLines(); len(got) != 1 || got[0] != "SHARED_API_KEY=shared-secret" {
		t.Fatalf("credential lines = %v", got)
	}
}

func TestProviderSetupSessionCancelDoesNotWriteFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	original := []byte("default_model = \"keep\"\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := setupTestConfig()
	s := newProviderSetupSession(cfg)
	if err := s.upsert([]config.ProviderEntry{{Name: "staged", Kind: "openai", BaseURL: "https://staged.example/v1", Model: "staged-model", APIKeyEnv: "STAGED_API_KEY"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.setCredential("STAGED_API_KEY", "not-written"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("staging changed config on disk: %q", got)
	}
}

func TestRestoreEnvironmentRemovesSetupPromptValues(t *testing.T) {
	const key = "REASONIX_SETUP_MANAGER_TEMP_KEY"
	_ = os.Unsetenv(key)
	snapshot := snapshotEnvironment()
	if err := os.Setenv(key, "temporary"); err != nil {
		t.Fatal(err)
	}
	restoreEnvironment(snapshot)
	if _, exists := os.LookupEnv(key); exists {
		t.Fatalf("%s leaked after restoring setup environment", key)
	}
}

func TestPromptOptionalAPIKeyEnvNameAllowsNoAuthProvider(t *testing.T) {
	var out bytes.Buffer
	got := promptOptionalAPIKeyEnvName(bufio.NewScanner(strings.NewReader("\n")), &out, "API key variable", "")
	if got != "" {
		t.Fatalf("optional API key variable = %q, want empty", got)
	}
}

func TestProviderSetupSessionSummaryReportsChanges(t *testing.T) {
	cfg := setupTestConfig()
	s := newProviderSetupSession(cfg)
	if err := s.remove("cli-provider"); err != nil {
		t.Fatal(err)
	}
	if err := s.upsert([]config.ProviderEntry{{Name: "grok-relay", Kind: "openai", BaseURL: "https://relay.example/v1", Model: "grok-4.5", APIKeyEnv: "GROK_API_KEY"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.setCredential("GROK_API_KEY", "secret"); err != nil {
		t.Fatal(err)
	}
	text := strings.Join(s.summary(), "\n")
	for _, want := range []string{"grok-relay", "cli-provider", "1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("summary %q missing %q", text, want)
		}
	}
}

func TestResolveSetupTargetsLocalKeepsGlobalCredentialTarget(t *testing.T) {
	targets := resolveSetupTargets([]string{"--local"})
	if targets.config != "reasonix.toml" {
		t.Fatalf("local config target = %q", targets.config)
	}
	if targets.env != config.CredentialsTargetDescription() {
		t.Fatalf("credential target = %q, want global %q", targets.env, config.CredentialsTargetDescription())
	}
}

func TestLocalSetupPersistsWorkspaceProviderAccess(t *testing.T) {
	cfg := setupTestConfig()
	cfg.Desktop.ProviderAccess = []string{"grok-relay"}
	path := filepath.Join(t.TempDir(), "reasonix.toml")
	if err := cfg.SaveTo(path); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "[desktop]") || !strings.Contains(text, `provider_access = ["grok-relay"]`) {
		t.Fatalf("local setup omitted workspace desktop access:\n%s", text)
	}
	if strings.Contains(text, "theme_style") || strings.Contains(text, "default_tool_approval_mode") {
		t.Fatalf("local setup leaked user-global desktop preferences:\n%s", text)
	}
}
