package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const managementBasePath = "/plugins/" + pluginID

type publicSettings struct {
	Enabled                  bool       `json:"enabled"`
	MaxConcurrencyPerAccount int        `json:"max_concurrency_per_account"`
	QueueTimeoutSeconds      int        `json:"queue_timeout_seconds"`
	QuotaSource              string     `json:"quota_source"`
	ManagerPlusKeyConfigured bool       `json:"manager_plus_key_configured"`
	FiveHour                 WindowRule `json:"five_hour"`
	Weekly                   WindowRule `json:"weekly"`
	MatchPolicy              string     `json:"match_policy"`
	QueryFailurePolicy       string     `json:"query_failure_policy"`
}

type statusAccount struct {
	AccountSnapshot
	Concurrency limiterSnapshot `json:"concurrency"`
	Limit       int             `json:"limit"`
	Effective   AccountSettings `json:"effective_settings"`
	Override    AccountOverride `json:"override"`
	Decision    QuotaDecision   `json:"decision"`
}

type statusSummary struct {
	Schedulable int `json:"schedulable"`
	Total       int `json:"total"`
	Active      int `json:"active"`
	Queued      int `json:"queued"`
	Blocked     int `json:"blocked"`
}

type statusPayload struct {
	PluginID     string          `json:"plugin_id"`
	Version      string          `json:"version"`
	GeneratedAt  time.Time       `json:"generated_at"`
	RefreshError string          `json:"refresh_error,omitempty"`
	Settings     publicSettings  `json:"settings"`
	Summary      statusSummary   `json:"summary"`
	Accounts     []statusAccount `json:"accounts"`
}

func registerManagement() pluginapi.ManagementRegistrationResponse {
	return pluginapi.ManagementRegistrationResponse{
		Resources: []pluginapi.ResourceRoute{{Path: "/status", Menu: "调度控制", Description: "Codex 账号并发与额度调度控制。"}},
		Routes: []pluginapi.ManagementRoute{
			{Method: http.MethodGet, Path: managementBasePath + "/status", Description: "读取账号调度状态。"},
			{Method: http.MethodPut, Path: managementBasePath + "/settings", Description: "保存全局调度设置。"},
			{Method: http.MethodPut, Path: managementBasePath + "/account", Description: "保存账号独立设置。"},
			{Method: http.MethodPost, Path: managementBasePath + "/refresh", Description: "立即查询全部账号额度。"},
		},
	}
}

func handleManagement(raw []byte) ([]byte, error) {
	var request pluginapi.ManagementRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	current := currentService()
	if current == nil {
		return okEnvelope(jsonResponse(http.StatusServiceUnavailable, map[string]string{"error": "plugin service unavailable"}))
	}
	path := normalizeManagementPath(request.Path)
	method := strings.ToUpper(request.Method)
	if isResourcePath(request.Path) {
		if method != http.MethodGet || path != "/status" {
			return okEnvelope(jsonResponse(http.StatusNotFound, map[string]string{"error": "not found"}))
		}
		return okEnvelope(htmlResponse(http.StatusOK, []byte(statusPageHTML)))
	}
	switch {
	case method == http.MethodGet && path == "/status":
		refreshErr := current.RefreshAccounts(nil)
		return okEnvelope(jsonResponse(http.StatusOK, buildStatus(current, refreshErr)))
	case method == http.MethodPost && path == "/refresh":
		if err := current.RefreshAccounts(nil); err != nil {
			return okEnvelope(jsonResponse(http.StatusBadGateway, map[string]string{"error": err.Error()}))
		}
		return okEnvelope(jsonResponse(http.StatusOK, buildStatus(current, nil)))
	case method == http.MethodPut && path == "/settings":
		var settings GlobalSettings
		if err := json.Unmarshal(request.Body, &settings); err != nil {
			return okEnvelope(jsonResponse(http.StatusBadRequest, map[string]string{"error": "invalid JSON body"}))
		}
		// 与 codex-keepalive 页面一致，复用管理中心“记住凭证”后随请求
		// 携带的管理密钥，不要求用户在插件页面再次输入。
		if settings.QuotaSource == quotaSourceManagerPlus {
			if baseURL := managementOrigin(request.Headers); baseURL != "" {
				settings.ManagerPlusBaseURL = baseURL
			}
			if key := managementBearerToken(request.Headers); key != "" {
				settings.ManagerPlusManagementKey = key
			}
		}
		if err := current.SaveGlobalSettings(settings); err != nil {
			return okEnvelope(jsonResponse(http.StatusBadRequest, map[string]string{"error": err.Error()}))
		}
		return okEnvelope(jsonResponse(http.StatusOK, map[string]bool{"ok": true}))
	case method == http.MethodPut && path == "/account":
		var payload struct {
			AuthID   string          `json:"auth_id"`
			Override AccountOverride `json:"override"`
		}
		if err := json.Unmarshal(request.Body, &payload); err != nil {
			return okEnvelope(jsonResponse(http.StatusBadRequest, map[string]string{"error": "invalid JSON body"}))
		}
		if err := current.SaveAccountOverride(payload.AuthID, payload.Override); err != nil {
			return okEnvelope(jsonResponse(http.StatusBadRequest, map[string]string{"error": err.Error()}))
		}
		return okEnvelope(jsonResponse(http.StatusOK, map[string]bool{"ok": true}))
	default:
		return okEnvelope(jsonResponse(http.StatusNotFound, map[string]string{"error": "not found"}))
	}
}

