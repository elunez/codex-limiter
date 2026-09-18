package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	codexUsageURL  = "https://chatgpt.com/backend-api/wham/usage"
	fiveHourSecs   = int64(5 * time.Hour / time.Second)
	weeklySecs     = int64(7 * 24 * time.Hour / time.Second)
	quotaUserAgent = "codex_cli_rs/0.76.0 (CLIProxyAPI plugin)"
)

type QuotaWindow struct {
	Present     bool      `json:"present"`
	UsedPercent float64   `json:"used_percent"`
	ResetAt     time.Time `json:"reset_at,omitempty"`
	ObservedAt  time.Time `json:"observed_at,omitempty"`
	WindowSecs  int64     `json:"window_seconds,omitempty"`
}

type QuotaSnapshot struct {
	PlanType string      `json:"plan_type,omitempty"`
	FiveHour QuotaWindow `json:"five_hour"`
	Weekly   QuotaWindow `json:"weekly"`
}

type QuotaDecision struct {
	Blocked bool   `json:"blocked"`
	Unknown bool   `json:"unknown"`
	Reason  string `json:"reason"`
}

type authMaterial struct {
	AccessToken string
	AccountID   string
}

type usageWindowPayload struct {
	UsedPercent        *float64 `json:"used_percent"`
	LimitWindowSeconds *float64 `json:"limit_window_seconds"`
	ResetAfterSeconds  *float64 `json:"reset_after_seconds"`
	ResetAt            *float64 `json:"reset_at"`
}

type rateLimitPayload struct {
	PrimaryWindow   *usageWindowPayload `json:"primary_window"`
	SecondaryWindow *usageWindowPayload `json:"secondary_window"`
}

type usagePayload struct {
	PlanType  string            `json:"plan_type"`
	RateLimit *rateLimitPayload `json:"rate_limit"`
}

func fetchRealtimeQuota(host HostClient, auth pluginapi.HostAuthFileEntry, now time.Time) (QuotaSnapshot, error) {
	if strings.TrimSpace(auth.AuthIndex) == "" {
		return QuotaSnapshot{}, errors.New("账号缺少 auth_index")
	}
	credential, err := host.GetAuth(auth.AuthIndex)
	if err != nil {
		return QuotaSnapshot{}, fmt.Errorf("读取账号凭据失败: %w", err)
	}
	material, err := parseAuthMaterial(credential.JSON)
	if err != nil {
		return QuotaSnapshot{}, err
	}
	headers := http.Header{
		"Accept":        []string{"application/json"},
		"Authorization": []string{"Bearer " + material.AccessToken},
		"User-Agent":    []string{quotaUserAgent},
	}
	if material.AccountID != "" {
		headers.Set("Chatgpt-Account-Id", material.AccountID)
	}
	response, err := host.Do(pluginapi.HTTPRequest{Method: http.MethodGet, URL: codexUsageURL, Headers: headers})
	if err != nil {
		return QuotaSnapshot{}, fmt.Errorf("实时额度查询失败: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return QuotaSnapshot{}, fmt.Errorf("实时额度查询返回 HTTP %d", response.StatusCode)
	}
	return parseRealtimeQuota(response.Body, now)
}

func parseRealtimeQuota(raw []byte, now time.Time) (QuotaSnapshot, error) {
	var payload usagePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return QuotaSnapshot{}, fmt.Errorf("解析实时额度失败: %w", err)
	}
	if payload.RateLimit == nil {
		return QuotaSnapshot{}, errors.New("实时额度响应缺少 rate_limit")
	}
	result := QuotaSnapshot{PlanType: strings.ToLower(strings.TrimSpace(payload.PlanType))}
	windows := []*usageWindowPayload{payload.RateLimit.PrimaryWindow, payload.RateLimit.SecondaryWindow}
	for index, item := range windows {
		window, ok := normalizeUsageWindow(item, now)
		if !ok {
			continue
		}
		switch classifyQuotaWindow(window.WindowSecs, index) {
		case "five_hour":
			result.FiveHour = window
		case "weekly":
			result.Weekly = window
		}
	}
	if !result.FiveHour.Present && !result.Weekly.Present {
		return QuotaSnapshot{}, errors.New("实时额度响应没有 5 小时或周窗口")
	}
	return result, nil
}

