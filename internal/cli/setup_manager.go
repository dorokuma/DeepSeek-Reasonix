package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"reasonix/internal/config"
	"reasonix/internal/i18n"
)

type providerSetupSession struct {
	cfg                *config.Config
	originalProviders  map[string]config.ProviderEntry
	originalDefault    string
	pendingCredentials map[string]string
	removed            map[string]bool
	accessDeclared     bool
}

const setupManagerContinue = 2

func newProviderSetupSession(cfg *config.Config) *providerSetupSession {
	s := &providerSetupSession{
		cfg:                cfg,
		originalProviders:  make(map[string]config.ProviderEntry, len(cfg.Providers)),
		originalDefault:    cfg.DefaultModel,
		pendingCredentials: map[string]string{},
		removed:            map[string]bool{},
	}
	for _, p := range cfg.Providers {
		s.originalProviders[p.Name] = p
	}
	return s
}

func newProviderSetupSessionForPath(cfg *config.Config, path string) *providerSetupSession {
	s := newProviderSetupSession(cfg)
	declared, err := config.DesktopProviderAccessDeclared(path)
	if err != nil {
		// LoadForEdit already reports malformed/unreadable config and falls back;
		// keep the conservative policy here so setup never enables hidden siblings.
		s.accessDeclared = true
		return s
	}
	s.accessDeclared = declared
	return s
}

func (s *providerSetupSession) upsert(entries []config.ProviderEntry) error {
	for _, entry := range entries {
		if err := s.cfg.UpsertProvider(entry); err != nil {
			return err
		}
		delete(s.removed, entry.Name)
	}
	return nil
}

func (s *providerSetupSession) remove(name string) error {
	if err := s.cfg.RemoveProvider(name); err != nil {
		return err
	}
	s.removeProviderAccess(name)
	if _, existed := s.originalProviders[name]; existed {
		s.removed[name] = true
	}
	return nil
}

func (s *providerSetupSession) addProviderAccess(entries []config.ProviderEntry) {
	if len(entries) == 0 {
		return
	}
	// Preserve the legacy "undeclared means infer all configured providers"
	// behavior before turning provider_access into an explicit list.
	if !s.accessDeclared && len(s.cfg.Desktop.ProviderAccess) == 0 {
		config.NormalizeLegacyDesktopProviderAccess(s.cfg)
	}
	seen := make(map[string]bool, len(s.cfg.Desktop.ProviderAccess)+len(entries))
	for _, name := range s.cfg.Desktop.ProviderAccess {
		name = strings.TrimSpace(name)
		if name != "" {
			seen[name] = true
		}
	}
	for _, entry := range entries {
		name := strings.TrimSpace(entry.Name)
		if name == "" || seen[name] {
			continue
		}
		s.cfg.Desktop.ProviderAccess = append(s.cfg.Desktop.ProviderAccess, name)
		seen[name] = true
	}
	s.accessDeclared = true
}

func (s *providerSetupSession) removeProviderAccess(name string) {
	name = strings.TrimSpace(name)
	if name == "" || len(s.cfg.Desktop.ProviderAccess) == 0 {
		return
	}
	out := s.cfg.Desktop.ProviderAccess[:0]
	for _, current := range s.cfg.Desktop.ProviderAccess {
		if strings.TrimSpace(current) != name {
			out = append(out, current)
		}
	}
	s.cfg.Desktop.ProviderAccess = out
}

func (s *providerSetupSession) setCredential(key, value string) error {
	key = strings.TrimSpace(key)
	if !config.IsValidCredentialKey(key) {
		return fmt.Errorf("invalid API key variable name %q", key)
	}
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("API key for %s contains a newline", key)
	}
	s.pendingCredentials[key] = value
	return nil
}

func (s *providerSetupSession) credentialLines() []string {
	keys := make([]string, 0, len(s.pendingCredentials))
	for key := range s.pendingCredentials {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, key+"="+s.pendingCredentials[key])
	}
	return lines
}

