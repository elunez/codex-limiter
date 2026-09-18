package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type capturingHostClient struct {
	fakeHostClient
	request pluginapi.HTTPRequest
}

func (client *capturingHostClient) Do(request pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	client.request = request
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

	decision := evaluateQuota(quota, "", accountSettingsFromGlobal(settings), failureAllow, now)
	if !decision.Blocked || decision.Unknown {
		t.Fatalf("decision = %+v", decision)
	}
}

func jsonNumber(value int64) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
