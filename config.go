package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const pluginID = "codex-limiter"

// pluginVersion 会在发布构建时由 -ldflags 注入。
var pluginVersion = "0.0.3"

const (
	quotaSourceRealtime    = "realtime"
	quotaSourceManagerPlus = "manager_plus"
	matchAny               = "any"
	matchAll               = "all"
	failureAllow           = "allow"
	failureDeny            = "deny"
)

type WindowRule struct {
	Enabled       bool    `json:"enabled"`
	CutoffPercent float64 `json:"cutoff_percent"`
}

type GlobalSettings struct {
	Enabled                  bool       `json:"enabled"`
	MaxConcurrencyPerAccount int        `json:"max_concurrency_per_account"`
	QueueTimeoutSeconds      int        `json:"queue_timeout_seconds"`
	QuotaSource              string     `json:"quota_source"`
	ManagerPlusBaseURL       string     `json:"manager_plus_base_url,omitempty"`
	ManagerPlusManagementKey string     `json:"manager_plus_management_key,omitempty"`
	FiveHour                 WindowRule `json:"five_hour"`
	Weekly                   WindowRule `json:"weekly"`
	MatchPolicy              string     `json:"match_policy"`
	QueryFailurePolicy       string     `json:"query_failure_policy"`
}

type AccountSettings struct {
	MaxConcurrencyPerAccount int        `json:"max_concurrency_per_account"`
	QueueTimeoutSeconds      int        `json:"queue_timeout_seconds"`
	FiveHour                 WindowRule `json:"five_hour"`
	Weekly                   WindowRule `json:"weekly"`
	MatchPolicy              string     `json:"match_policy"`
	QueryFailurePolicy       string     `json:"query_failure_policy"`
}

type AccountOverride struct {
	UseGlobal bool            `json:"use_global"`
	Settings  AccountSettings `json:"settings"`
}

type Config struct {
	StatePath string
	Defaults  GlobalSettings
}

func defaultSettings() GlobalSettings {
	return GlobalSettings{
		Enabled:                  true,
		MaxConcurrencyPerAccount: 3,
		QueueTimeoutSeconds:      300,
		QuotaSource:              quotaSourceRealtime,
		FiveHour:                 WindowRule{Enabled: true, CutoffPercent: 95},
		Weekly:                   WindowRule{Enabled: true, CutoffPercent: 95},
		MatchPolicy:              matchAny,
		QueryFailurePolicy:       failureAllow,
	}
}

func defaultConfig() Config {
	home, _ := os.UserHomeDir()
	return Config{
		StatePath: filepath.Join(home, ".cli-proxy-api", "plugins", pluginID, "state.json"),
		Defaults:  defaultSettings(),
	}
}