func normalizeUsageWindow(raw *usageWindowPayload, now time.Time) (QuotaWindow, bool) {
	if raw == nil || raw.UsedPercent == nil || *raw.UsedPercent < 0 || *raw.UsedPercent > 100 {
		return QuotaWindow{}, false
	}
	window := QuotaWindow{Present: true, UsedPercent: *raw.UsedPercent, ObservedAt: now}
	if raw.LimitWindowSeconds != nil {
		window.WindowSecs = int64(*raw.LimitWindowSeconds + 0.5)
	}
	if raw.ResetAt != nil && *raw.ResetAt > 0 {
		window.ResetAt = time.Unix(int64(*raw.ResetAt), 0)
	} else if raw.ResetAfterSeconds != nil && *raw.ResetAfterSeconds >= 0 {
		window.ResetAt = now.Add(time.Duration(*raw.ResetAfterSeconds * float64(time.Second)))
	}
	return window, true
}

func classifyQuotaWindow(seconds int64, order int) string {
	switch {
	case seconds == fiveHourSecs:
		return "five_hour"
	case seconds == weeklySecs:
		return "weekly"
	case seconds == 0 && order == 0:
		return "five_hour"
	case seconds == 0 && order == 1:
		return "weekly"
	default:
		return "unknown"
	}
}

func parseAuthMaterial(raw json.RawMessage) (authMaterial, error) {
	var root map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &root) != nil {
		return authMaterial{}, errors.New("Codex 账号凭据无效")
	}
	material := authMaterial{
		AccessToken: firstString(root, "access_token", "accessToken", "oauth_access_token", "oauthAccessToken", "token", "id_token", "idToken"),
		AccountID:   firstString(root, "account_id", "chatgpt_account_id", "accountId", "chatgptAccountId"),
	}
	for _, key := range []string{"tokens", "credentials", "auth", "oauth", "session"} {
		var nested map[string]json.RawMessage
		if value, ok := root[key]; !ok || json.Unmarshal(value, &nested) != nil {
			continue
		}
		if material.AccessToken == "" {
			material.AccessToken = firstString(nested, "access_token", "accessToken", "oauth_access_token", "oauthAccessToken", "token", "id_token", "idToken")
		}
		if material.AccountID == "" {
			material.AccountID = firstString(nested, "account_id", "chatgpt_account_id", "accountId", "chatgptAccountId")
		}
	}
	if material.AccessToken == "" {
		return authMaterial{}, errors.New("Codex 账号凭据缺少访问令牌")
	}
	return material, nil
}

func firstString(document map[string]json.RawMessage, keys ...string) string {
	for _, key := range keys {
		var value string
		if raw, ok := document[key]; ok && json.Unmarshal(raw, &value) == nil {
			if value = strings.TrimSpace(value); value != "" {
				return value
			}
		}
	}
	return ""
}

func evaluateQuota(snapshot QuotaSnapshot, queryErr string, settings AccountSettings, failurePolicy string, now time.Time) QuotaDecision {
	if queryErr != "" {
		return QuotaDecision{Blocked: failurePolicy == failureDeny, Unknown: true, Reason: queryErr}
	}
	type check struct {
		name   string
		rule   WindowRule
		window QuotaWindow
	}
	checks := []check{{"5 小时", settings.FiveHour, snapshot.FiveHour}, {"周额度", settings.Weekly, snapshot.Weekly}}
	enabled := 0
	blocked := 0
	unknown := 0
	reasons := make([]string, 0, 2)
	for _, current := range checks {
		if !current.rule.Enabled {
			continue
		}
		enabled++
		if !current.window.Present || (!current.window.ResetAt.IsZero() && !now.Before(current.window.ResetAt)) {
			unknown++
			continue
		}
		if current.window.UsedPercent >= current.rule.CutoffPercent {
			blocked++
			reasons = append(reasons, fmt.Sprintf("%s %.1f%% ≥ %.1f%%", current.name, current.window.UsedPercent, current.rule.CutoffPercent))
		}
	}
	decision := QuotaDecision{}
	if settings.MatchPolicy == matchAll {
		decision.Blocked = enabled > 0 && blocked == enabled
	} else {
		decision.Blocked = blocked > 0
	}
	if !decision.Blocked && unknown > 0 {
		decision.Unknown = true
		decision.Blocked = failurePolicy == failureDeny
	}
	if decision.Blocked && len(reasons) > 0 {
		decision.Reason = strings.Join(reasons, "；")
	} else if decision.Blocked {
		decision.Reason = "额度查询失败，已暂停调度"
	} else if decision.Unknown {
		decision.Reason = "部分额度未知，继续调度"
	} else {
		decision.Reason = "额度低于停止阈值"
	}
	return decision
}