func managementBearerToken(headers http.Header) string {
	value := strings.TrimSpace(headers.Get("Authorization"))
	scheme, token, ok := strings.Cut(value, " ")
	if !ok || !strings.EqualFold(strings.TrimSpace(scheme), "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

func managementOrigin(headers http.Header) string {
	value := strings.TrimRight(strings.TrimSpace(headers.Get("Origin")), "/")
	if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
		return value
	}
	value = strings.TrimSpace(headers.Get("Referer"))
	if value == "" {
		return ""
	}
	request, err := http.NewRequest(http.MethodGet, value, nil)
	if err != nil || request.URL.Host == "" || (request.URL.Scheme != "http" && request.URL.Scheme != "https") {
		return ""
	}
	return request.URL.Scheme + "://" + request.URL.Host
}

func buildStatus(current *Service, refreshErr error) statusPayload {
	settings, overrides, accounts := current.Snapshot()
	now := time.Now()
	result := statusPayload{
		PluginID:    pluginID,
		Version:     pluginVersion,
		GeneratedAt: now,
		Settings: publicSettings{
			Enabled:                  settings.Enabled,
			MaxConcurrencyPerAccount: settings.MaxConcurrencyPerAccount,
			QueueTimeoutSeconds:      settings.QueueTimeoutSeconds,
			QuotaSource:              settings.QuotaSource,
			ManagerPlusKeyConfigured: settings.ManagerPlusManagementKey != "",
			FiveHour:                 settings.FiveHour,
			Weekly:                   settings.Weekly,
			MatchPolicy:              settings.MatchPolicy,
			QueryFailurePolicy:       settings.QueryFailurePolicy,
		},
	}
	if refreshErr != nil {
		result.RefreshError = refreshErr.Error()
	}
	result.Summary.Total = len(accounts)
	for _, account := range accounts {
		override, custom := overrides[account.AuthID]
		if !custom {
			override = AccountOverride{UseGlobal: true, Settings: accountSettingsFromGlobal(settings)}
		}
		effective := accountSettingsFromGlobal(settings)
		if custom && !override.UseGlobal {
			effective = override.Settings
		}
		load := current.limiter.Snapshot(account.AuthID)
		decision := current.accountDecision(account, effective, settings, now)
		status := statusAccount{AccountSnapshot: account, Concurrency: load, Limit: effective.MaxConcurrencyPerAccount, Effective: effective, Override: override, Decision: decision}
		result.Accounts = append(result.Accounts, status)
		result.Summary.Active += load.Active
		result.Summary.Queued += load.Queued
		if decision.Blocked {
			result.Summary.Blocked++
		} else if !account.Disabled && !account.Unavailable {
			result.Summary.Schedulable++
		}
	}
	return result
}

func normalizeManagementPath(path string) string {
	for _, prefix := range []string{"/v0/management" + managementBasePath, "/v0/resource" + managementBasePath, managementBasePath} {
		if strings.HasPrefix(path, prefix) {
			trimmed := strings.TrimPrefix(path, prefix)
			if trimmed == "" {
				return "/"
			}
			return trimmed
		}
	}
	return path
}

func isResourcePath(path string) bool {
	return strings.HasPrefix(path, "/v0/resource"+managementBasePath)
}

func jsonResponse(status int, value any) pluginapi.ManagementResponse {
	raw, _ := json.Marshal(value)
	return pluginapi.ManagementResponse{StatusCode: status, Headers: http.Header{"Content-Type": []string{"application/json; charset=utf-8"}, "Cache-Control": []string{"no-store"}}, Body: raw}
}

func htmlResponse(status int, body []byte) pluginapi.ManagementResponse {
	return pluginapi.ManagementResponse{
		StatusCode: status,
		Headers: http.Header{
			"Content-Type":            []string{"text/html; charset=utf-8"},
			"Cache-Control":           []string{"no-store"},
			"Content-Security-Policy": []string{"default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; frame-ancestors 'self'; base-uri 'none'; form-action 'none'"},
			"Referrer-Policy":         []string{"no-referrer"},
			"X-Content-Type-Options":  []string{"nosniff"},
		},
		Body: body,
	}
}