func decodeConfig(raw []byte) (Config, error) {
	cfg := defaultConfig()
	values := parseFlatYAML(raw)
	if value := strings.TrimSpace(values["state_path"]); value != "" {
		cfg.StatePath = filepath.Clean(value)
	}
	if value := values["enabled"]; value != "" {
		enabled, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			return Config{}, fmt.Errorf("enabled must be true or false")
		}
		cfg.Defaults.Enabled = enabled
	}
	if value := values["max_concurrency_per_account"]; value != "" {
		count, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return Config{}, fmt.Errorf("max_concurrency_per_account must be an integer")
		}
		cfg.Defaults.MaxConcurrencyPerAccount = count
	}
	if value := values["queue_timeout_seconds"]; value != "" {
		seconds, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return Config{}, fmt.Errorf("queue_timeout_seconds must be an integer")
		}
		cfg.Defaults.QueueTimeoutSeconds = seconds
	}
	if value := strings.TrimSpace(values["quota_source"]); value != "" {
		cfg.Defaults.QuotaSource = strings.ToLower(value)
	}
	if value := strings.TrimSpace(values["manager_plus_base_url"]); value != "" {
		cfg.Defaults.ManagerPlusBaseURL = value
	}
	if value := strings.TrimSpace(values["manager_plus_management_key"]); value != "" {
		cfg.Defaults.ManagerPlusManagementKey = value
	}
	if value := values["five_hour_enabled"]; value != "" {
		enabled, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			return Config{}, fmt.Errorf("five_hour_enabled must be true or false")
		}
		cfg.Defaults.FiveHour.Enabled = enabled
	}
	if value := values["five_hour_cutoff_percent"]; value != "" {
		cutoff, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			return Config{}, fmt.Errorf("five_hour_cutoff_percent must be numeric")
		}
		cfg.Defaults.FiveHour.CutoffPercent = cutoff
	}
	if value := values["weekly_enabled"]; value != "" {
		enabled, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			return Config{}, fmt.Errorf("weekly_enabled must be true or false")
		}
		cfg.Defaults.Weekly.Enabled = enabled
	}
	if value := values["weekly_cutoff_percent"]; value != "" {
		cutoff, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			return Config{}, fmt.Errorf("weekly_cutoff_percent must be numeric")
		}
		cfg.Defaults.Weekly.CutoffPercent = cutoff
	}
	if value := strings.TrimSpace(values["match_policy"]); value != "" {
		cfg.Defaults.MatchPolicy = strings.ToLower(value)
	}
	if value := strings.TrimSpace(values["query_failure_policy"]); value != "" {
		cfg.Defaults.QueryFailurePolicy = strings.ToLower(value)
	}
	if cfg.StatePath == "" {
		return Config{}, fmt.Errorf("state_path must not be empty")
	}
	settings, err := normalizeGlobalSettings(cfg.Defaults)
	if err != nil {
		return Config{}, err
	}
	cfg.Defaults = settings
	return cfg, nil
}

func normalizeGlobalSettings(settings GlobalSettings) (GlobalSettings, error) {
	if settings.MaxConcurrencyPerAccount < 1 || settings.MaxConcurrencyPerAccount > 64 {
		return settings, fmt.Errorf("max_concurrency_per_account must be between 1 and 64")
	}
	if settings.QueueTimeoutSeconds < 0 || settings.QueueTimeoutSeconds > 86400 {
		return settings, fmt.Errorf("queue_timeout_seconds must be between 0 and 86400")
	}
	settings.QuotaSource = strings.ToLower(strings.TrimSpace(settings.QuotaSource))
	if settings.QuotaSource == "" {
		settings.QuotaSource = quotaSourceRealtime
	}
	if settings.QuotaSource != quotaSourceRealtime && settings.QuotaSource != quotaSourceManagerPlus {
		return settings, fmt.Errorf("quota_source must be realtime or manager_plus")
	}
	settings.ManagerPlusBaseURL = strings.TrimRight(strings.TrimSpace(settings.ManagerPlusBaseURL), "/")
	settings.ManagerPlusManagementKey = strings.TrimSpace(settings.ManagerPlusManagementKey)
	if settings.QuotaSource == quotaSourceManagerPlus && (settings.ManagerPlusBaseURL == "" || settings.ManagerPlusManagementKey == "") {
		return settings, fmt.Errorf("manager_plus requires base URL and management key")
	}
	if err := validateRules(settings.FiveHour, settings.Weekly, settings.MatchPolicy); err != nil {
		return settings, err
	}
	settings.MatchPolicy = strings.ToLower(strings.TrimSpace(settings.MatchPolicy))
	settings.QueryFailurePolicy = strings.ToLower(strings.TrimSpace(settings.QueryFailurePolicy))
	if settings.QueryFailurePolicy == "" {
		settings.QueryFailurePolicy = failureAllow
	}
	if settings.QueryFailurePolicy != failureAllow && settings.QueryFailurePolicy != failureDeny {
		return settings, fmt.Errorf("query_failure_policy must be allow or deny")
	}
	return settings, nil
}

