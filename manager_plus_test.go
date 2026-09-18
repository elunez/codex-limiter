package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type capturingHostClient struct {
	fakeHostClient
	request  pluginapi.HTTPRequest
	requests []pluginapi.HTTPRequest
	do       func(pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error)
}

func (client *capturingHostClient) Do(request pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	client.request = request
	client.requests = append(client.requests, request)
	if client.do != nil {
		return client.do(request)
	}
	return client.httpResult, client.httpErr
}

func TestFetchManagerPlusQuotasUsesCompleteCodexIdentity(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	resetAt := now.Add(time.Hour).UnixMilli()
	host := &capturingHostClient{fakeHostClient: fakeHostClient{
		authJSON: json.RawMessage(`{"tokens":{"account_id":"workspace-a"},"email":"member@example.com","access_token":"secret"}`),
		httpResult: pluginapi.HTTPResponse{
			StatusCode: 200,
			Body:       []byte(`{"items":[{"row_key":"auth-a","windows":[{"provider_window_id":"five-hour","window_kind":"five_hour","used_percent":95,"cycle_end_ms":` + jsonNumber(resetAt) + `,"observed_at_ms":` + jsonNumber(now.UnixMilli()) + `,"duration_seconds":18000,"plan_type":"plus","availability":"active"},{"provider_window_id":"weekly","window_kind":"weekly","used_percent":68,"cycle_end_ms":` + jsonNumber(resetAt) + `,"observed_at_ms":` + jsonNumber(now.UnixMilli()) + `,"duration_seconds":604800,"plan_type":"plus","availability":"active"}]}]}`),
		},
	}}
	auth := pluginapi.HostAuthFileEntry{
		ID:          "auth-a",
		AuthIndex:   "index-a",
		Name:        "codex-a.json",
		Provider:    "codex",
		Label:       "主账号",
		AccountType: "plus",
	}
	settings := defaultSettings()
	settings.QuotaSource = quotaSourceManagerPlus
	settings.ManagerPlusBaseURL = "http://manager.example"
	settings.ManagerPlusManagementKey = "secret"

	quotas, errorsByID := fetchManagerPlusQuotas(host, []pluginapi.HostAuthFileEntry{auth}, settings, now)
	if message := errorsByID[auth.ID]; message != "" {
		t.Fatalf("query error = %q", message)
	}
	quota := quotas[auth.ID]
	if !quota.FiveHour.Present || quota.FiveHour.UsedPercent != 95 || !quota.Weekly.Present || quota.Weekly.UsedPercent != 68 {
		t.Fatalf("quota = %+v", quota)
	}

	var payload struct {
		Accounts []managerPlusQueryAccount `json:"accounts"`
	}
	if err := json.Unmarshal(host.request.Body, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Accounts) != 1 {
		t.Fatalf("accounts = %+v", payload.Accounts)
	}
	identity := payload.Accounts[0].Account
	if identity.AccountSnapshot != "member@example.com" || identity.AuthLabelSnapshot != auth.Label || identity.AuthFileSnapshot != auth.Name || identity.AuthProviderSnapshot != auth.Provider || identity.AuthAccountIDSnapshot != "workspace-a" || identity.AuthProjectIDSnapshot != "" || identity.AuthIndex != auth.AuthIndex || identity.Source != auth.Name {
		t.Fatalf("identity = %+v", identity)
	}
	if len(host.requests) != 1 {
		t.Fatalf("complete manager snapshot unexpectedly used realtime fallback: %+v", host.requests)
	}

	decision := evaluateQuota(quota, "", accountSettingsFromGlobal(settings), failureAllow, now)
	if !decision.Blocked || decision.Unknown {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestFetchManagerPlusQuotasFillsIncompleteSnapshotFromRealtime(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	expiredAt := now.Add(-time.Minute).UnixMilli()
	weeklyReset := now.Add(5 * 24 * time.Hour).UnixMilli()
	managerResponse := pluginapi.HTTPResponse{
		StatusCode: 200,
		Body: []byte(`{"items":[{"row_key":"auth-a","windows":[` +
			`{"provider_window_id":"five-hour","window_kind":"five_hour","model_scope_kind":"family","model_scope_key":"codex_main","used_percent":86,"cycle_end_ms":` + jsonNumber(expiredAt) + `,"observed_at_ms":` + jsonNumber(now.Add(-time.Hour).UnixMilli()) + `,"duration_seconds":18000,"plan_type":"plus","stale":true,"availability":"active"},` +
			`{"provider_window_id":"weekly","window_kind":"weekly","model_scope_kind":"family","model_scope_key":"codex_main","used_percent":0,"cycle_end_ms":` + jsonNumber(weeklyReset) + `,"observed_at_ms":` + jsonNumber(now.UnixMilli()) + `,"duration_seconds":604800,"plan_type":"plus","availability":"active"},` +
			`{"provider_window_id":"code-review-five-hour","window_kind":"five_hour","model_scope_kind":"feature","model_scope_key":"code_review","used_percent":99,"observed_at_ms":` + jsonNumber(now.UnixMilli()) + `,"duration_seconds":18000,"availability":"active"}` +
			`] }]}`),
	}
	realtimeResponse := pluginapi.HTTPResponse{
		StatusCode: 200,
		Body:       []byte(`{"plan_type":"plus","rate_limit":{"primary_window":{"used_percent":65,"limit_window_seconds":18000,"reset_after_seconds":10800},"secondary_window":{"used_percent":30,"limit_window_seconds":604800,"reset_after_seconds":518400}}}`),
	}
	host := &capturingHostClient{fakeHostClient: fakeHostClient{
		authJSON: json.RawMessage(`{"access_token":"token-a","account_id":"account-a"}`),
	}}
	host.do = func(request pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		if request.URL == codexUsageURL {
			return realtimeResponse, nil
		}
		return managerResponse, nil
	}
	auth := pluginapi.HostAuthFileEntry{ID: "auth-a", AuthIndex: "index-a", Provider: "codex"}
	settings := defaultSettings()
	settings.QuotaSource = quotaSourceManagerPlus
	settings.ManagerPlusBaseURL = "http://manager.example"
	settings.ManagerPlusManagementKey = "secret"

	quotas, errorsByID := fetchManagerPlusQuotas(host, []pluginapi.HostAuthFileEntry{auth}, settings, now)
	if message := errorsByID[auth.ID]; message != "" {
		t.Fatalf("query error = %q", message)
	}
	quota := quotas[auth.ID]
	if !quota.FiveHour.Present || quota.FiveHour.UsedPercent != 65 {
		t.Fatalf("five-hour quota = %+v", quota.FiveHour)
	}
	if !quota.Weekly.Present || quota.Weekly.UsedPercent != 30 {
		t.Fatalf("weekly quota = %+v", quota.Weekly)
	}
	var payload struct {
		IncludeInactive bool `json:"include_inactive"`
	}
	if len(host.requests) != 2 || host.requests[1].URL != codexUsageURL {
		t.Fatalf("requests = %+v", host.requests)
	}
	if err := json.Unmarshal(host.requests[0].Body, &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.IncludeInactive {
		t.Fatal("manager query did not request inactive lifecycle data")
	}
	decision := evaluateQuota(quota, "", accountSettingsFromGlobal(settings), failureAllow, now)
	if decision.Unknown || decision.Blocked {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestManagerPlusRealtimeFallbackCacheExpiresAfterFiveMinutes(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	cache := newManagerPlusRealtimeFallbackCache()
	quota := QuotaSnapshot{FiveHour: QuotaWindow{Present: true, UsedPercent: 42}}
	cache.put("auth-a", quota, "", now)

	if cached, err, ok := cache.get("auth-a", now.Add(4*time.Minute)); !ok || err != "" || cached.FiveHour.UsedPercent != 42 {
		t.Fatalf("fallback cache entry missing before expiry: quota=%+v err=%q ok=%v", cached, err, ok)
	}
	if _, _, ok := cache.get("auth-a", now.Add(5*time.Minute)); ok {
		t.Fatal("fallback cache entry should expire after five minutes")
	}
}

func jsonNumber(value int64) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
