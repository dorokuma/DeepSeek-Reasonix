package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"log/slog"

	"reasonix/internal/config"
	"reasonix/internal/doctor"
)

func doctorCommand(args []string, version string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "print diagnostics as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	// Refresh live model lists so the report shows what the provider actually
	// serves, not just the static config fallback.
	refreshModelLists()

	report := doctor.Collect(doctor.Options{Version: version})
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	}
	fmt.Print(doctor.RenderText(report))
	return 0
}

// refreshModelLists loads the config and refreshes every provider's model list
// from its live API endpoint. Errors are non-fatal — the static config fallback
// remains in place. This is called before doctor reports and the bridge's
// doctor --json polling so the output reflects what the provider actually serves.
func refreshModelLists() {
	cfg, err := config.Load()
	if err != nil {
		slog.Debug("refreshModelLists: load config", "error", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		if p.BaseURL == "" || p.APIKey() == "" {
			continue
		}
		if err := p.RefreshModels(ctx); err != nil {
			slog.Debug("refreshModelLists: provider refresh failed", "provider", p.Name, "error", err)
		}
	}
}