func normalizeAccountSettings(settings AccountSettings) (AccountSettings, error) {
	if settings.MaxConcurrencyPerAccount < 1 || settings.MaxConcurrencyPerAccount > 64 {
		return settings, fmt.Errorf("max_concurrency_per_account must be between 1 and 64")
	}
	if settings.QueueTimeoutSeconds < 0 || settings.QueueTimeoutSeconds > 86400 {
		return settings, fmt.Errorf("queue_timeout_seconds must be between 0 and 86400")
	}
	if err := validateRules(settings.FiveHour, settings.Weekly, settings.MatchPolicy); err != nil {
		return settings, err
	}
	settings.MatchPolicy = strings.ToLower(strings.TrimSpace(settings.MatchPolicy))
	settings.QueryFailurePolicy = strings.ToLower(strings.TrimSpace(settings.QueryFailurePolicy))
	if settings.QueryFailurePolicy == "" {
		settings.QueryFailurePolicy = failureAllow
	}
	if settings.QueryFailurePolicy != failureAllow && settings.QueryFailurePolicy != failureDeny {
		return settings, fmt.Errorf("query_failure_policy must be allow or deny")
	}
	return settings, nil
}

func validateRules(fiveHour, weekly WindowRule, matchPolicy string) error {
	for name, rule := range map[string]WindowRule{"five_hour": fiveHour, "weekly": weekly} {
		if rule.Enabled && (rule.CutoffPercent <= 0 || rule.CutoffPercent > 100) {
			return fmt.Errorf("%s cutoff_percent must be within (0, 100]", name)
		}
	}
	matchPolicy = strings.ToLower(strings.TrimSpace(matchPolicy))
	if matchPolicy == "" {
		matchPolicy = matchAny
	}
	if matchPolicy != matchAny && matchPolicy != matchAll {
		return fmt.Errorf("match_policy must be any or all")
	}
	return nil
}

func accountSettingsFromGlobal(settings GlobalSettings) AccountSettings {
	return AccountSettings{
		MaxConcurrencyPerAccount: settings.MaxConcurrencyPerAccount,
		QueueTimeoutSeconds:      settings.QueueTimeoutSeconds,
		FiveHour:                 settings.FiveHour,
		Weekly:                   settings.Weekly,
		MatchPolicy:              settings.MatchPolicy,
		QueryFailurePolicy:       settings.QueryFailurePolicy,
	}
}

// parseFlatYAML 只读取本插件公开的标量配置，宿主通用字段会被忽略。
func parseFlatYAML(raw []byte) map[string]string {
	result := make(map[string]string)
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(strings.SplitN(value, "#", 2)[0])
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
			if unquoted, err := strconv.Unquote(value); err == nil {
				value = unquoted
			} else {
				value = value[1 : len(value)-1]
			}
		}
		result[key] = value
	}
	return result
}

type registration struct {
	SchemaVersion uint32                   `json:"schema_version"`
	Metadata      pluginapi.Metadata       `json:"metadata"`
	Capabilities  registrationCapabilities `json:"capabilities"`
}

type registrationCapabilities struct {
	Scheduler              bool `json:"scheduler"`
	ManagementAPI          bool `json:"management_api"`
	RequestInterceptor     bool `json:"request_interceptor"`
	RequestLifecyclePlugin bool `json:"request_lifecycle_plugin"`
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             "Codex 调度控制",
			Version:          pluginVersion,
			Author:           "Jie",
			GitHubRepository: "https://github.com/elunez/codex-limiter",
			Logo:             "https://raw.githubusercontent.com/router-for-me/CLIProxyAPI/main/docs/logo.png",
			ConfigFields: []pluginapi.ConfigField{
				{Name: "quota_source", Type: pluginapi.ConfigFieldTypeEnum, EnumValues: []string{quotaSourceRealtime, quotaSourceManagerPlus}, Description: "额度来源，默认实时查询。"},
				{Name: "state_path", Type: pluginapi.ConfigFieldTypeString, Description: "页面设置和账号覆盖规则的状态文件。"},
			},
		},
		Capabilities: registrationCapabilities{Scheduler: true, ManagementAPI: true, RequestInterceptor: true, RequestLifecyclePlugin: true},
	}
}
