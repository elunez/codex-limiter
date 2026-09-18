package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecodeConfigDefaults(t *testing.T) {
	cfg, err := decodeConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Defaults.MaxConcurrencyPerAccount != 3 || cfg.Defaults.QueueTimeoutSeconds != 300 {
		t.Fatalf("defaults = %+v", cfg.Defaults)
	}
	if cfg.Defaults.QuotaSource != quotaSourceRealtime || cfg.Defaults.FiveHour.CutoffPercent != 95 || cfg.Defaults.Weekly.CutoffPercent != 95 {
		t.Fatalf("quota defaults = %+v", cfg.Defaults)
	}
	if filepath.Base(cfg.StatePath) != "state.json" {
		t.Fatalf("state path = %q", cfg.StatePath)
	}
	if filepath.Base(filepath.Dir(cfg.StatePath)) != pluginID {
		t.Fatalf("state directory = %q", filepath.Dir(cfg.StatePath))
	}
}

func TestDecodeConfigOverrides(t *testing.T) {
	raw := []byte("max_concurrency_per_account: 3\nqueue_timeout_seconds: 45\nfive_hour_cutoff_percent: 85\nweekly_cutoff_percent: 92\nquery_failure_policy: deny\n")
	cfg, err := decodeConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Defaults.MaxConcurrencyPerAccount != 3 || cfg.Defaults.QueueTimeoutSeconds != 45 || cfg.Defaults.FiveHour.CutoffPercent != 85 || cfg.Defaults.Weekly.CutoffPercent != 92 || cfg.Defaults.QueryFailurePolicy != failureDeny {
		t.Fatalf("config = %+v", cfg.Defaults)
	}
}

func TestDecodeConfigRejectsInvalidValues(t *testing.T) {
	for _, raw := range []string{"max_concurrency_per_account: 0", "max_concurrency_per_account: 65", "queue_timeout_seconds: -1", "queue_timeout_seconds: 86401", "quota_source: other", "query_failure_policy: other"} {
		t.Run(raw, func(t *testing.T) {
			if _, err := decodeConfig([]byte(raw)); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestPluginRegistrationDeclaresRequiredCapabilities(t *testing.T) {
	registration := pluginRegistration()
	if registration.Metadata.Name != "Codex 调度控制" || registration.Metadata.Version == "" || registration.Metadata.Author == "" || registration.Metadata.GitHubRepository != "https://github.com/elunez/codex-limiter" {
		t.Fatalf("metadata = %+v", registration.Metadata)
	}
	capabilities := registration.Capabilities
	if !capabilities.Scheduler || !capabilities.SchedulerAcrossPriorities || !capabilities.ManagementAPI || !capabilities.RequestInterceptor || !capabilities.RequestLifecyclePlugin {
		t.Fatalf("capabilities = %+v", capabilities)
	}
	encoded, err := json.Marshal(registration)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"scheduler_across_priorities":true`) {
		t.Fatalf("registration = %s", encoded)
	}
	if fields := registration.Metadata.ConfigFields; len(fields) != 0 {
		t.Fatalf("installation page should not expose plugin settings: %+v", fields)
	}
}

func TestNormalizeAccountSettingsDefaultsQueryFailurePolicy(t *testing.T) {
	settings, err := normalizeAccountSettings(AccountSettings{MaxConcurrencyPerAccount: 3, QueueTimeoutSeconds: 300, FiveHour: WindowRule{Enabled: true, CutoffPercent: 95}, Weekly: WindowRule{Enabled: true, CutoffPercent: 95}, MatchPolicy: matchAny})
	if err != nil {
		t.Fatal(err)
	}
	if settings.QueryFailurePolicy != failureAllow {
		t.Fatalf("query failure policy = %q", settings.QueryFailurePolicy)
	}
}
