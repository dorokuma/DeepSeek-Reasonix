package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/joho/godotenv"

	"reasonix/internal/i18n"
)

// Config holds all bridge configuration.
type Config struct {
	BotToken     string
	AllowedUsers []int64
	Model        string // default model, empty = use reasonix config
	StateDir     string
	WorkDir      string
	SessionDir   string // REASONIX_SESSION_DIR
	ReasonixBin  string // reasonix binary path
	NotificationMode string
	secrets      []string
}

func loadConfig() (*Config, error) {
	// Load .env if present (dev convenience)
	_ = godotenv.Load()
	// 1. Load /etc/reasonix-bridge.env if exists
	envFile := "/etc/reasonix-bridge.env"
	if _, err := os.Stat(envFile); err == nil {
		if err := godotenv.Load(envFile); err != nil {
			return nil, fmt.Errorf("load env file %s: %w", envFile, err)
		}
	}

	allowedRaw := os.Getenv("ALLOWED_USERS")
	var allowed []int64
	for _, s := range strings.Split(allowedRaw, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		uid, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("ALLOWED_USERS: invalid user id %q: %w", s, err)
		}
		allowed = append(allowed, uid)
	}

	cfg := &Config{
		BotToken:     os.Getenv("TG_BOT_TOKEN"),
		AllowedUsers: allowed,
		Model:        os.Getenv("MODEL"),
		StateDir:     os.Getenv("STATE_DIR"),
		WorkDir:      os.Getenv("WORK_DIR"),
		SessionDir:   os.Getenv("REASONIX_SESSION_DIR"),
		ReasonixBin:  os.Getenv("REASONIX_BIN"),
	}
	if cfg.ReasonixBin == "" {
		cfg.ReasonixBin = "/root/reasonix/reasonix"
	}
	cfg.secrets = []string{cfg.BotToken}
	cfg.NotificationMode = os.Getenv("NOTIFICATION_MODE")

	// 2. Defaults
	if cfg.BotToken == "" {
		return nil, fmt.Errorf("TG_BOT_TOKEN is required")
	}
	if cfg.StateDir == "" {
		cfg.StateDir = "/var/lib/reasonix-bridge"
	}
	if cfg.WorkDir == "" {
		cfg.WorkDir = "/root"
	}

	// 3. API keys via *_FILE (§3.2.3)
	// If DEEPSEEK_API_KEY_FILE is set, read the key from that file and set DEEPSEEK_API_KEY.
	for _, pair := range []struct{ fileEnv, targetEnv string }{
		{"DEEPSEEK_API_KEY_FILE", "DEEPSEEK_API_KEY"},
		{"JINA_API_KEY_FILE", "JINA_API_KEY"},
		{"CF_TOKEN_FILE", "CF_TOKEN"},
	} {
		if path := os.Getenv(pair.fileEnv); path != "" {
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("read %s (%s): %w", pair.fileEnv, path, err)
			}
			os.Setenv(pair.targetEnv, strings.TrimSpace(string(data)))
		}
	}

	// 旧的 /etc/reasonix-api.env 也加载
	apiEnvFile := "/etc/reasonix-api.env"
	if _, err := os.Stat(apiEnvFile); err == nil {
		if err := godotenv.Load(apiEnvFile); err != nil {
			return nil, fmt.Errorf("load api env %s: %w", apiEnvFile, err)
		}
	}

	// 加载用户 credentials（/root/.config/reasonix/credentials）
	credFile := "/root/.config/reasonix/credentials"
	if _, err := os.Stat(credFile); err == nil {
		if err := godotenv.Load(credFile); err != nil {
			return nil, fmt.Errorf("load credentials %s: %w", credFile, err)
		}
		log.Printf("loaded credentials from %s", credFile)
	}

	// 4. Collect all secrets for log redaction before clearing env.
	// Keep provider/plugin keys (JINA/DEEPSEEK/OPENCODE) in the process env so
	// MCP plugins and boot.Build can expand ${JINA_API_KEY} etc. Only clear the
	// Telegram bot tokens from the environment after capturing them for redaction.
	secretSources := []string{"TG_BOT_TOKEN", "TG_CRON_BOT_TOKEN", "CF_TOKEN"}
	for _, key := range secretSources {
		if v := os.Getenv(key); v != "" {
			// Deduplicate against already-collected secrets.
			found := false
			for _, s := range cfg.secrets {
				if s == v {
					found = true
					break
				}
			}
			if !found {
				cfg.secrets = append(cfg.secrets, v)
			}
		}
	}

	// 5. Clear env secrets to prevent /proc leak (§3.2.3, §9.1)
	for _, key := range secretSources {
		os.Unsetenv(key)
	}

	return cfg, nil
}