func (s *providerSetupSession) summary() []string {
	var added, edited []string
	for _, p := range s.cfg.Providers {
		old, existed := s.originalProviders[p.Name]
		switch {
		case !existed:
			added = append(added, p.Name)
		case !providerSetupEqual(old, p):
			edited = append(edited, p.Name)
		}
	}
	var out []string
	if len(added) > 0 {
		out = append(out, fmt.Sprintf(i18n.M.SetupSummaryAddedFmt, strings.Join(added, ", ")))
	}
	if len(edited) > 0 {
		out = append(out, fmt.Sprintf(i18n.M.SetupSummaryEditedFmt, strings.Join(edited, ", ")))
	}
	if len(s.removed) > 0 {
		names := make([]string, 0, len(s.removed))
		for name := range s.removed {
			names = append(names, name)
		}
		sort.Strings(names)
		out = append(out, fmt.Sprintf(i18n.M.SetupSummaryRemovedFmt, strings.Join(names, ", ")))
	}
	if s.cfg.DefaultModel != s.originalDefault {
		out = append(out, fmt.Sprintf(i18n.M.SetupSummaryDefaultFmt, s.cfg.DefaultModel))
	}
	if len(s.pendingCredentials) > 0 {
		out = append(out, fmt.Sprintf(i18n.M.SetupSummaryKeysFmt, len(s.pendingCredentials)))
	}
	if len(out) == 0 {
		out = append(out, i18n.M.SetupSummaryNoChanges)
	}
	return out
}

func providerSetupEqual(a, b config.ProviderEntry) bool {
	// Render-level equality is unnecessary here: the manager only changes these
	// fields, while advanced provider fields are preserved by editing a copy.
	return a.Name == b.Name && a.Kind == b.Kind && a.BaseURL == b.BaseURL &&
		a.Model == b.Model && strings.Join(a.Models, "\x00") == strings.Join(b.Models, "\x00") &&
		a.Default == b.Default && a.APIKeyEnv == b.APIKeyEnv
}

func runProviderSetupManager(cfg *config.Config, configPath, envPath string) int {
	repaired, repairs := repairInvalidProviderKeyEnvs(cfg.Providers)
	cfg.Providers = repaired
	for _, repair := range repairs {
		fmt.Fprintf(os.Stderr, "  %s\n", dim(fmt.Sprintf(i18n.M.RepairedAPIKeyEnvFmt, repair.provider, repair.old, repair.new)))
	}
	s := newProviderSetupSessionForPath(cfg, configPath)
	for {
		items := providerManagerItems(s)
		idx, err := selectOne(i18n.M.SetupManagerTitle, items)
		if err != nil {
			fmt.Fprintln(os.Stderr, "\n"+i18n.M.SetupCancelled)
			return 1
		}
		providerCount := len(cfg.Providers)
		switch idx {
		case providerCount:
			if !addProviderToSession(s, false) {
				continue
			}
		case providerCount + 1:
			if !addProviderToSession(s, true) {
				continue
			}
		case providerCount + 2:
			rc := saveProviderSetupSession(s, configPath, envPath)
			if rc == setupManagerContinue {
				continue
			}
			return rc
		case providerCount + 3:
			fmt.Println(i18n.M.SetupCancelled)
			return 1
		default:
			manageProvider(s, idx)
		}
	}
}

func providerManagerItems(s *providerSetupSession) []menuItem {
	cfg := s.cfg
	items := make([]menuItem, 0, len(cfg.Providers)+4)
	for _, p := range cfg.Providers {
		models := p.ModelList()
		keyStatus := i18n.M.SetupKeyMissing
		if p.APIKeyEnv == "" || config.CredentialIsSet(p.APIKeyEnv) || s.pendingCredentials[p.APIKeyEnv] != "" {
			keyStatus = i18n.M.SetupKeySet
		}
		desc := fmt.Sprintf("%s · %d %s · %s", p.Kind, len(models), i18n.M.SetupModelsUnit, keyStatus)
		if cfg.DefaultModel == p.Name || config.ModelRefsProvider(cfg.DefaultModel, p.Name) {
			desc += " · " + i18n.M.SetupDefaultBadge
		}
		items = append(items, menuItem{name: p.Name, desc: desc})
	}
	return append(items,
		menuItem{name: i18n.M.SetupAddOpenAI, desc: i18n.M.CustomProviderDesc},
		menuItem{name: i18n.M.SetupAddAnthropic, desc: i18n.M.AnthropicProviderDesc},
		menuItem{name: i18n.M.SetupSaveExit, desc: i18n.M.SetupSaveExitDesc},
		menuItem{name: i18n.M.SetupCancel, desc: i18n.M.SetupCancelDesc},
	)
}

func addProviderToSession(s *providerSetupSession, anthropic bool) bool {
	envBefore := snapshotEnvironment()
	defer restoreEnvironment(envBefore)

	var entries []config.ProviderEntry
	var err error
	if anthropic {
		entries, err = promptAnthropicProvider()
	} else {
		entries, err = promptCustomProvider()
	}
	if err != nil {
		if err != errCancelled {
			fmt.Fprintln(os.Stderr, err)
		}
		return false
	}
	for _, entry := range entries {
		if !confirmSharedCredential(s.cfg, entry, "") {
			return false
		}
	}
	if err := s.upsert(entries); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return false
	}
	s.addProviderAccess(entries)
	for _, entry := range entries {
		if entry.APIKeyEnv != "" {
			value := os.Getenv(entry.APIKeyEnv)
			if value != "" && value != envBefore[entry.APIKeyEnv] {
				_ = s.setCredential(entry.APIKeyEnv, value)
			}
		}
	}
	return true
}

func manageProvider(s *providerSetupSession, providerIndex int) {
	if providerIndex < 0 || providerIndex >= len(s.cfg.Providers) {
		return
	}
	p := s.cfg.Providers[providerIndex]
	idx, err := selectOne(fmt.Sprintf(i18n.M.SetupProviderActionsFmt, p.Name), []menuItem{
		{name: i18n.M.SetupEditProvider},
		{name: i18n.M.SetupUpdateKey},
		{name: i18n.M.SetupTestRefresh},
		{name: i18n.M.SetupSetDefault},
		{name: i18n.M.SetupRemoveProvider},
		{name: i18n.M.SetupBack},
	})
	if err != nil || idx == 5 {
		return
	}
	switch idx {
	case 0:
		editProvider(s, p)
	case 1:
		updateProviderKey(s, p)
	case 2:
		testAndRefreshProvider(s, p)
	case 3:
		setDefaultProvider(s, p)
	case 4:
		removeProviderFromSession(s, p)
	}
}

func editProvider(s *providerSetupSession, current config.ProviderEntry) {
	in := bufio.NewScanner(os.Stdin)
	edited := current
	edited.BaseURL = ask(in, os.Stdout, i18n.M.CustomPromptBaseURL, current.BaseURL)
	models := ask(in, os.Stdout, i18n.M.SetupPromptModels, strings.Join(current.ModelList(), ","))
	edited.Models = splitModels(models)
	if len(edited.Models) == 1 {
		edited.Model = edited.Models[0]
	} else {
		edited.Model = ""
	}
	if len(edited.Models) > 0 && !containsString(edited.Models, edited.Default) {
		edited.Default = edited.Models[0]
	}
	edited.APIKeyEnv = promptOptionalAPIKeyEnvName(in, os.Stdout, i18n.M.CustomPromptKeyEnv, current.APIKeyEnv)
	if !confirmSharedCredential(s.cfg, edited, current.Name) {
		return
	}
	if err := s.upsert([]config.ProviderEntry{edited}); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}

func promptOptionalAPIKeyEnvName(in *bufio.Scanner, w io.Writer, label, def string) string {
	for {
		key := ask(in, w, label, def)
		if key == "" || config.IsValidCredentialKey(key) {
			return key
		}
		fmt.Fprintf(w, i18n.M.InvalidAPIKeyEnvFmt+"\n", key)
	}
}

func splitModels(raw string) []string {
	seen := map[string]bool{}
	var models []string
	for _, model := range strings.Split(raw, ",") {
		model = strings.TrimSpace(model)
		if model != "" && !seen[model] {
			seen[model] = true
			models = append(models, model)
		}
	}
	return models
}

func confirmSharedCredential(cfg *config.Config, candidate config.ProviderEntry, ignoreName string) bool {
	if candidate.APIKeyEnv == "" {
		return true
	}
	for _, p := range cfg.Providers {
		if p.Name == ignoreName || p.Name == candidate.Name || p.APIKeyEnv != candidate.APIKeyEnv || p.BaseURL == candidate.BaseURL {
			continue
		}
		in := bufio.NewScanner(os.Stdin)
		answer := ask(in, os.Stdout, fmt.Sprintf(i18n.M.SetupSharedKeyWarningFmt, candidate.APIKeyEnv, p.Name, p.BaseURL), "y/N")
		return answer == "y" || answer == "Y"
	}
	return true
}

func updateProviderKey(s *providerSetupSession, p config.ProviderEntry) {
	in := bufio.NewScanner(os.Stdin)
	if p.APIKeyEnv == "" {
		p.APIKeyEnv = promptAPIKeyEnvName(in, os.Stdout, i18n.M.CustomPromptKeyEnv, apiKeyEnvFromProviderName(p.Name))
		if !confirmSharedCredential(s.cfg, p, p.Name) {
			return
		}
		if err := s.upsert([]config.ProviderEntry{p}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return
		}
	}
	value := ask(in, os.Stdout, fmt.Sprintf(i18n.M.SetupPromptAPIKeyFmt, p.APIKeyEnv), "")
	if value == "" {
		return
	}
	if err := s.setCredential(p.APIKeyEnv, value); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}