func validatePaths(cfg *Config) error {
	// Check writability of STATE_DIR, SessionDir, WORK_DIR (§9.1)
	paths := []string{cfg.StateDir}
	if cfg.SessionDir != "" {
		paths = append(paths, cfg.SessionDir)
	}
	if cfg.WorkDir != "" {
		paths = append(paths, cfg.WorkDir)
		// Also check WORK_DIR/reasonix.toml parent dir for permanent allow (§3.2.4)
		tomlDir := filepath.Dir(filepath.Join(cfg.WorkDir, "reasonix.toml"))
		paths = append(paths, tomlDir)
	}
	for _, p := range paths {
		if err := checkWritable(p); err != nil {
			return fmt.Errorf("path %s: %w", p, err)
		}
	}
	return nil
}

func checkWritable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Try to create
			if err := os.MkdirAll(path, 0700); err != nil {
				return fmt.Errorf("cannot create: %w", err)
			}
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("not a directory")
	}
	// Write test
	testFile := filepath.Join(path, ".write_test")
	if err := os.WriteFile(testFile, []byte{}, 0600); err != nil {
		return fmt.Errorf("not writable: %w", err)
	}
	os.Remove(testFile)
	return nil
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Println("reasonix-bridge v2 starting...")

	// Telegram 壳对用户一律中文；核心 notice/斜杠文案走 i18n，必须先锁定 zh。
	// 配置/环境里的 language 也强制中文，禁止掉出英文提示。
	_ = os.Setenv("REASONIX_LANG", "zh")
	i18n.DetectLanguage("zh")

	// Load config
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	// Validate paths
	if err := validatePaths(cfg); err != nil {
		log.Fatalf("path validation: %v", err)
	}

	// Install secret log redaction before any logging of secrets (§9.1, #14)
	installSecretLogRedaction(cfg.secrets)

	// Set session directory in reasonix config (§3.2.1)
	// REASONIX_SESSION_DIR env var takes priority in SessionDir() resolution.
	if cfg.SessionDir != "" {
		os.Setenv("REASONIX_SESSION_DIR", cfg.SessionDir)
	}

	// Create bridge
	bridge, err := NewBridge(cfg)
	if err != nil {
		log.Fatalf("NewBridge: %v", err)
	}

	// Create state store and load existing session index (§3.2.2, #4, #6, #11, #16)
	store, err := newStateStore(cfg.StateDir)
	if err != nil {
		log.Fatalf("stateStore: %v", err)
	}
	bridge.sm.SetStore(store)
	if err := bridge.sm.LoadIndex(); err != nil {
		log.Printf("warning: load session index: %v", err)
	}

	// Start long-polling in background
	go func() {
		if err := bridge.Start(); err != nil {
			log.Printf("bridge poll stopped: %v", err)
		}
	}()

	// Load and start cron tasks (§3.7)
	if err := bridge.cron.Load(); err != nil {
		log.Printf("warning: load cron tasks: %v", err)
	}
	bridge.cron.Start()

	// Handle signals (§9.2)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	<-sigCh
	log.Println("received shutdown signal")

	// Graceful shutdown (§9.2): stop cron → stop polling → index flush → snapshot each controller → close
	bridge.cron.Stop()
	bridge.Shutdown()
	log.Println("bridge shutdown complete")
}