func testAndRefreshProvider(s *providerSetupSession, p config.ProviderEntry) {
	restore := temporarilySetCredential(p.APIKeyEnv, s.pendingCredentials[p.APIKeyEnv])
	defer restore()
	p.ResolveAPIKeyFromProcessEnvForProbe()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	models, err := p.FetchModels(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, i18n.M.FetchModelsFailedFmt+"\n", p.Name, err)
		return
	}
	if len(models) == 0 {
		fmt.Fprintln(os.Stderr, i18n.M.CustomFetchEmpty)
		return
	}
	items := make([]menuItem, len(models))
	for i, model := range models {
		items[i] = menuItem{name: model}
	}
	idxs, err := selectMany(fmt.Sprintf(i18n.M.SelectModelsLabel, p.Name), items)
	if err != nil || len(idxs) == 0 {
		return
	}
	selected := make([]string, 0, len(idxs))
	for _, idx := range idxs {
		selected = append(selected, models[idx])
	}
	p.Models = selected
	p.Model = ""
	if !containsString(selected, p.Default) {
		p.Default = selected[0]
	}
	if err := s.upsert([]config.ProviderEntry{p}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	fmt.Printf("  %s\n", green(fmt.Sprintf(i18n.M.FetchModelsSuccessFmt, len(models), p.Name)))
}

func snapshotEnvironment() map[string]string {
	snapshot := map[string]string{}
	for _, assignment := range os.Environ() {
		if key, value, ok := strings.Cut(assignment, "="); ok {
			snapshot[key] = value
		}
	}
	return snapshot
}

func restoreEnvironment(snapshot map[string]string) {
	for _, assignment := range os.Environ() {
		key, _, ok := strings.Cut(assignment, "=")
		if !ok {
			continue
		}
		if _, existed := snapshot[key]; !existed {
			_ = os.Unsetenv(key)
		}
	}
	for key, value := range snapshot {
		_ = os.Setenv(key, value)
	}
}

func temporarilySetCredential(key, value string) func() {
	if key == "" || value == "" {
		return func() {}
	}
	old, existed := os.LookupEnv(key)
	_ = os.Setenv(key, value)
	return func() {
		if existed {
			_ = os.Setenv(key, old)
		} else {
			_ = os.Unsetenv(key)
		}
	}
}

func setDefaultProvider(s *providerSetupSession, p config.ProviderEntry) {
	models := p.ModelList()
	if len(models) == 0 {
		return
	}
	items := make([]menuItem, len(models))
	for i, model := range models {
		items[i] = menuItem{name: model}
	}
	idx, err := selectOne(i18n.M.SetupSelectDefaultModel, items)
	if err != nil {
		return
	}
	if err := s.cfg.SetDefaultModel(p.Name + "/" + models[idx]); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}

func removeProviderFromSession(s *providerSetupSession, p config.ProviderEntry) {
	in := bufio.NewScanner(os.Stdin)
	answer := ask(in, os.Stdout, fmt.Sprintf(i18n.M.SetupConfirmRemoveFmt, p.Name), "y/N")
	if answer != "y" && answer != "Y" {
		return
	}
	if err := s.remove(p.Name); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}

func saveProviderSetupSession(s *providerSetupSession, configPath, envPath string) int {
	fmt.Println()
	fmt.Println(i18n.M.SetupSummaryTitle)
	for _, line := range s.summary() {
		fmt.Println("  " + line)
	}
	in := bufio.NewScanner(os.Stdin)
	answer := ask(in, os.Stdout, i18n.M.SetupConfirmSave, "Y/n")
	if answer == "n" || answer == "N" {
		return setupManagerContinue
	}
	if err := s.cfg.SaveTo(configPath); err != nil {
		fmt.Fprintln(os.Stderr, i18n.M.WriteConfigErr, err)
		return 1
	}
	fmt.Printf("\n%s %s\n", green("✓"), fmt.Sprintf(i18n.M.WroteFileFmt, displayPath(configPath)))
	if lines := s.credentialLines(); len(lines) > 0 {
		target, err := config.StoreCredentialLines(lines)
		if err != nil {
			fmt.Fprintln(os.Stderr, i18n.M.WriteEnvErr, err)
			return 1
		}
		if target == "" {
			target = envPath
		}
		fmt.Printf("%s %s\n", green("✓"), fmt.Sprintf(i18n.M.WroteFileFmt, displayPath(target)))
	}
	fmt.Printf("\n%s %s\n", accent("◆"), i18n.M.SetupComplete)
	return 0
}
